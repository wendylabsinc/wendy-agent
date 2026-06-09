//go:build linux

package services

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

const (
	// dmesgMaxMsgsPerSec caps non-critical messages forwarded per second.
	// KERN_EMERG/ALERT/CRIT messages are always forwarded regardless of this limit.
	dmesgMaxMsgsPerSec = 500

	// dmesgMaxMessageLen caps the byte length of a single kernel message body.
	dmesgMaxMessageLen = 4096

	// minValidTimestampNs rejects computed timestamps earlier than year 2000.
	minValidTimestampNs = 946684800 * int64(time.Second)

	// maxFutureSkewNs rejects timestamps more than 24 h in the future.
	maxFutureSkewNs = int64(24 * time.Hour)

	// maxReasonableTsUS rejects kernel timestamps beyond 100 years of uptime,
	// guarding against integer overflow in the tsUS*1000 multiplication.
	maxReasonableTsUS = int64(100 * 365 * 24 * 3600 * 1e6) // 100 years in µs

	// dmesgPIIAllowFile must exist on-disk to enable WENDY_DMESG_REDACT=false.
	// Requires filesystem write access separate from env-var permission domain.
	dmesgPIIAllowFile = "/etc/wendy/dmesg-pii-allowed"

	// dmesgPIIRecheckInterval controls how often the DPIA confirmation file and PII
	// allow-file are re-validated. Set to 5 s to bound the TOCTOU window: if an
	// operator revokes consent (removes the DPIA file) or removes the PII allow-file,
	// collection / unredacted forwarding stops within this interval.
	// 60 s was too long a grace period for a consent-revocation control.
	dmesgPIIRecheckInterval = 5 * time.Second
)

// piiPatterns matches host-identifying values in kernel messages for redaction.
// Covers: MAC (colon/hyphen), IPv4, USB serial numbers, OOM process names+PIDs,
// filesystem home paths, kernel comm= annotations, Bluetooth bdaddr values,
// network interface names, and block device node paths.
var piiPatterns = regexp.MustCompile(
	`(?i)(?:` +
		// MAC address (colon separated, exactly 6 octets)
		`(?:[0-9a-f]{2}:){5}[0-9a-f]{2}` +
		// MAC address (hyphen separated, exactly 6 octets)
		`|(?:[0-9a-f]{2}-){5}[0-9a-f]{2}` +
		// IPv4 address
		`|\b(?:\d{1,3}\.){3}\d{1,3}\b` +
		// USB serial number variants (e.g. "SerialNumber: ABC123DEF", "ID_SERIAL=XYZ")
		`|SerialNumber:\s+\S+` +
		`|ID_SERIAL(?:_SHORT)?=\S+` +
		// OOM killer: process name+PID (e.g. "Kill process 1234 (nginx)")
		`|Kill(?:ed)?\s+process\s+\d+\s+\([^)]+\)` +
		// Filesystem paths containing usernames
		`|/(?:home|Users|root)/[^\s/]+` +
		// Kernel process name annotations (e.g. "comm=nginx")
		`|comm=\S+` +
		// Audit-log argument values — both quoted (a0="bash") and unquoted (a0=bash)
		`|a\d+=(?:"[^"]*"|\S+)` +
		// OOM/audit argv arrays (e.g. argv[0]=/usr/bin/nginx)
		`|argv\[\d+\]=[^\s,]+` +
		// Kernel audit UID/GID/PID fields (e.g. uid=1000 auid=1000 gid=100)
		// These are directly identifying in multi-user systems (GDPR Art.5).
		`|(?:a?uid|a?gid|pid)=\d+` +
		// WiFi SSID names (e.g. "SSID: MyNetwork" — can be personally identifying)
		`|SSID:\s*(?:"[^"]*"|\S+)` +
		// Bluetooth device address (e.g. "bdaddr 00:11:22:33:44:55")
		`|bdaddr\s+(?:[0-9a-f]{2}:){5}[0-9a-f]{2}` +
		// Network interface names (e.g. "eth0", "wlan0", "enp3s0", "docker0").
		// These are hardware-identifying and appear in kernel network messages.
		`|\b(?:eth|wlan|ens|enp|wlp|docker|veth|br-|virbr)\w+\b` +
		// Block device node paths (e.g. "/dev/sda", "/dev/nvme0n1", "/dev/mmcblk0").
		// These are hardware-identifying and appear in storage/filesystem messages.
		`|/dev/(?:sd[a-z]\w*|nvme\w+|mmcblk\w+|vd[a-z]\w*)` +
		// Kernel audit exe field (e.g. "exe=/usr/sbin/sshd")
		`|exe=\S+` +
		// Kernel audit key field (e.g. "key=privileged-access")
		`|key=\S+` +
		// Kernel audit COMM field in quoted uppercase form (e.g. COMM="bash")
		`|COMM="[^"]*"` +
		// USB product/manufacturer strings (e.g. "usb 1-1: Product: Alice's iPhone")
		`|(?:Product|Manufacturer):\s+[^\n]+` +
		// Kernel audit path/context fields (e.g. name="/home/alice/.ssh", cwd="/root",
		// subj=system_u:system_r:kernel_t:s0, proctitle=<hex-or-text>, tty=pts0)
		`|(?:subj|name|cwd|path|proctitle|tty)=(?:"[^"]*"|\S+)` +
		// Kernel audit session/message header (e.g. ses=42, msg=audit(1234567890.123:456))
		`|ses=\d+` +
		`|msg=audit\([^)]+\)` +
		`)`,
)

