package crashreport

import (
	"regexp"

	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

const maxTail = 200

var reTrackingID = regexp.MustCompile(`^WDY-[A-Z0-9]{6}$`)

// Bundle is the fully-redacted, bounded diagnostic payload.
type Bundle struct {
	Info            platforminfo.Info
	ErrorClass      string
	Severity        string
	ErrorChain      string
	LogTail         []string
	BuildOutputTail []string
	Contact         string
}

// Build assembles a redacted, bounded bundle. All free-text inputs pass through
// the redactor; log and build tails keep only the last maxTail entries.
func Build(info platforminfo.Info, errorClass, severity, errorChain string, logTail, buildTail []string) Bundle {
	return Bundle{
		Info:            info,
		ErrorClass:      errorClass,
		Severity:        severity,
		ErrorChain:      Redact(errorChain),
		LogTail:         RedactLines(lastN(logTail, maxTail)),
		BuildOutputTail: RedactLines(lastN(buildTail, maxTail)),
	}
}

// submitPayload is the JSON body POSTed to the telemetry crashreports endpoint.
// It embeds the redacted bundle fields plus the anonymous routing key.
type submitPayload struct {
	AnonymousID     string            `json:"anonymous_id"`
	NotifyOnFix     bool              `json:"notify_on_fix"`
	PlatformInfo    platforminfo.Info `json:"platform_info"`
	ErrorClass      string            `json:"error_class,omitempty"`
	Severity        string            `json:"severity,omitempty"`
	ErrorChain      string            `json:"error_chain,omitempty"`
	LogTail         []string          `json:"log_tail,omitempty"`
	BuildOutputTail []string          `json:"build_output_tail,omitempty"`
	Contact         string            `json:"contact,omitempty"`
}

// Payload builds the JSON submit body from the (already redacted) bundle.
func (b Bundle) Payload(anonymousID string, notifyOnFix bool) submitPayload {
	return submitPayload{
		AnonymousID:     anonymousID,
		NotifyOnFix:     notifyOnFix,
		PlatformInfo:    b.Info,
		ErrorClass:      b.ErrorClass,
		Severity:        b.Severity,
		ErrorChain:      b.ErrorChain,
		LogTail:         b.LogTail,
		BuildOutputTail: b.BuildOutputTail,
		Contact:         b.Contact,
	}
}

// ValidTrackingID reports whether id matches the WDY-XXXXXX format.
func ValidTrackingID(id string) bool { return reTrackingID.MatchString(id) }

func lastN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
