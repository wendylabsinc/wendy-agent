package platforminfo

import (
	"strings"
	"testing"
)

type fakeProber struct{ osVer, kernel string }

func (f fakeProber) OSVersion() string { return f.osVer }
func (f fakeProber) Kernel() string    { return f.kernel }

func TestCollectFillsDevFields(t *testing.T) {
	old := defaultProber
	defaultProber = fakeProber{osVer: "15.5", kernel: "Darwin 24.5.0"}
	t.Cleanup(func() { defaultProber = old })

	got := Collect()
	if got.DevOSVersion != "15.5" {
		t.Errorf("DevOSVersion = %q, want 15.5", got.DevOSVersion)
	}
	if got.DevOS == "" || got.DevArch == "" {
		t.Errorf("DevOS/DevArch should be populated, got %q/%q", got.DevOS, got.DevArch)
	}
	if got.CLIVersion == "" {
		t.Error("CLIVersion should be populated")
	}
}

func TestOneLineCompact(t *testing.T) {
	i := Info{CLIVersion: "0.10.2", DevOS: "darwin", DevOSVersion: "15.5", DevArch: "arm64"}
	line := i.OneLine()
	if !strings.Contains(line, "0.10.2") || !strings.Contains(line, "arm64") {
		t.Errorf("OneLine missing fields: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("OneLine must be single line: %q", line)
	}
}

func TestOneLineAppendsTarget(t *testing.T) {
	i := Info{CLIVersion: "0.10.2", DevOS: "darwin", DevArch: "arm64"}
	i.WithAgentVersion("0.9.1", "wendyos", "2026.06.10", "jetson-orin-nano", "", "", "", "")
	line := i.OneLine()
	if !strings.Contains(line, "jetson-orin-nano") || !strings.Contains(line, "0.9.1") {
		t.Errorf("OneLine should include target info: %q", line)
	}
}

func TestProtoRoundTrip(t *testing.T) {
	i := Info{CLIVersion: "0.10.2", DevOS: "linux", DevArch: "amd64", TargetHardware: "rpi5"}
	p := i.Proto()
	if p.GetCliVersion() != "0.10.2" || p.GetTargetHardware() != "rpi5" {
		t.Errorf("proto mismatch: %+v", p)
	}
}
