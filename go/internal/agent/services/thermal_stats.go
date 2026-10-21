package services

// Hot and cold spots of a radiometric thermal camera, for viewers.
//
// A thermal viewer wants to point at the hottest and the coldest thing in the
// picture. The picture cannot answer that: it is AGC'd, so its brightest pixel
// is merely the brightest after a per-frame stretch, and H.264 would blur it
// anyway. The answer is in the radiometric block the TOPDON TC001 stacks on top
// of the picture in its 512x484 mode -- the block the raw tap already reads off
// every frame (video_raw_tap.go) and the encoded branch crops away (videocrop
// top=100). So the agent scans it once per frame and sends the result alongside
// the encoded video, in VideoFrame.thermal, where a viewer that never sees a raw
// byte can draw two dots.
//
// The layout, measured on three physical TC001 units (wendy-box-theta, ccr1,
// ccr2) by the wendy-console team:
//
//	512x484 YUYV, 1024 bytes per line.
//	rows   0..95   a 256x192 array of little-endian uint16, one per sensor
//	               pixel, row-major (96 rows x 1024 B = 256*192*2 B)
//	row    96      header
//	row    97      zeros
//	rows  98..483  the AGC'd picture; viewers get rows 100..483 (512x384)
//
// The block and the 512x384 picture are both 4:3 and cover the same scene, so
// sensor (col,row) lands on the picture at ((col+0.5)/256, (row+0.5)/192),
// normalized at pixel centres.
//
// The scale is NOT the community's "/64". In the 512-wide mode a value is
// sixteenths of a kelvin; /64 reads a room as about -198 C. The 256-wide mode
// is the one that uses /64, so the scale is derived from the frame width and
// any other width gets no stats at all rather than a guess. The two scales are
// a factor of four apart, which is the difference between a room and a ~900 C
// furnace, so a frame whose MEAN is not a plausible scene (-40..150 C) is
// refused too: one wrong assumption about a new firmware must produce no dots,
// never confident dots on the wrong pixels.

import (
	"encoding/binary"
	"time"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	// The TC001's radiometric array: one 16-bit value per sensor pixel, stored
	// row-major from the first byte of the frame.
	thermalSensorWidth  = 256
	thermalSensorHeight = 192
	thermalSensorPixels = thermalSensorWidth * thermalSensorHeight
	thermalBlockBytes   = thermalSensorPixels * 2

	kelvinZeroCelsius = 273.15

	// The plausibility guard on a frame's mean temperature. Wide enough for any
	// scene the camera is rated for (a flame is a few pixels, not the mean);
	// narrow enough that a scale off by 4x in either direction always fails it.
	thermalMinMeanCelsius = -40.0
	thermalMaxMeanCelsius = 150.0

	// thermalStatsMaxAge is how old the latest reading may be and still ride on
	// an outgoing frame. Encoded frames are chunks of a byte stream, not
	// pictures, so "the latest reading as of now" is the only honest pairing;
	// past this age the tap has stalled or stopped, and stale dots are worse than
	// none.
	thermalStatsMaxAge = 2 * time.Second
)

// thermalCountsPerKelvin is the radiometric scale of a stacked frame of this
// width: 512 is the TC001's stacked mode (sixteenths of a kelvin, measured), 256
// its half-size mode (sixty-fourths). Any other width has no known scale and
// gets no stats.
func thermalCountsPerKelvin(width uint32) (float64, bool) {
	switch width {
	case 512:
		return 16, true
	case 256:
		return 64, true
	}
	return 0, false
}

// thermalLayout is what a raw tap's geometry says about its radiometric block.
type thermalLayout struct {
	countsPerKelvin float64
	frameBytes      int
}

// thermalLayoutFor checks that frames of this format, with metadataRows stacked
// on top of the picture, carry the TC001 radiometric block, and that the block
// maps onto the picture viewers receive. Every condition is a refusal rather
// than an adaptation: a layout that has not been measured is not guessed at.
func thermalLayoutFor(format *agentpb.RawFormat, metadataRows uint32) (thermalLayout, bool) {
	width, height, bpl := format.GetWidth(), format.GetHeight(), format.GetBytesPerLine()
	countsPerKelvin, ok := thermalCountsPerKelvin(width)
	if !ok {
		return thermalLayout{}, false
	}
	// The block is read as one contiguous run, so lines must be unpadded.
	if format.GetFourcc() != fourccYUYV || bpl != width*yuyvBytesPerPixel {
		return thermalLayout{}, false
	}
	if metadataRows == 0 || metadataRows >= height {
		return thermalLayout{}, false
	}
	// The block must fit inside the rows viewers never see...
	if uint64(metadataRows)*uint64(bpl) < thermalBlockBytes {
		return thermalLayout{}, false
	}
	// ...and the picture they do see must be the sensor grid scaled by one whole
	// factor on both axes, or a normalized sensor position lands somewhere else
	// on screen.
	scale := width / thermalSensorWidth
	if width != scale*thermalSensorWidth || height-metadataRows != scale*thermalSensorHeight {
		return thermalLayout{}, false
	}
	return thermalLayout{countsPerKelvin: countsPerKelvin, frameBytes: int(bpl) * int(height)}, true
}

