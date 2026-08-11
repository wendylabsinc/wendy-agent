// Package timefmt renders durations for operators. Clock skew on an RTC-less
// device is measured in years, which time.Duration.String renders as a six-digit
// hour count.
package timefmt

import (
	"fmt"
	"time"
)

// Skew renders d at the coarsest unit that still describes it: "56y", "146d",
// "3h", "20m".
func Skew(d time.Duration) string {
	if d < 0 {
		return "-" + Skew(-d)
	}
	switch {
	case d >= 365*24*time.Hour:
		return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return d.Round(time.Second).String()
	}
}
