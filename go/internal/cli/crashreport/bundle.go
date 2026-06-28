package crashreport

import (
	"regexp"

	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
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

// Request converts the bundle to the wire request.
func (b Bundle) Request() *cloudpb.SubmitReportRequest {
	return &cloudpb.SubmitReportRequest{
		PlatformInfo:    b.Info.Proto(),
		ErrorClass:      b.ErrorClass,
		Severity:        b.Severity,
		ErrorChain:      b.ErrorChain,
		LogTail:         b.LogTail,
		BuildOutputTail: b.BuildOutputTail,
		Contact:         b.Contact,
		RedactedFields:  map[string]string{},
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