// thermalStats is one frame's reading. Positions are normalized against the
// picture viewers receive; temperatures are degrees Celsius.
type thermalStats struct {
	maxX, maxY, maxCelsius float32
	minX, minY, minCelsius float32
	// meanCelsius is what the plausibility guard judged. Not on the wire; it is
	// set even when the guard refuses the frame, so the refusal can be logged
	// with the number that caused it.
	meanCelsius float32
}

// computeThermalStats finds the hottest and coldest sensor pixels of one raw
// tap frame whose top metadataRows carry the TC001 radiometric block. Ties go to
// the first pixel in row-major order (top row first, left to right).
//
// One pass over 49,152 values and no allocation: it runs on every frame the
// camera produces, whether or not anyone is watching the raw tap.
func computeThermalStats(frame []byte, format *agentpb.RawFormat, metadataRows uint32) (thermalStats, bool) {
	layout, ok := thermalLayoutFor(format, metadataRows)
	if !ok || len(frame) < layout.frameBytes {
		return thermalStats{}, false
	}
	block := frame[:thermalBlockBytes]

	first := binary.LittleEndian.Uint16(block)
	minV, maxV := first, first
	minI, maxI := 0, 0
	var sum uint64
	for i := 0; i < thermalSensorPixels; i++ {
		v := binary.LittleEndian.Uint16(block[2*i:])
		sum += uint64(v)
		// Strict comparisons: an equal value later in the scan never displaces
		// the first one, so the dot does not wander across a flat region.
		if v > maxV {
			maxV, maxI = v, i
		}
		if v < minV {
			minV, minI = v, i
		}
	}

	st := thermalStats{
		meanCelsius: float32(float64(sum)/thermalSensorPixels/layout.countsPerKelvin - kelvinZeroCelsius),
	}
	if st.meanCelsius < thermalMinMeanCelsius || st.meanCelsius > thermalMaxMeanCelsius {
		return st, false
	}
	st.maxX, st.maxY = thermalPixelCentre(maxI)
	st.minX, st.minY = thermalPixelCentre(minI)
	st.maxCelsius = float32(float64(maxV)/layout.countsPerKelvin - kelvinZeroCelsius)
	st.minCelsius = float32(float64(minV)/layout.countsPerKelvin - kelvinZeroCelsius)
	return st, true
}

// thermalPixelCentre maps a row-major sensor index to the normalized centre of
// that pixel on the picture.
func thermalPixelCentre(i int) (x, y float32) {
	col, row := i%thermalSensorWidth, i/thermalSensorWidth
	return float32((float64(col) + 0.5) / thermalSensorWidth),
		float32((float64(row) + 0.5) / thermalSensorHeight)
}

// thermalSnapshot is a hub's latest reading, as it goes on the wire, plus when
// it arrived. The proto is embedded so one allocation per frame covers both,
// and it is never modified once published: every subscriber's Send marshals the
// same message, exactly as they already share a frame's RawFormat.
type thermalSnapshot struct {
	stats agentpb.ThermalStats
	// arrived carries the monotonic clock reading, so freshness is immune to a
	// wall-clock step; stats.TimestampNs is the wall-clock time clients see.
	arrived time.Time
}

func newThermalSnapshot(st thermalStats, tsNs uint64, arrived time.Time) *thermalSnapshot {
	return &thermalSnapshot{
		stats: agentpb.ThermalStats{
			MaxX:        st.maxX,
			MaxY:        st.maxY,
			MaxCelsius:  st.maxCelsius,
			MinX:        st.minX,
			MinY:        st.minY,
			MinCelsius:  st.minCelsius,
			TimestampNs: tsNs,
		},
		arrived: arrived,
	}
}

// freshAt returns the stats to attach to a frame sent at now, or nil when the
// reading is too old to describe what the viewer is looking at.
func (s *thermalSnapshot) freshAt(now time.Time) *agentpb.ThermalStats {
	if s == nil || now.Sub(s.arrived) > thermalStatsMaxAge {
		return nil
	}
	return &s.stats
}

// publishThermal records the latest reading from this hub's raw tap. nil clears
// it: the frame failed the plausibility guard, or the tap closed.
func (h *deviceHub) publishThermal(snap *thermalSnapshot) {
	h.thermal.Store(snap)
}

// currentThermal returns the stats to attach to a frame being sent now: the
// latest reading if it is fresh, else nil. Always nil for a camera with no
// radiometric block, whose raw tap never publishes one.
func (h *deviceHub) currentThermal() *agentpb.ThermalStats {
	snap := h.thermal.Load()
	if snap == nil {
		return nil
	}
	return snap.freshAt(time.Now())
}
