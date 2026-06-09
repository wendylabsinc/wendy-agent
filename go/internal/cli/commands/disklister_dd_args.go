package commands

import (
	"fmt"
	"regexp"
)

var validDarwinRawDiskPath = regexp.MustCompile(`^/dev/rdisk[0-9]+$`)

func darwinDDArgs(rawPath, bs string) ([]string, error) {
	if !validDarwinRawDiskPath.MatchString(rawPath) {
		return nil, fmt.Errorf("invalid Darwin raw disk path %q", rawPath)
	}
	switch bs {
	case "8m", "64m":
	default:
		return nil, fmt.Errorf("invalid Darwin dd block size %q", bs)
	}

	// Use rdisk for faster raw writes on macOS. The CLI streams image readers
	// such as ZIP entries through stdin; if BSD dd sees short pipe reads,
	// conv=sync pads each short read to bs and can write/corrupt far more than
	// the image size. iflag=fullblock keeps reading until a full block or EOF.
	return []string{
		"dd",
		"of=" + rawPath,
		"bs=" + bs,
		"iflag=fullblock",
		"status=progress",
	}, nil
}
