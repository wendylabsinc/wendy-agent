// Package containerd implements the ContainerdClient interface using the official
// containerd v2 SDK to manage containers, images, and content on the agent device.
package containerd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// safeJoin joins base and a single path component, returning an error if the
// component contains a path separator or a dot-only segment, or if the result
// does not fall directly under base. Use this wherever an attacker-controlled
// string is joined with a trusted base directory to prevent path traversal
// (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
func safeJoin(base, component string) (string, error) {
	if strings.ContainsRune(component, filepath.Separator) {
		return "", fmt.Errorf("path component %q contains path separator", component)
	}
	if component == "." || component == ".." {
		return "", fmt.Errorf("path component %q is not allowed", component)
	}
	joined := filepath.Join(base, component)
	if !strings.HasPrefix(filepath.Clean(joined), filepath.Clean(base)+string(filepath.Separator)) {
		return "", fmt.Errorf("component %q escapes base directory %q", component, base)
	}
	return joined, nil
}

// normalizeImageName canonicalises a Docker short reference (e.g.
// "python:3.11-slim", "nginx") to a fully-qualified form
// ("docker.io/library/python:3.11-slim") that containerd's reference parser
// accepts. References that already include a registry, tag, or digest pass
// through unchanged. When the input cannot be parsed as a valid Docker
// reference, the original string is returned so existing error paths still
// surface a meaningful diagnostic.
func normalizeImageName(image string) string {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return image
	}
	named, err := reference.ParseNormalizedNamed(trimmed)
	if err != nil {
		return image
	}
	return reference.TagNameOnly(named).String()
}

// labelKeyAppVersion is the containerd label key that marks Wendy-managed containers.
const labelKeyAppVersion = "sh.wendy/app.version"

// labelKeyRestartPolicy stores the restart policy (e.g. "on-failure:5").
const labelKeyRestartPolicy = "sh.wendy/restart.policy"

// labelKeyMCPPort stores the MCP server port for containers with an mcp entitlement.
const labelKeyMCPPort = "sh.wendy/mcp.port"

// labelKeyGCRoot prevents garbage collection of content blobs.
const labelKeyGCRoot = "containerd.io/gc.root"

// labelKeyWendyLayer marks a content blob as a Wendy-pushed layer.
const labelKeyWendyLayer = "sh.wendy.layer"

// labelKeyAppID is the app identity (appId from wendy.json) for every
// Wendy-managed container. Always set, regardless of whether the app uses
// multi-service naming. Used to find all containers belonging to an app without
// relying on container-name conventions.
const labelKeyAppID = "sh.wendy/app.id"

// labelKeyServiceName is the service name for a multi-service container.
// Set whenever appCfg.ServiceName is non-empty.
const labelKeyServiceName = "sh.wendy/service"

// ContainerName returns the containerd container ID for the given appID and
// optional serviceName.
//
//   - Single-container apps (serviceName == ""): returns appID unchanged,
//     preserving backward-compatibility with all existing tooling.
//   - Multi-service apps (serviceName != ""): returns "{appID}_{serviceName}".
//     "_" is the separator because containerd identifiers must match
//     ^[A-Za-z0-9]+(?:[._-](?:[A-Za-z0-9]+))*$ and "/" is not in that set.
//     serviceName is validated to contain only [a-z0-9-] so it cannot itself
//     contain "_", keeping the last "_" in the name as the unambiguous
//     app–service boundary (validated inputs only; see Precondition).
//
// Precondition: callers must have validated appID with appconfig.ValidateAppID
// and serviceName with appconfig.ValidateServiceName before calling. A
// serviceName containing "_" would break ParseContainerName; neither is
// possible when both values have passed validation.
func ContainerName(appID, serviceName string) string {
	if serviceName == "" {
		return appID
	}
	return appID + "_" + serviceName
}

