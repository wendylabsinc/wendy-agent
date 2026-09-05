package appconfig

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	ReadinessMaxTimeoutSeconds      = 3600
	ReadinessMaxPeriodSeconds       = 300
	ReadinessMaxProbeTimeoutSeconds = 300
)

// HasProbe reports whether a probe was explicitly configured. An explicitly
// empty exec list is still configured, so validation rejects it instead of
// accidentally replacing it with an HTTP-entitlement probe.
func (r *ReadinessConfig) HasProbe() bool {
	return r != nil && (r.TCPSocket != nil || r.HTTPGet != nil || r.Exec != nil)
}

// Timeout is the total readiness deadline. Callers validate the config first.
func (r *ReadinessConfig) Timeout() time.Duration {
	if r == nil || r.TimeoutSeconds == 0 {
		return 30 * time.Second
	}
	return time.Duration(r.TimeoutSeconds) * time.Second
}

// Period is the delay between a failed probe and the next attempt.
func (r *ReadinessConfig) Period() time.Duration {
	if r == nil || r.PeriodSeconds == 0 {
		return time.Second
	}
	return time.Duration(r.PeriodSeconds) * time.Second
}

// ProbeTimeout is the maximum duration of one probe. The runner must also
// enforce the remaining total readiness deadline, whichever is shorter.
func (r *ReadinessConfig) ProbeTimeout() time.Duration {
	if r == nil || r.ProbeTimeoutSeconds == 0 {
		return 2 * time.Second
	}
	return time.Duration(r.ProbeTimeoutSeconds) * time.Second
}

// RequestPath returns the endpoint path, applying the default root path.
func (p *HTTPGetProbe) RequestPath() string {
	if p == nil || p.Path == "" {
		return "/"
	}
	return p.Path
}

// EffectiveReadiness returns an explicit probe, or synthesizes the existing TCP
// readiness behavior of an HTTP entitlement. Merely declaring an HTTP port does
// not promise that GET / succeeds. An app with neither has no readiness probe.
// The returned config must be treated as read-only by callers.
func EffectiveReadiness(cfg *AppConfig) *ReadinessConfig {
	if cfg == nil {
		return nil
	}
	if cfg.Readiness.HasProbe() {
		return cfg.Readiness
	}
	for _, e := range cfg.Entitlements {
		if e.Type != EntitlementHTTP {
			continue
		}
		r := ReadinessConfig{}
		if cfg.Readiness != nil {
			r = *cfg.Readiness
		}
		r.TCPSocket = &TCPSocketProbe{Port: e.Port}
		return &r
	}
	return nil
}

// ValidateReadiness checks probe exclusivity, endpoint/command validity, and
// bounded timing values. Zero timing values select defaults. A nil or timing-
// only config is valid for backward-compatible HTTP-entitlement synthesis.
func ValidateReadiness(prefix string, r *ReadinessConfig) error {
	if r == nil {
		return nil
	}
	probes := 0
	if r.TCPSocket != nil {
		probes++
	}
	if r.HTTPGet != nil {
		probes++
	}
	if r.Exec != nil {
		probes++
	}
	if probes > 1 {
		return fmt.Errorf("%s must configure at most one of tcpSocket, httpGet, or exec", prefix)
	}
	if r.TCPSocket != nil {
		if err := validateReadinessPort(prefix+".tcpSocket.port", r.TCPSocket.Port); err != nil {
			return err
		}
	}
	if r.HTTPGet != nil {
		if err := validateReadinessPort(prefix+".httpGet.port", r.HTTPGet.Port); err != nil {
			return err
		}
		p := r.HTTPGet.RequestPath()
		u, err := url.ParseRequestURI(p)
		if err != nil || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") ||
			strings.ContainsAny(p, "\\#") || strings.ContainsFunc(p, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) ||
			u == nil || u.IsAbs() || u.Host != "" || u.Fragment != "" {
			return fmt.Errorf("%s.httpGet.path must be a local HTTP request path beginning with /, without a host, fragment, whitespace, or control characters", prefix)
		}
	}
	if r.Exec != nil {
		if len(r.Exec) == 0 || strings.TrimSpace(r.Exec[0]) == "" {
			return fmt.Errorf("%s.exec must contain a non-empty command followed by optional arguments", prefix)
		}
		for i, arg := range r.Exec {
			if strings.ContainsRune(arg, '\x00') {
				return fmt.Errorf("%s.exec[%d] must not contain NUL", prefix, i)
			}
		}
	}
	for _, setting := range []struct {
		name  string
		value int
		max   int
	}{
		{"timeoutSeconds", r.TimeoutSeconds, ReadinessMaxTimeoutSeconds},
		{"periodSeconds", r.PeriodSeconds, ReadinessMaxPeriodSeconds},
		{"probeTimeoutSeconds", r.ProbeTimeoutSeconds, ReadinessMaxProbeTimeoutSeconds},
	} {
		if setting.value < 0 || setting.value > setting.max {
			return fmt.Errorf("%s.%s must be between 0 and %d (0 uses the default), got %d", prefix, setting.name, setting.max, setting.value)
		}
	}
	return nil
}

func validateReadinessPort(field string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s must be between 1 and 65535, got %d", field, port)
	}
	return nil
}
