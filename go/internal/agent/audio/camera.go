package audio

import "context"

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
//
// Matching api.v4l2.path assumes the v4l2 monitor publishes a node for every camera. It holds
// because WendyOS requires monitor.v4l2 and WirePlumber has no cross-monitor dedup, so the
// v4l2 node coexists with libcamera's; a version that deduped them would break this silently.
func parseCameraSource(data []byte, devicePath string) (uint64, bool) {
	objects, err := decodeDump(data)
	if err != nil {
		return 0, false
	}
	for _, o := range objects {
		props, _, ok := nodeProps(o, "Video/Source")
		if !ok || propString(props, "api.v4l2.path") != devicePath {
			continue
		}
		if serial := propUint(props, "object.serial"); serial != 0 {
			return serial, true
		}
	}
	return 0, false
}
