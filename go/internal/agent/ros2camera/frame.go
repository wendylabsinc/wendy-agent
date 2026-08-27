// Package ros2camera discovers ROS 2 image writers and exposes their frames as
// ordinary V4L2 cameras through Wendy's v4l2loopback support.
package ros2camera

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/rtps/cdr"
)

const (
	TypeImage           = "sensor_msgs::msg::dds_::Image_"
	TypeCompressedImage = "sensor_msgs::msg::dds_::CompressedImage_"
	TypeGo2FrontVideo   = "unitree_go::msg::dds_::Go2FrontVideoData_"
)

var ErrUnsupportedEncoding = errors.New("unsupported ROS 2 image encoding")

// SupportsType reports whether typeName carries a video frame this package can
// decode. DDS and ros2cli spellings are both accepted.
func SupportsType(typeName string) bool {
	switch typeName {
	case TypeImage, "sensor_msgs/msg/Image",
		TypeCompressedImage, "sensor_msgs/msg/CompressedImage",
		TypeGo2FrontVideo, "unitree_go/msg/Go2FrontVideoData":
		return true
	default:
		return false
	}
}

// TopicName turns ROS 2's DDS wire spelling (rt/foo) into the spelling users
// pass to ros2cli (/foo).
func TopicName(topic string) string {
	topic = strings.TrimPrefix(topic, "rt/")
	if !strings.HasPrefix(topic, "/") {
		topic = "/" + topic
	}
	return topic
}

// DecodeJPEG extracts a frame from a serialized ROS 2 sample and returns a
// complete JPEG plus its dimensions. CompressedImage and the Go2's native
// front-video message stay compressed; raw Image frames are converted with the
// standard library so the v4l2loopback writer has one stable MJPEG format.
func DecodeJPEG(typeName string, payload []byte) ([]byte, int, int, error) {
	switch typeName {
	case TypeCompressedImage, "sensor_msgs/msg/CompressedImage":
		return decodeCompressedImage(payload)
	case TypeImage, "sensor_msgs/msg/Image":
		return decodeRawImage(payload)
	case TypeGo2FrontVideo, "unitree_go/msg/Go2FrontVideoData":
		return decodeGo2FrontVideo(payload)
	default:
		return nil, 0, 0, fmt.Errorf("%w: message type %s", ErrUnsupportedEncoding, typeName)
	}
}

func skipHeader(d *cdr.Decoder) error {
	if _, err := d.Int32(); err != nil {
		return fmt.Errorf("header.stamp.sec: %w", err)
	}
	if _, err := d.Uint32(); err != nil {
		return fmt.Errorf("header.stamp.nanosec: %w", err)
	}
	if err := d.SkipString(); err != nil {
		return fmt.Errorf("header.frame_id: %w", err)
	}
	return nil
}

func jpegDimensions(frame []byte) ([]byte, int, int, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(frame))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decoding compressed image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 8192 || cfg.Height > 8192 {
		return nil, 0, 0, fmt.Errorf("invalid compressed image dimensions %dx%d", cfg.Width, cfg.Height)
	}
	if format == "jpeg" {
		return frame, cfg.Width, cfg.Height, nil
	}
	img, _, err := image.Decode(bytes.NewReader(frame))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decoding compressed image: %w", err)
	}
	return encodeJPEG(img)
}

func decodeCompressedImage(payload []byte) ([]byte, int, int, error) {
	d, err := cdr.NewDecoder(payload)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := skipHeader(d); err != nil {
		return nil, 0, 0, err
	}
	if _, err := d.String(); err != nil { // format (jpeg/png plus optional transport metadata)
		return nil, 0, 0, fmt.Errorf("format: %w", err)
	}
	frame, err := d.Bytes()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("data: %w", err)
	}
	return jpegDimensions(frame)
}