// piiIPv6Pattern matches IPv6 addresses in both full and compressed notation.
// Full form requires ≥3 colon-terminated hex groups to avoid false-positive
// matches on 2-group sequences like "dead:beef" that are not IP addresses.
// Compressed form: ::1, fe80::1, 2001:db8:: (zero or more groups + ::).
// Kept separate from piiPatterns and gated behind strings.ContainsRune(':').
var piiIPv6Pattern = regexp.MustCompile(
	`(?i)` +
		// Full-form: at least 3 hex groups followed by colon, then trailing group
		`(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{0,4}` +
		// Compressed form with :: (covers ::1, fe80::1, 2001:db8::, etc.)
		`|(?:[0-9a-f]{1,4}:)*::(?:[0-9a-f]{1,4}:)*[0-9a-f]{0,4}`,
)

// piiKernelPtrPattern matches kernel pointer addresses (e.g. "0xffffffff81234567").
// Kept separate and gated behind strings.Contains("0x") to avoid scanning messages
// that cannot contain a pointer, reducing per-message cost at high message rates.
var piiKernelPtrPattern = regexp.MustCompile(`(?i)0x[0-9a-f]{8,16}`)

// csiRemnantPattern strips orphaned ANSI/VT escape remnants left after ESC
// (U+001B, removed by strings.Map's r < 0x20 check) is stripped:
//   - Standard CSI: "[" + decimal/semicolon params + letter (e.g. "[31m")
//   - Private/intermediate CSI: "[" + "?", "!", "<", ">", "=" + params + letter
//     (e.g. "[?25l" cursor visibility, "[>c" device attributes)
//   - OSC remnants: "]" + numeric param + ";" + text, terminator already removed
//     by the control-char strip above (BEL=0x07, ST=0x9c are both <0x20 or C1)
var csiRemnantPattern = regexp.MustCompile(`\[[0-9;?!<>=]*[A-Za-z]|\]\d+;[^\x00-\x1f]*`)

