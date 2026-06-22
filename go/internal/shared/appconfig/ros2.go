package appconfig

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// ROS2AnnotationKey is the containerd label / OCI annotation under which a
// container's resolved ROS 2 configuration is published, e.g.
// "distro=humble,domain_id=42". The agent uses it to discover ROS 2
// containers and configure the CLI sidecar (WDY-884, WDY-1332).
const ROS2AnnotationKey = EntitlementAnnotationKeyPrefix + "ros2"

// ROS2DefaultDistro is the ROS 2 distribution assumed when wendy.json does
// not specify one.
const ROS2DefaultDistro = "humble"

// ROS2DefaultRMW is the RMW implementation injected when wendy.json does not
// specify one.
const ROS2DefaultRMW = "rmw_cyclonedds_cpp"

// ROS2DomainIDMin and ROS2DomainIDMax bound valid ROS_DOMAIN_ID values to the
// full ROS 2 range (0–232). RMW implementations map the domain ID to UDP ports;
// 232 is the maximum the DDS port-mapping scheme supports
// (SOC2-CC6, NIST-SI-10).
const (
	ROS2DomainIDMin = 0
	ROS2DomainIDMax = 232
)

// ros2RMWAliases maps wendy.json rmw values to full RMW implementation
// identifiers. Both the short and full forms are accepted; validating against
// a fixed set prevents injection of arbitrary strings into the container
// environment (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
var ros2RMWAliases = map[string]string{
	"cyclonedds":         "rmw_cyclonedds_cpp",
	"fastrtps":           "rmw_fastrtps_cpp",
	"fastdds":            "rmw_fastrtps_cpp",
	"connextdds":         "rmw_connextdds",
	"gurumdds":           "rmw_gurumdds_cpp",
	"rmw_cyclonedds_cpp": "rmw_cyclonedds_cpp",
	"rmw_fastrtps_cpp":   "rmw_fastrtps_cpp",
	"rmw_connextdds":     "rmw_connextdds",
	"rmw_gurumdds_cpp":   "rmw_gurumdds_cpp",
}

// validateROS2Config rejects an out-of-range domainId or an unknown rmw so a
// typo fails fast at config-parse / `wendy run` time instead of silently
// launching a container with no ROS_DOMAIN_ID/RMW_IMPLEMENTATION isolation
// (WDY-1701). prefix labels the source, e.g. "frameworks.ros2" or
// `services["talker"].frameworks.ros2`.
func validateROS2Config(prefix string, r *ROS2Config) error {
	if r == nil {
		return nil
	}
	if r.DomainID != nil && (*r.DomainID < ROS2DomainIDMin || *r.DomainID > ROS2DomainIDMax) {
		return fmt.Errorf("%s.domainId must be between %d and %d, got %d", prefix, ROS2DomainIDMin, ROS2DomainIDMax, *r.DomainID)
	}
	if r.RMW != "" && ros2RMWAliases[strings.ToLower(r.RMW)] == "" {
		return fmt.Errorf("%s.rmw %q is not a supported RMW implementation", prefix, r.RMW)
	}
	return nil
}

// IsValidRMWImplementation reports whether s is a full RMW implementation
// identifier this codebase recognizes (the values ros2RMWAliases maps to).
// Callers validate an RMW string read back from a container environment or
// label before injecting it into another container's environment, so a
// tampered or malformed value can never reach the sidecar's env
// (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
func IsValidRMWImplementation(s string) bool {
	switch s {
	case "rmw_cyclonedds_cpp", "rmw_fastrtps_cpp", "rmw_connextdds", "rmw_gurumdds_cpp":
		return true
	default:
		return false
	}
}

// ROS2AutoDomainID derives a stable ROS_DOMAIN_ID from the appId so the
// domain does not change between restarts (WDY-884). The result is always in
// [ROS2DomainIDMin, ROS2DomainIDMax].
func ROS2AutoDomainID(appID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(appID))
	return int(h.Sum32() % uint32(ROS2DomainIDMax-ROS2DomainIDMin+1))
}

// ResolvedDomainID returns the effective ROS_DOMAIN_ID: the explicit
// domainId when set, otherwise a stable hash of appID. It returns -1 when an
// explicit domainId is outside the valid 0–232 range.
func (r *ROS2Config) ResolvedDomainID(appID string) int {
	if r.DomainID == nil {
		return ROS2AutoDomainID(appID)
	}
	id := *r.DomainID
	if id < ROS2DomainIDMin || id > ROS2DomainIDMax {
		return -1
	}
	return id
}

// ResolvedRMW returns the full RMW implementation identifier for the config,
// defaulting to CycloneDDS. It returns "" for unknown rmw values so callers
// can drop the value rather than inject an unvalidated string.
func (r *ROS2Config) ResolvedRMW() string {
	if r.RMW == "" {
		return ROS2DefaultRMW
	}
	return ros2RMWAliases[strings.ToLower(r.RMW)]
}

// ResolvedDistro returns the ROS 2 distribution for the config, defaulting
// to ROS2DefaultDistro.
func (r *ROS2Config) ResolvedDistro() string {
	if r.Distro == "" {
		return ROS2DefaultDistro
	}
	return strings.ToLower(r.Distro)
}

// ResolveROS2ConfigForService returns the effective ROS 2 config for the
// named service: the service-level frameworks.ros2 when present, otherwise
// the group-level frameworks.ros2. serviceName may be empty for
// single-container apps. Returns nil when ROS 2 is not configured.
func (a *AppConfig) ResolveROS2ConfigForService(serviceName string) *ROS2Config {
	if serviceName != "" {
		if svc, ok := a.Services[serviceName]; ok && svc != nil && svc.Frameworks != nil && svc.Frameworks.ROS2 != nil {
			return svc.Frameworks.ROS2
		}
	}
	return a.GetROS2Config()
}

// ROS2AnnotationValue encodes the resolved ROS 2 configuration for the
// sh.wendy/entitlement.ros2 container label, e.g. "distro=humble,domain_id=42".
// It returns "" when the resolved domain ID is invalid.
func ROS2AnnotationValue(r *ROS2Config, appID string) string {
	domainID := r.ResolvedDomainID(appID)
	if domainID < 0 {
		return ""
	}
	return "distro=" + r.ResolvedDistro() + ",domain_id=" + strconv.Itoa(domainID)
}

// ParseROS2Annotation decodes a sh.wendy/entitlement.ros2 label value
// produced by ROS2AnnotationValue. ok is false when the value is missing
// required fields or malformed.
func ParseROS2Annotation(value string) (distro string, domainID int, ok bool) {
	domainID = -1
	for _, part := range strings.Split(value, ",") {
		key, val, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		switch key {
		case "distro":
			distro = val
		case "domain_id":
			if n, err := strconv.Atoi(val); err == nil {
				domainID = n
			}
		}
	}
	if distro == "" || domainID < ROS2DomainIDMin || domainID > ROS2DomainIDMax {
		return "", 0, false
	}
	return distro, domainID, true
}
