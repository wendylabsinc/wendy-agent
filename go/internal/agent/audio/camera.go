package audio

import (
	"context"
	"encoding/json"
)

// FindCameraSource returns the object.serial of the PipeWire node publishing devicePath.
// Serial, not node.name: a multi-interface UVC camera publishes one node per V4L2 device
// under a single name, so the name resolves to the wrong one.
func FindCameraSource(ctx context.Context, devicePath string) (uint64, bool) {
	out, err := DumpRun(ctx)
	if err != nil {
		return 0, false
	}
	return parseCameraSource(out, devicePath)
}

// parseCameraSource finds the Video/Source node for devicePath in a pw-dump.
func parseCameraSource(data []byte, devicePath string) (uint64, bool) {
	var objects []pwObject
	if err := json.Unmarshal(data, &objects); err != nil {
		return 0, false
	}
	for _, o := range objects {
		// Device and Port objects carry media.class and api.v4l2.path too, but
		// only a Node can be streamed.
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		if propString(o.Info.Props, "media.class") != "Video/Source" {
			continue
		}
		if propString(o.Info.Props, "api.v4l2.path") != devicePath {
			continue
		}
		if serial := propUint(o.Info.Props, "object.serial"); serial != 0 {
			return serial, true
		}
	}
	return 0, false
}