// CollectDmesgLogs reads kernel messages from /dev/kmsg and publishes them as
// OTel log records at debug/trace severity. It replays buffered kernel messages
// first, then follows new ones. Blocks until ctx is cancelled.
//
// Requires /etc/wendy/dmesg-dpia-confirmed with non-empty content (DPIA record).
// PII redaction is enabled by default. To disable, set WENDY_DMESG_REDACT=false
// AND create /etc/wendy/dmesg-pii-allowed on the host filesystem.
//
// Severity mapping: KERN_DEBUG/INFO/NOTICE/WARNING/ERR map to OTel TRACE–DEBUG3
// (below INFO) so routine dmesg output does not pollute default INFO+ views.
// KERN_CRIT→WARN, KERN_ALERT→ERROR, KERN_EMERG→FATAL so critical kernel events
// remain visible in SIEM/alerting pipelines (SOC2-CC7, NIST-AU-2). These levels
// additionally emit a zap.Warn to the agent's own log stream. See kernelLevelToOTEL.
func CollectDmesgLogs(ctx context.Context, logger *zap.Logger, broadcaster *TelemetryBroadcaster) {
	// Require a file-based DPIA confirmation. Unlike an env var, a file:
	//   - Cannot be satisfied by copying an env block in a container spec
	//   - Produces a filesystem access log entry (audit trail)
	// Non-empty content is required so an empty placeholder file doesn't pass.
	dpiaContent, readErr := os.ReadFile(DmesgDPIAConfirmFile)
	if readErr != nil || len(bytes.TrimSpace(dpiaContent)) == 0 {
		logger.Error("kernel dmesg collection requires "+DmesgDPIAConfirmFile+" with non-empty content",
			zap.String("reason", "GDPR Art.35 requires a documented DPIA before forwarding kernel messages to an external backend"),
		)
		return
	}
	// Zero the content before releasing; dpiaContent may contain PII (operator
	// names, DPO contacts, ticket IDs). Avoid string() conversion which would
	// create an immutable copy that cannot be zeroed.
	for i := range dpiaContent {
		dpiaContent[i] = 0
	}
	dpiaContent = nil
	logger.Info("dmesg DPIA confirmation found",
		zap.String("file", DmesgDPIAConfirmFile),
		zap.Bool("confirmation_present", true),
	)

	// redactAtomic: 1 = redact enabled (safe default), 0 = redact disabled.
	// Using an atomic int32 allows the periodic re-check goroutine below to
	// re-enable redaction if the allow-file disappears after startup, so a
	// momentary file creation cannot permanently disable redaction.
	//
	// Disabling requires BOTH WENDY_DMESG_REDACT=false (env-var domain) AND
	// the existence of dmesgPIIAllowFile (filesystem domain). Separate permission
	// domains: an actor who can only set env vars cannot bypass redaction.
	var redactAtomic int32 = 1
	if v, err := strconv.ParseBool(os.Getenv("WENDY_DMESG_REDACT")); err == nil && !v {
		if _, statErr := os.Stat(dmesgPIIAllowFile); statErr == nil {
			atomic.StoreInt32(&redactAtomic, 0)
		} else {
			logger.Warn("WENDY_DMESG_REDACT=false requires "+dmesgPIIAllowFile+" to exist; keeping redaction enabled",
				zap.String("reason", "file-based out-of-band confirmation required; env-var alone is insufficient"),
			)
		}
	}

	// Capture hostname once at startup for both redact paths:
	// - redact=true: used for per-message literal hostname substitution.
	// - redact=false: included in the OTel resource as service.instance.id.
	// Note: only the exact os.Hostname() string is redacted; FQDN variants,
	// mDNS (.local) names, and hostname aliases are not covered and are
	// listed in redact_not_covered at startup. The hostname is a process
	// constant already present in kernel memory — caching it here adds no
	// additional retention risk vs. fetching it per-message.
	hostname, _ := os.Hostname()

	f, err := os.Open("/dev/kmsg")
	if err != nil {
		logger.Warn("dmesg collection unavailable", zap.Error(err))
		return
	}

	// Verify /dev/kmsg is actually a character device to guard against a
	// container bind-mount replacing it with a regular file or FIFO.
	info, statErr := f.Stat()
	if statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
		logger.Warn("dmesg: /dev/kmsg is not a character device; skipping collection",
			zap.String("mode", func() string {
				if statErr != nil {
					return statErr.Error()
				}
				return info.Mode().String()
			}()))
		_ = f.Close()
		return
	}

	// Verify device major/minor numbers match /dev/kmsg (major=1, minor=11) to
	// prevent a bind-mount substituting another char device (e.g. /dev/urandom).
	// Fail closed: if Fstat itself fails, skip collection rather than proceed
	// without device-number verification (bind-mount hardening must not be skipped).
	var kst unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &kst); err != nil {
		logger.Warn("dmesg: cannot verify /dev/kmsg device numbers; skipping collection",
			zap.Error(err),
		)
		_ = f.Close()
		return
	} else if maj, min := unix.Major(kst.Rdev), unix.Minor(kst.Rdev); maj != 1 || min != 11 {
		logger.Warn("dmesg: /dev/kmsg has unexpected device numbers; skipping",
			zap.Uint32("major", maj),
			zap.Uint32("minor", min),
		)
		_ = f.Close()
		return
	}

	if atomic.LoadInt32(&redactAtomic) != 0 {
		logger.Warn("kernel dmesg collection started with partial PII redaction",
			zap.String("source", "/dev/kmsg"),
			zap.String("redact", "partial"),
			zap.Strings("redact_covered", []string{
				"MAC-address", "IPv4-address", "IPv6-address-full-and-compressed",
				"USB-SerialNumber", "ID_SERIAL", "USB-Product-string", "USB-Manufacturer-string",
				"Bluetooth-bdaddr", "OOM-process-name+PID", "filesystem-home-paths",
				"comm=", "COMM=quoted", "process-argv-quoted-and-unquoted",
				"kernel-audit-uid-gid-pid", "audit-exe", "audit-key", "wifi-ssid",
				"kernel-pointer-addresses", "network-interface-names",
				"block-device-paths", "hostname-exact", "hostname-FQDN-prefix",
				"audit-subj=", "audit-name=", "audit-cwd=", "audit-path=",
				"audit-proctitle=", "audit-tty=", "audit-ses=", "audit-msg-header",
			}),
			zap.Strings("redact_not_covered", []string{
				"NFS-paths", "unlabelled-kernel-fields",
				"hostname-mDNS-aliases", "oom-cmdline", "custom-kernel-module-output",
			}),
			zap.String("dpia_file", DmesgDPIAConfirmFile),
		)
	} else {
		logger.Warn("kernel dmesg collection started with PII redaction DISABLED",
			zap.String("source", "/dev/kmsg"),
			zap.Bool("redact", false),
			zap.String("compliance_note", "raw kernel messages forwarded; GDPR/compliance obligations are operator responsibility"),
		)
	}
	defer logger.Info("kernel dmesg collection stopped")

	// sync.Once ensures only one close fires even though both the ctx-cancel
	// goroutine and the defer call closeFile().
	var closeOnce sync.Once
	closeFile := func() { closeOnce.Do(func() { _ = f.Close() }) }
	go func() {
		<-ctx.Done()
		closeFile()
	}()
	defer closeFile()

	// Periodically re-validate the PII allow-file. If it disappears after startup,
	// re-enable redaction so that a momentary file creation does not permanently
	// disable redaction for the process lifetime (TOCTOU mitigation).
	if atomic.LoadInt32(&redactAtomic) == 0 {
		go func() {
			ticker := time.NewTicker(dmesgPIIRecheckInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, statErr := os.Stat(dmesgPIIAllowFile); statErr != nil {
						atomic.StoreInt32(&redactAtomic, 1)
						logger.Warn("dmesg: PII allow-file removed; redaction re-enabled",
							zap.String("file", dmesgPIIAllowFile),
						)
					}
				}
			}
		}()
	}

	// Periodically re-validate the DPIA confirmation file. If it is removed
	// after startup, stop collection so a revoked DPIA takes effect within one
	// recheck interval (TOCTOU mitigation).
	go func() {
		ticker := time.NewTicker(dmesgPIIRecheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, statErr := os.Stat(DmesgDPIAConfirmFile); statErr != nil {
					logger.Warn("dmesg: DPIA confirmation file removed; stopping kernel log collection",
						zap.String("file", DmesgDPIAConfirmFile),
					)
					closeFile()
					return
				}
			}
		}
	}()

	// Pre-compute both resource variants; pick per-publish based on current atomic
	// redactAtomic so the resource attributes always reflect the effective state
	// even if redaction is re-enabled at runtime (PII allow-file removed).
	redactOnResource := dmesgResource(true, hostname)
	redactOffResource := dmesgResource(false, hostname)
	bootEpoch := kmsgBootEpochNanoseconds()

	// Sliding-window rate limiter for non-critical messages only.
	// KERN_EMERG (0), KERN_ALERT (1), KERN_CRIT (2) bypass this entirely.
	// All three window variables are accessed exclusively from the scanner
	// goroutine below — there is no concurrent access and no mutex is needed.
	var (
		windowStart = time.Now()
		windowCount int
		windowDrop  int
	)
	rateAllow := func() bool {
		now := time.Now()
		if now.Sub(windowStart) >= time.Second {
			if windowDrop > 0 {
				logger.Warn("dmesg rate limit: messages dropped in last second",
					zap.Int("dropped", windowDrop),
					zap.Int("forwarded", windowCount),
				)
			}
			windowStart = now
			windowCount = 0
			windowDrop = 0
		}
		if windowCount >= dmesgMaxMsgsPerSec {
			windowDrop++
			return false
		}
		windowCount++
		return true
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 8192), 256*1024)
	for {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				if errors.Is(err, bufio.ErrTooLong) {
					// A single oversized kmsg line must not terminate collection.
					// Recreating the scanner on the same fd is safe: /dev/kmsg
					// advances the read position per-record at the kernel level,
					// so the next Read() starts at the following message.
					logger.Warn("dmesg: oversized kernel message dropped; restarting scanner")
					scanner = bufio.NewScanner(f)
					scanner.Buffer(make([]byte, 0, 8192), 256*1024)
					continue
				}
				if !errors.Is(err, os.ErrClosed) {
					logger.Warn("dmesg scanner exited with error", zap.Error(err))
				}
			}
			break
		}
		line := scanner.Text()
		if len(line) == 0 || line[0] == ' ' {
			continue
		}

		level, message, tsUS, ok := parseKmsgLine(line)
		if !ok {
			continue
		}
		if len(message) > dmesgMaxMessageLen {
			message = message[:dmesgMaxMessageLen]
		}
		if atomic.LoadInt32(&redactAtomic) != 0 {
			// Save original for gate checks — gate conditions must use the pre-redaction
			// text so that "<redacted>" tokens from earlier passes don't create false
			// positives (e.g. "<redacted>" does not contain ":" or "0x").
			originalMessage := message
			// Run IPv6 and pointer patterns FIRST on the original message to prevent
			// piiPatterns from partially consuming an IPv6 address (e.g. MAC sub-pattern
			// matching a 2-hex-digit fragment of an IPv6 group and leaving a remainder
			// that piiIPv6Pattern then fails to match). By redacting IPv6/pointers before
			// piiPatterns, all downstream pattern passes operate on already-<redacted>
			// tokens that cannot interfere with the remaining PII detection.
			if strings.ContainsRune(originalMessage, ':') {
				message = piiIPv6Pattern.ReplaceAllString(message, "<redacted>")
			}
			if strings.Contains(originalMessage, "0x") {
				message = piiKernelPtrPattern.ReplaceAllString(message, "<redacted>")
			}
			// piiPatterns runs last; IPv6 and pointer values are already replaced
			// so no cross-pattern fragmentation can occur.
			message = piiPatterns.ReplaceAllString(message, "<redacted>")
			// Hostname: redact FQDN prefix (e.g. hostname.corp.example.com) before
			// the bare hostname, so the full FQDN is caught rather than leaving the
			// domain suffix visible.
			if hostname != "" {
				message = strings.ReplaceAll(message, hostname+".", "<redacted>.")
				message = strings.ReplaceAll(message, hostname, "<redacted>")
			}
		}

		isCritical := level <= 2 // KERN_EMERG, KERN_ALERT, KERN_CRIT

		// Critical messages bypass the rate limiter so they are never silently
		// dropped. The zap.Warn fires after the rate check so the agent log
		// stays visible at the default INFO+ level.
		if !isCritical && !rateAllow() {
			continue
		}
		if isCritical {
			// Emit kernel_level to the Zap logger (agent's own log stream) so
			// critical events are visible at INFO+ without exposing the message
			// body to Zap's sink (which may have different retention/access
			// policies than the OTel backend). The full message body — already
			// redacted if redactAtomic==1 — is forwarded exclusively via OTel
			// below, which is the authorised telemetry channel.
			logger.Warn("critical kernel message received",
				zap.Int("kernel_level", level),
			)
		}

		timeNano := kmsgTimestampToWall(tsUS, bootEpoch)
		severity, severityText := kernelLevelToOTEL(level)
		resource := redactOnResource
		if atomic.LoadInt32(&redactAtomic) == 0 {
			resource = redactOffResource
		}
		broadcaster.PublishLogs(&otelpb.ExportLogsServiceRequest{
			ResourceLogs: []*otelpb.ResourceLogs{
				{
					Resource: resource,
					ScopeLogs: []*otelpb.ScopeLogs{
						{
							Scope: &otelpb.InstrumentationScope{Name: "wendy.dmesg"},
							LogRecords: []*otelpb.LogRecord{
								{
									TimeUnixNano:         timeNano,
									ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
									SeverityNumber:       severity,
									SeverityText:         severityText,
									Body: &otelpb.AnyValue{
										Value: &otelpb.AnyValue_StringValue{StringValue: message},
									},
								},
							},
						},
					},
				},
			},
		})
	}

	// Flush any drops accumulated in the current window that were never reported
	// because no new message arrived to trigger the window-rollover log line.
	if windowDrop > 0 {
		logger.Warn("dmesg rate limit: messages dropped at shutdown",
			zap.Int("dropped", windowDrop),
			zap.Int("forwarded", windowCount),
		)
	}
}

