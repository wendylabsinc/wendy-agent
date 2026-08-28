package ros2camera

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

type cdrBuilder struct{ b []byte }

func newCDRBuilder() *cdrBuilder { return &cdrBuilder{b: []byte{0, 1, 0, 0}} }
func (c *cdrBuilder) align(n int) {
	body := len(c.b) - 4
	for body%n != 0 {
		c.b = append(c.b, 0)
		body++
	}
}
func (c *cdrBuilder) u8(v byte) { c.b = append(c.b, v) }
func (c *cdrBuilder) u32(v uint32) {
	c.align(4)
	c.b = binary.LittleEndian.AppendUint32(c.b, v)
}
func (c *cdrBuilder) u64(v uint64) {
	c.align(8)
	c.b = binary.LittleEndian.AppendUint64(c.b, v)
}
func (c *cdrBuilder) str(v string) {
	c.u32(uint32(len(v) + 1))
	c.b = append(c.b, v...)
	c.b = append(c.b, 0)
}
func (c *cdrBuilder) bytes(v []byte) {
	c.u32(uint32(len(v)))
	c.b = append(c.b, v...)
}
func (c *cdrBuilder) header() {
	c.u32(1)
	c.u32(2)
	c.str("camera")
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDecodeCompressedImagePreservesJPEG(t *testing.T) {
	want := testJPEG(t, 3, 2)
	c := newCDRBuilder()
	c.header()
	c.str("jpeg")
	c.bytes(want)
	got, width, height, err := DecodeJPEG(TypeCompressedImage, c.b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || width != 3 || height != 2 {
		t.Fatalf("got %dx%d, %d bytes; want 3x2, %d bytes", width, height, len(got), len(want))
	}
}

func TestDecodeCompressedImageRejectsNonJPEG(t *testing.T) {
	var frame bytes.Buffer
	if err := png.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	c := newCDRBuilder()
	c.header()
	c.str("png")
	c.bytes(frame.Bytes())
	if _, _, _, err := DecodeJPEG(TypeCompressedImage, c.b); !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("error = %v; want ErrUnsupportedEncoding", err)
	}
}

func TestDecodeRawImageRejectsOversizedDimensions(t *testing.T) {
	c := newCDRBuilder()
	c.header()
	c.u32(1)
	c.u32(maxFrameDimension + 1)
	c.str("mono8")
	c.u8(0)
	c.u32(maxFrameDimension + 1)
	c.bytes(nil)
	if _, _, _, err := DecodeJPEG(TypeImage, c.b); err == nil || !strings.Contains(err.Error(), "invalid image dimensions") {
		t.Fatalf("error = %v; want invalid image dimensions", err)
	}
}

func TestDecodeGo2FrontVideoPrefers720p(t *testing.T) {
	want := testJPEG(t, 4, 3)
	c := newCDRBuilder()
	c.u64(42)
	c.bytes(want)
	c.bytes(nil)
	c.bytes(nil)
	got, width, height, err := DecodeJPEG(TypeGo2FrontVideo, c.b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || width != 4 || height != 3 {
		t.Fatalf("got %dx%d, %d bytes", width, height, len(got))
	}
}

func TestDecodeRawBGR8(t *testing.T) {
	c := newCDRBuilder()
	c.header()
	c.u32(1) // height
	c.u32(2) // width
	c.str("bgr8")
	c.u8(0)
	c.u32(6)
	c.bytes([]byte{0, 0, 255, 0, 255, 0})
	got, width, height, err := DecodeJPEG(TypeImage, c.b)
	if err != nil {
		t.Fatal(err)
	}
	if width != 2 || height != 1 {
		t.Fatalf("dimensions = %dx%d", width, height)
	}
	img, err := jpeg.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(0, 0).RGBA()
	if r <= g || r <= b {
		t.Fatalf("first BGR pixel was not converted to red: r=%d g=%d b=%d", r, g, b)
	}
}

func TestTopicAndTypeRecognition(t *testing.T) {
	if got := TopicName("rt/frontvideostream"); got != "/frontvideostream" {
		t.Fatalf("TopicName = %q", got)
	}
	for _, typ := range []string{TypeImage, TypeCompressedImage, TypeGo2FrontVideo, "sensor_msgs/msg/Image"} {
		if !SupportsType(typ) {
			t.Errorf("SupportsType(%q) = false", typ)
		}
	}
}
