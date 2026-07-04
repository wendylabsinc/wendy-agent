// Package platforminfo assembles a structured snapshot of the developer
// machine and (optionally) the connected target device for logs and crash
// reports. Collection never fails: missing data yields empty strings.
package platforminfo

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// Info is an environment snapshot. Target* fields are empty until
// WithAgentVersion is called with a connected device's version response.
type Info struct {
	CLIVersion           string `json:"cli_version,omitempty"`
	DevOS                string `json:"dev_os,omitempty"`
	DevOSVersion         string `json:"dev_os_version,omitempty"`
	DevArch              string `json:"dev_arch,omitempty"`
	DevKernel            string `json:"dev_kernel,omitempty"`
	TargetAgentVersion   string `json:"target_agent_version,omitempty"`
	TargetOS             string `json:"target_os,omitempty"`
	TargetOSVersion      string `json:"target_os_version,omitempty"`
	TargetHardware       string `json:"target_hardware,omitempty"`
	TargetGPUVendor      string `json:"target_gpu_vendor,omitempty"`
	TargetJetpackVersion string `json:"target_jetpack_version,omitempty"`
	TargetCUDAVersion    string `json:"target_cuda_version,omitempty"`
	TargetStorageMedium  string `json:"target_storage_medium,omitempty"`
}

// Collect gathers developer-machine fields. It never returns an error;
// unavailable probes leave their fields empty.
func Collect() Info {
	return Info{
		CLIVersion:   version.Version,
		DevOS:        runtime.GOOS,
		DevOSVersion: defaultProber.OSVersion(),
		DevArch:      runtime.GOARCH,
		DevKernel:    defaultProber.Kernel(),
	}
}

// WithAgentVersion fills the target-device fields. Callers pass values read off
// *agentpb.GetAgentVersionResponse; this package avoids importing agentpb to
// keep it dependency-light and free of import cycles.
func (i *Info) WithAgentVersion(agentVersion, os, osVersion, hardware, gpuVendor, jetpack, cuda, storage string) {
	i.TargetAgentVersion = agentVersion
	i.TargetOS = os
	i.TargetOSVersion = osVersion
	i.TargetHardware = hardware
	i.TargetGPUVendor = gpuVendor
	i.TargetJetpackVersion = jetpack
	i.TargetCUDAVersion = cuda
	i.TargetStorageMedium = storage
}

// OneLine renders a compact single-line summary for the startup banner.
func (i Info) OneLine() string {
	var b strings.Builder
	fmt.Fprintf(&b, "wendy %s · %s", emptyDash(i.CLIVersion), emptyDash(i.DevOS))
	if i.DevOSVersion != "" {
		fmt.Fprintf(&b, " %s", i.DevOSVersion)
	}
	if i.DevArch != "" {
		fmt.Fprintf(&b, " %s", i.DevArch)
	}
	if i.TargetHardware != "" || i.TargetAgentVersion != "" || i.TargetOS != "" {
		fmt.Fprintf(&b, " → %s", emptyDash(i.TargetHardware))
		if i.TargetOS != "" {
			fmt.Fprintf(&b, " %s", i.TargetOS)
		}
		if i.TargetOSVersion != "" {
			fmt.Fprintf(&b, " %s", i.TargetOSVersion)
		}
		if i.TargetAgentVersion != "" {
			fmt.Fprintf(&b, " agent %s", i.TargetAgentVersion)
		}
	}
	return b.String()
}

// Block renders the full multi-line view for --verbose and crash reports.
func (i Info) Block() string {
	lines := []string{
		fmt.Sprintf("CLI version:    %s", emptyDash(i.CLIVersion)),
		fmt.Sprintf("Dev OS:         %s %s (%s)", emptyDash(i.DevOS), i.DevOSVersion, emptyDash(i.DevArch)),
		fmt.Sprintf("Dev kernel:     %s", emptyDash(i.DevKernel)),
	}
	if i.TargetAgentVersion != "" || i.TargetOS != "" || i.TargetHardware != "" {
		lines = append(lines,
			fmt.Sprintf("Target OS:      %s %s", emptyDash(i.TargetOS), i.TargetOSVersion),
			fmt.Sprintf("Target HW:      %s", emptyDash(i.TargetHardware)),
			fmt.Sprintf("Agent version:  %s", emptyDash(i.TargetAgentVersion)),
		)
		if i.TargetGPUVendor != "" {
			lines = append(lines, fmt.Sprintf("GPU:            %s", i.TargetGPUVendor))
		}
		if i.TargetJetpackVersion != "" {
			lines = append(lines, fmt.Sprintf("JetPack:        %s", i.TargetJetpackVersion))
		}
		if i.TargetCUDAVersion != "" {
			lines = append(lines, fmt.Sprintf("CUDA:           %s", i.TargetCUDAVersion))
		}
		if i.TargetStorageMedium != "" {
			lines = append(lines, fmt.Sprintf("Storage:        %s", i.TargetStorageMedium))
		}
	}
	return strings.Join(lines, "\n")
}

// Proto converts the snapshot to the wire type.
func (i Info) Proto() *cloudpb.PlatformInfo {
	return &cloudpb.PlatformInfo{
		CliVersion:           i.CLIVersion,
		DevOs:                i.DevOS,
		DevOsVersion:         i.DevOSVersion,
		DevArch:              i.DevArch,
		DevKernel:            i.DevKernel,
		TargetAgentVersion:   i.TargetAgentVersion,
		TargetOs:             i.TargetOS,
		TargetOsVersion:      i.TargetOSVersion,
		TargetHardware:       i.TargetHardware,
		TargetGpuVendor:      i.TargetGPUVendor,
		TargetJetpackVersion: i.TargetJetpackVersion,
		TargetCudaVersion:    i.TargetCUDAVersion,
		TargetStorageMedium:  i.TargetStorageMedium,
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