// dmesgResource returns the OTel resource for kernel log records.
// service.instance.id (hostname) is gated behind redact=false so the device
// hostname is not forwarded when PII redaction is enabled. The wendy.dmesg.redact
// attribute records the effective redaction state for downstream monitoring.
// "partial" indicates redaction is active but has documented gaps (see startup log);
// "false" means no redaction at all (requires dual-domain consent).
func dmesgResource(redact bool, hostname string) *otelpb.Resource {
	redactStr := "partial"
	if !redact {
		redactStr = "false"
	}
	attrs := []*otelpb.KeyValue{
		stringKV("service.name", "kernel"),
		stringKV("service.namespace", "wendy"),
		stringKV("wendy.dmesg.redact", redactStr),
	}
	if !redact && hostname != "" {
		attrs = append(attrs, stringKV("service.instance.id", hostname))
	}
	return &otelpb.Resource{Attributes: attrs}
}

// kmsgBootEpochNanoseconds returns the wall-clock Unix nanosecond timestamp
// corresponding to the kernel boot instant, computed from CLOCK_BOOTTIME.
// Returns 0 if unavailable.
func kmsgBootEpochNanoseconds() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0
	}
	bootNowNs := ts.Sec*int64(time.Second) + ts.Nsec
	return time.Now().UnixNano() - bootNowNs
}