// SnapshotKey returns the containerd snapshot key for the given appID and
// optional serviceName.
//
//   - Single-container apps (serviceName == ""): "wendy-{appID}" (unchanged).
//   - Multi-service apps (serviceName != ""): "wendy-{appID}@{serviceName}".
//
// "@" is used as the separator because it cannot appear in a valid appID
// ([a-zA-Z0-9._-]) or a valid serviceName ([a-z]([a-z0-9-]{0,55}[a-z0-9])?), making
// the key unambiguous and free of collisions (e.g. SnapshotKey("foo-bar","baz")
// vs SnapshotKey("foo","bar-baz") produce distinct keys).
// Note: the key is not path-sanitised; "@" is safe for overlayfs snapshot
// stores (the containerd default), but callers must not treat it as a filename.
//
// Precondition: same as ContainerName — inputs must have passed validation.
func SnapshotKey(appID, serviceName string) string {
	if serviceName == "" {
		return "wendy-" + appID
	}
	return "wendy-" + appID + "@" + serviceName
}

// ParseContainerName is the inverse of ContainerName. It splits a container
// name of the form "{appID}" or "{appID}_{serviceName}" back into its
// components. Returns an error when the name is malformed.
//
// The separator is "_". Because serviceName is validated to contain only
// [a-z0-9-] (no underscores), the LAST "_" in a multi-service container name
// is the unambiguous boundary. The algorithm:
//  1. If ValidateAppID passes for the whole name → single-container, no service.
//  2. Otherwise try splitting at the last "_": if the suffix passes
//     ValidateServiceName and the prefix passes ValidateAppID → multi-service.
//  3. Otherwise the name is malformed.
//
// Note: this function is used for format validation in StartContainer and as a
// best-effort parser in recreateContainer (which prefers container labels as
// the authoritative source for appID/serviceName).
func ParseContainerName(name string) (appID, serviceName string, err error) {
	// Fast path: whole name is a valid appID (single-container app).
	if appconfig.ValidateAppID(name) == nil {
		return name, "", nil
	}
	// Try to split at the last "_" (multi-service: "{appID}_{serviceName}").
	idx := strings.LastIndexByte(name, '_')
	if idx > 0 {
		prefix, suffix := name[:idx], name[idx+1:]
		if appconfig.ValidateAppID(prefix) == nil && appconfig.ValidateServiceName(suffix) == nil {
			return prefix, suffix, nil
		}
	}
	return "", "", fmt.Errorf("invalid container name %q: does not match appID or appID_serviceName format", sanitizeForLog(name, 300))
}

// computeChainID computes the chain ID for a layer given its parent chain ID
// and the layer's diff ID. The chain ID is defined recursively:
//
//	chainID(L0) = diffID(L0)
//	chainID(L0|...|Ln) = SHA256(chainID(L0|...|Ln-1) + " " + diffID(Ln))
func computeChainID(parent, diffID string) string {
	if parent == "" {
		return diffID
	}
	h := sha256.New()
	h.Write([]byte(parent + " " + diffID))
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// parseRestartPolicyLabel parses a restart policy label value such as
// "on-failure:5" or "unless-stopped" into the policy string and max retries.
func parseRestartPolicyLabel(label string) (string, int) {
	parts := strings.SplitN(label, ":", 2)
	policy := parts[0]
	maxRetries := 0
	if len(parts) == 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			maxRetries = n
		}
	}
	return policy, maxRetries
}

// isLocalRegistryImage reports whether the image reference points at the
// device-local HTTP registry. Such pulls must use a PlainHTTP resolver, but
// they should be a fallback only — the registry shares containerd's content
// store, so a successful GetImage avoids round-tripping bytes over loopback.
func isLocalRegistryImage(imageName string) bool {
	return strings.HasPrefix(imageName, "localhost:5000/") ||
		strings.HasPrefix(imageName, "127.0.0.1:5000/") ||
		strings.HasPrefix(imageName, "[::1]:5000/") ||
		strings.HasPrefix(imageName, "localhost:5555/") ||
		strings.HasPrefix(imageName, "127.0.0.1:5555/") ||
		strings.HasPrefix(imageName, "[::1]:5555/")
}

func gcTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// sanitizeForLog strips control characters from s (replacing each with '?')
// and truncates to maxLen bytes before the result is used as a structured log
// field. This prevents log injection when s has not yet passed validation;
// zap's JSON encoder is safe, but text/syslog transports are not.
func sanitizeForLog(s string, maxLen int) string {
	s = s[:min(len(s), maxLen)]
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// wendyLabels builds the standard set of containerd labels for a Wendy-managed
// container. These labels are used to identify, filter, and manage containers.
//
// When serviceName is non-empty (multi-service app), labelKeyServiceName is
// additionally set to serviceName.
func wendyLabels(appName, serviceName, version string, restartPolicy *agentpb.RestartPolicy, entitlements []appconfig.Entitlement) map[string]string {
	labels := map[string]string{
		labelKeyAppVersion: version,
		labelKeyAppID:      appName,
	}

	if serviceName != "" {
		labels[labelKeyServiceName] = serviceName
	}

	if restartPolicy != nil {
		policyStr := restartPolicyToLabel(restartPolicy)
		if policyStr != "" {
			labels[labelKeyRestartPolicy] = policyStr
		}
	}

	for _, e := range entitlements {
		if e.Type == appconfig.EntitlementMCP && e.Port > 0 {
			labels[labelKeyMCPPort] = strconv.FormatUint(uint64(e.Port), 10)
			break
		}
	}

	for k, v := range appconfig.BuildEntitlementAnnotations(entitlements) {
		labels[k] = v
	}

	return labels
}

// parseEntitlementsFromAnnotations reconstructs an entitlement list from OCI
// manifest annotations or containerd container labels. It is the inverse of
// buildEntitlementAnnotations / wendyLabels.
// Keys have the form sh.wendy/entitlement.<type> (single) or
// sh.wendy/entitlement.<type>.<index> (multiple of the same type). Values use
// the comma-separated key=value format produced by appconfig.EntitlementAnnotationValue.
func parseEntitlementsFromAnnotations(annotations map[string]string) []appconfig.Entitlement {
	type indexedEnt struct {
		entType string
		idx     int
		ent     appconfig.Entitlement
	}

	var indexed []indexedEnt
	for k, v := range annotations {
		if !strings.HasPrefix(k, appconfig.EntitlementAnnotationKeyPrefix) {
			continue
		}
		suffix := k[len(appconfig.EntitlementAnnotationKeyPrefix):]

		// sh.wendy/entitlement.ros2 carries framework config (distro, DDS
		// domain), not an entitlement; it has its own codec (WDY-884).
		if suffix == "ros2" {
			continue
		}

		entType := suffix
		idx := 0
		if dot := strings.LastIndex(suffix, "."); dot >= 0 {
			if n, err := strconv.Atoi(suffix[dot+1:]); err == nil {
				entType = suffix[:dot]
				idx = n
			}
		}

		indexed = append(indexed, indexedEnt{
			entType: entType,
			idx:     idx,
			ent:     parseEntitlementValue(entType, v),
		})
	}

	sort.Slice(indexed, func(i, j int) bool {
		if indexed[i].entType != indexed[j].entType {
			return indexed[i].entType < indexed[j].entType
		}
		return indexed[i].idx < indexed[j].idx
	})

	result := make([]appconfig.Entitlement, len(indexed))
	for i, ie := range indexed {
		result[i] = ie.ent
	}
	return result
}

// parseEntitlementValue parses a single entitlement annotation value. It
// accepts both the current JSON format ({"mode":"host"}) and the legacy
// comma-separated format (mode=host) for backward compatibility.
func parseEntitlementValue(entType, value string) appconfig.Entitlement {
	if len(value) > 0 && value[0] == '{' {
		var ent appconfig.Entitlement
		if err := json.Unmarshal([]byte(value), &ent); err == nil {
			ent.Type = entType
			return ent
		}
	}
	return appconfig.ParseEntitlementAnnotation(entType, value)
}

// restartPolicyToLabel converts a protobuf RestartPolicy to a label string.
func restartPolicyToLabel(rp *agentpb.RestartPolicy) string {
	if rp == nil {
		return ""
	}
	switch rp.GetMode() {
	case agentpb.RestartPolicyMode_NO:
		return "no"
	case agentpb.RestartPolicyMode_UNLESS_STOPPED:
		return "unless-stopped"
	case agentpb.RestartPolicyMode_ON_FAILURE:
		maxRetries := rp.GetOnFailureMaxRetries()
		if maxRetries > 0 {
			return fmt.Sprintf("on-failure:%d", maxRetries)
		}
		return "on-failure"
	case agentpb.RestartPolicyMode_DEFAULT:
		return "unless-stopped"
	default:
		return ""
	}
}