func decodeGo2FrontVideo(payload []byte) ([]byte, int, int, error) {
	d, err := cdr.NewDecoder(payload)
	if err != nil {
		return nil, 0, 0, err
	}
	if _, err := d.Uint64(); err != nil {
		return nil, 0, 0, fmt.Errorf("time_frame: %w", err)
	}
	video720p, err := d.Bytes()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("video720p: %w", err)
	}
	video360p, err := d.Bytes()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("video360p: %w", err)
	}
	video180p, err := d.Bytes()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("video180p: %w", err)
	}
	// Prefer the native 720p stream, falling back in descending resolution for
	// firmware variants that leave one of the three fields empty.
	for _, frame := range [][]byte{video720p, video360p, video180p} {
		if len(frame) != 0 {
			return jpegDimensions(frame)
		}
	}
	return nil, 0, 0, errors.New("Go2 front-video sample contains no image")
}

func decodeRawImage(payload []byte) ([]byte, int, int, error) {
	d, err := cdr.NewDecoder(payload)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := skipHeader(d); err != nil {
		return nil, 0, 0, err
	}
	height, err := d.Uint32()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("height: %w", err)
	}
	width, err := d.Uint32()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("width: %w", err)
	}
	encoding, err := d.String()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("encoding: %w", err)
	}
	bigEndian, err := d.Uint8()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("is_bigendian: %w", err)
	}
	step, err := d.Uint32()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("step: %w", err)
	}
	data, err := d.Bytes()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("data: %w", err)
	}
	if width == 0 || height == 0 || width > 8192 || height > 8192 || uint64(step)*uint64(height) > uint64(len(data)) {
		return nil, 0, 0, errors.New("invalid ROS 2 image dimensions")
	}

	var img image.Image
	switch strings.ToLower(encoding) {
	case "mono8", "8uc1":
		if step < width {
			return nil, 0, 0, errors.New("ROS 2 image step is smaller than one row")
		}
		gray := image.NewGray(image.Rect(0, 0, int(width), int(height)))
		for y := 0; y < int(height); y++ {
			copy(gray.Pix[y*gray.Stride:y*gray.Stride+int(width)], data[y*int(step):y*int(step)+int(width)])
		}
		img = gray
	case "mono16", "16uc1":
		if step < width*2 {
			return nil, 0, 0, errors.New("ROS 2 image step is smaller than one row")
		}
		gray := image.NewGray(image.Rect(0, 0, int(width), int(height)))
		for y := 0; y < int(height); y++ {
			row := data[y*int(step):]
			for x := 0; x < int(width); x++ {
				if bigEndian != 0 {
					gray.SetGray(x, y, color.Gray{Y: row[x*2]})
				} else {
					gray.SetGray(x, y, color.Gray{Y: row[x*2+1]})
				}
			}
		}
		img = gray
	case "rgb8", "bgr8", "rgba8", "bgra8":
		channels := 3
		if strings.Contains(strings.ToLower(encoding), "a") {
			channels = 4
		}
		if step < width*uint32(channels) {
			return nil, 0, 0, errors.New("ROS 2 image step is smaller than one row")
		}
		rgba := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
		bgr := strings.HasPrefix(strings.ToLower(encoding), "bgr")
		for y := 0; y < int(height); y++ {
			row := data[y*int(step):]
			for x := 0; x < int(width); x++ {
				i := x * channels
				r, g, b := row[i], row[i+1], row[i+2]
				if bgr {
					r, b = b, r
				}
				// Camera alpha channels are transport padding in practice. JPEG has
				// no alpha, so keep the color fully opaque rather than premultiplying
				// dark pixels when a publisher leaves alpha at zero.
				rgba.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
			}
		}
		img = rgba
	default:
		return nil, 0, 0, fmt.Errorf("%w: %s", ErrUnsupportedEncoding, encoding)
	}
	return encodeJPEG(img)
}

func encodeJPEG(img image.Image) ([]byte, int, int, error) {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, 0, 0, fmt.Errorf("encoding JPEG: %w", err)
	}
	b := img.Bounds()
	return out.Bytes(), b.Dx(), b.Dy(), nil
}
