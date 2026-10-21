package liteclient

import (
	"strings"

	"github.com/google/uuid"
)

// Enrollment UUIDs are used verbatim. Older firmware advertises its hardware
// ID, whose issued principal has the lite- prefix. Never normalize UUID text:
// a noncanonical spelling must not silently select another pinned identity.
func enrollmentDeviceID(id string) string {
	if value, err := uuid.Parse(id); err == nil && value.String() == id {
		return id
	}
	return "lite-" + strings.TrimPrefix(id, "lite-")
}
