package episodeexport

import (
	"bytes"
	"fmt"
)

// Native capture may index each read(2) chunk, splitting a large H.264 access
// unit across several adjacent records. A continuation has no Annex-B prefix.
// Dropping it as a picture-less sample truncates the preceding coded slice.
// Join only contiguous bytes from the same segment and retain the timestamp
// of the access unit's first chunk. Never bridge a missing range/segment.
func joinIndexedH264Fragments(reader *segmentReader, frames []cameraIndexLine) ([]cameraIndexLine, int, error) {
	joined := make([]cameraIndexLine, 0, len(frames))
	fragments := 0
	for _, frame := range frames {
		payload, err := reader.read(frame)
		if err != nil {
			// Preserve the normal missing/truncated-record accounting in the muxer.
			joined = append(joined, frame)
			continue
		}
		hasPrefix := bytes.HasPrefix(payload, []byte{0, 0, 1}) || bytes.HasPrefix(payload, []byte{0, 0, 0, 1})
		if !hasPrefix && len(joined) > 0 {
			previous := &joined[len(joined)-1]
			if previous.Segment != frame.Segment || previous.ByteOffset+int64(previous.ByteSize) != frame.ByteOffset {
				return nil, fragments, fmt.Errorf("H.264 continuation is not contiguous with the preceding record")
			}
			if bytes.Contains(payload, []byte{0, 0, 1}) {
				return nil, fragments, fmt.Errorf("H.264 transport chunk contains an unindexed access-unit boundary; cannot assign reliable frame timestamps")
			}
			// The segment reader already checked this range against the file size.
			previous.ByteSize += frame.ByteSize
			fragments++
			continue
		}
		joined = append(joined, frame)
	}
	return joined, fragments, nil
}