// kmsgTimestampToWall converts a kernel timestamp (microseconds since boot) to
// a wall-clock Unix nanosecond value. Falls back to time.Now() if outside a
// plausible range to guard against NTP steps or boot-epoch skew.
func kmsgTimestampToWall(tsUS int64, bootEpoch int64) uint64 {
	now := time.Now().UnixNano()
	// maxReasonableTsUS guard prevents integer overflow in tsUS*1000 for
	// malformed or attacker-supplied timestamps (100-year uptime upper bound).
	// tsUS >= 0 (not > 0): the very first kernel message has timestamp 0 (boot
	// instant) and should map to the boot epoch, not fall back to time.Now().
	// parseKmsgLine already rejects parse failures before tsUS reaches here.
	if bootEpoch > 0 && tsUS >= 0 && tsUS <= maxReasonableTsUS {
		computed := bootEpoch + tsUS*1000
		if computed >= minValidTimestampNs && computed <= now+maxFutureSkewNs {
			return uint64(computed)
		}
	}
	return uint64(now)
}

// parseKmsgLine parses a /dev/kmsg record of the form:
//
//	priority,sequence,timestamp_us,-;message
//
// Returns the syslog level (0–7), sanitised message text, timestamp in
// microseconds since boot, and whether parsing succeeded. ASCII and Unicode
// control characters (except tab) are stripped to prevent log injection.
func parseKmsgLine(line string) (level int, message string, timestampUS int64, ok bool) {
	semi := strings.IndexByte(line, ';')
	if semi < 0 {
		return 0, "", 0, false
	}

	// Strip ASCII control chars and Unicode format/control characters.
	message = strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		// Drop ASCII control chars (C0/C1) and selected Unicode characters that
		// could be used for log injection or terminal escape sequences:
		//   0x200B zero-width space, 0x200E/0x200F directional marks, 0xFEFF BOM
		//   0x2028–0x2029 line/paragraph separators
		//   0x202A–0x202E bidirectional override characters (LRE/RLE/PDF/LRO/RLO)
		//   0x2066–0x2069 bidirectional isolation characters (LRI/RLI/FSI/PDI)
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) ||
			r == 0x200b || r == 0x200e || r == 0x200f || r == 0xfeff ||
			(r >= 0x2028 && r <= 0x202e) ||
			(r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, line[semi+1:])
	// Strip orphaned CSI remnants (e.g. "[31m") left after ESC is removed above.
	message = csiRemnantPattern.ReplaceAllString(message, "")

	parts := strings.SplitN(line[:semi], ",", 4)
	if len(parts) < 3 {
		return 0, "", 0, false
	}

	priority, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", 0, false
	}
	// The kmsg priority byte is facility|level (8 bits). Reject values outside
	// this range — a negative or oversized value indicates a crafted/malformed
	// record and could silently coerce to an unexpected severity via & 7.
	if priority < 0 || priority > 0xFF {
		return 0, "", 0, false
	}

	ts, err := strconv.ParseInt(parts[2], 10, 64)
	// Reject parse failures and negative timestamps. A failed parse leaves ts=0,
	// which is a valid boot-epoch timestamp, so we must distinguish the two cases
	// explicitly rather than relying on kmsgTimestampToWall's range guard.
	if err != nil || ts < 0 {
		return 0, "", 0, false
	}
	return priority & 7, message, ts, true
}

// kernelLevelToOTEL maps a kernel syslog level (0–7) to an OTel severity.
// Non-critical levels are mapped into the trace/debug band so they do not
// pollute default INFO+ views. Critical levels (KERN_CRIT and above) are
// mapped to their natural OTel equivalents so they remain visible in SIEM
// alerting pipelines (SOC2-CC7, ISO27001-A.12, NIST-AU-2).
func kernelLevelToOTEL(level int) (otelpb.SeverityNumber, string) {
	switch level {
	case 7: // KERN_DEBUG
		return otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE, "TRACE"
	case 6: // KERN_INFO
		return otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE4, "TRACE4"
	case 5: // KERN_NOTICE
		return otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG"
	case 4: // KERN_WARNING
		return otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG2, "DEBUG2"
	case 3: // KERN_ERR
		return otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG3, "DEBUG3"
	case 2: // KERN_CRIT
		return otelpb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"
	case 1: // KERN_ALERT
		return otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"
	default: // KERN_EMERG (0)
		return otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"
	}
}
