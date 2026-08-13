package hoststats

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseNvidiaSMI(t *testing.T) {
	// --query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw
	// --format=csv,noheader,nounits  (memory in MiB)
	csv := "0, NVIDIA RTX A2000, 12, 1024, 6138, 45, 18.42\n"
	got := ParseNvidiaSMI(csv)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	g := got[0]
	if g.Index != 0 || g.Name != "NVIDIA RTX A2000" {
		t.Errorf("index/name = %d/%q", g.Index, g.Name)
	}
	if g.UtilPercent != 12 {
		t.Errorf("util = %v, want 12", g.UtilPercent)
	}
	if g.MemUsedBytes != 1024*1024*1024 { // 1024 MiB
		t.Errorf("memUsed = %d, want %d", g.MemUsedBytes, 1024*1024*1024)
	}
	if g.MemTotalBytes != 6138*1024*1024 {
		t.Errorf("memTotal = %d", g.MemTotalBytes)
	}
	if g.TempC == nil || *g.TempC != 45 {
		t.Errorf("tempC = %v, want 45", g.TempC)
	}
	if g.PowerW == nil || *g.PowerW != 18.42 {
		t.Errorf("powerW = %v, want 18.42", g.PowerW)
	}
}

func TestParseTegrastats(t *testing.T) {
	line := "RAM 4096/30536MB (lfb 5x4MB) SWAP 0/15268MB (cached 0MB) " +
		"CPU [10%@1190,5%@1190] GR3D_FREQ 37% cpu@49C GPU@48C " +
		"VDD_GPU_SOC 1234mW/1234mW"
	got := ParseTegrastats(line)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	g := got[0]
	if g.UtilPercent != 37 {
		t.Errorf("util = %v, want 37", g.UtilPercent)
	}
	if g.TempC == nil || *g.TempC != 48 {
		t.Errorf("tempC = %v, want 48", g.TempC)
	}
	if g.PowerW == nil || *g.PowerW != 1.234 { // 1234 mW
		t.Errorf("powerW = %v, want 1.234", g.PowerW)
	}
	if g.MemUsedBytes != 0 || g.MemTotalBytes != 0 {
		t.Errorf("mem = %d/%d, want 0/0 (unified memory has no per-GPU figure)", g.MemUsedBytes, g.MemTotalBytes)
	}
}

func TestParseTegrastatsWindowAveragesBurstyUtilization(t *testing.T) {
	output := strings.Join([]string{
		"RAM 1/2MB GR3D_FREQ 0% GPU@48C VDD_GPU_SOC 1000mW/1000mW",
		"RAM 1/2MB GR3D_FREQ 80% GPU@49C VDD_GPU_SOC 2000mW/2000mW",
		"RAM 1/2MB GR3D_FREQ 0% GPU@50C VDD_GPU_SOC 3000mW/3000mW",
		"RAM 1/2MB GR3D_FREQ 100% GPU@51C VDD_GPU_SOC 4000mW/4000mW",
		"RAM 1/2MB GR3D_FREQ 0% GPU@52C VDD_GPU_SOC 5000mW/5000mW",
	}, "\n")

	got := ParseTegrastatsWindow(output)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	g := got[0]
	if g.UtilPercent != 36 {
		t.Errorf("util = %v, want 36", g.UtilPercent)
	}
	if g.TempC == nil || *g.TempC != 52 {
		t.Errorf("tempC = %v, want latest reading 52", g.TempC)
	}
	if g.PowerW == nil || *g.PowerW != 5 {
		t.Errorf("powerW = %v, want latest reading 5", g.PowerW)
	}
}

func TestParseTegrastatsWindowIgnoresLinesWithoutGPUData(t *testing.T) {
	output := "startup noise\nRAM 1/2MB GR3D_FREQ 40% GPU@48C\n"
	got := ParseTegrastatsWindow(output)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	if got[0].UtilPercent != 40 {
		t.Errorf("util = %v, want 40", got[0].UtilPercent)
	}
}

func TestParseTegrastatsWindowNoGPUFields(t *testing.T) {
	if got := ParseTegrastatsWindow("RAM 1/2MB CPU [1%@100]\n"); len(got) != 0 {
		t.Errorf("got %+v, want no gpus", got)
	}
}

func TestParseTegrastatsNoGPUFields(t *testing.T) {
	// A line with no GR3D_FREQ should yield no GPU entries rather than a bogus 0%.
	got := ParseTegrastats("RAM 100/200MB CPU [1%@100]")
	if len(got) != 0 {
		t.Errorf("got %d gpus, want 0", len(got))
	}
}

func TestParseNvidiaSMIUnifiedMemoryNA(t *testing.T) {
	// Jetson (unified memory): nvidia-smi answers "[N/A]" for both memory
	// fields. They must stay zero (renderers treat 0 as "not applicable").
	// Observed on a Jetson AGX Thor (JetPack 7.2), 2026-07-02.
	csv := "0, NVIDIA Thor, 85, [N/A], [N/A], 62, 37.53\n"
	got := ParseNvidiaSMI(csv)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	g := got[0]
	if g.MemUsedBytes != 0 {
		t.Errorf("memUsed = %d, want 0 for [N/A]", g.MemUsedBytes)
	}
	if g.MemTotalBytes != 0 {
		t.Errorf("memTotal = %d, want 0 for [N/A]", g.MemTotalBytes)
	}
	if g.UtilPercent != 85 {
		t.Errorf("util = %v, want 85", g.UtilPercent)
	}
	if g.TempC == nil || *g.TempC != 62 {
		t.Errorf("tempC = %v, want 62", g.TempC)
	}
	if g.PowerW == nil || *g.PowerW != 37.53 {
		t.Errorf("powerW = %v, want 37.53", g.PowerW)
	}
}

func TestParseNvidiaSMIOrinMarksUtilizationUnavailable(t *testing.T) {
	// Verbatim JetPack 6.2.1 output from an Orin while tegrastats reported an
	// active GPU. Treating [N/A] as the float zero caused device top to lie.
	csv := "0, Orin (nvgpu), [N/A], [N/A], [N/A], [N/A], [N/A]\n"
	got, utilizationAvailable := parseNvidiaSMI(csv)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	if utilizationAvailable {
		t.Fatal("utilizationAvailable = true, want false for [N/A]")
	}
	if got[0].Name != "Orin (nvgpu)" {
		t.Errorf("name = %q, want Orin (nvgpu)", got[0].Name)
	}
}

func TestParseNvidiaSMIThorMarksUtilizationAvailable(t *testing.T) {
	csv := "0, NVIDIA Thor, 85, [N/A], [N/A], 62, 37.53\n"
	_, utilizationAvailable := parseNvidiaSMI(csv)
	if !utilizationAvailable {
		t.Fatal("utilizationAvailable = false, want true for numeric utilization")
	}
}

func TestSampleGPUFallsBackToTegrastatsForOrin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses executable shell fixtures")
	}

	binDir := t.TempDir()
	writeExecutable := func(name, body string) {
		t.Helper()
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeExecutable("nvidia-smi", "printf '%s\\n' '0, Orin (nvgpu), [N/A], [N/A], [N/A], [N/A], [N/A]'\n")
	writeExecutable("tegrastats", "printf '%s\\n' 'RAM 1/2MB GR3D_FREQ 20% GPU@48C' 'RAM 1/2MB GR3D_FREQ 80% GPU@50C'\n")
	t.Setenv("PATH", binDir)

	got := SampleGPU(context.Background())
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	if got[0].Name != "Orin (nvgpu)" {
		t.Errorf("name = %q, want nvidia-smi identity", got[0].Name)
	}
	if got[0].UtilPercent != 50 {
		t.Errorf("util = %v, want tegrastats window mean 50", got[0].UtilPercent)
	}
	if got[0].TempC == nil || *got[0].TempC != 50 {
		t.Errorf("tempC = %v, want latest tegrastats reading 50", got[0].TempC)
	}
}

func TestParseTegrastatsJetPack7Thor(t *testing.T) {
	// Verbatim tegrastats line from a Jetson AGX Thor (WendyOS-0.16.1,
	// JetPack 7.2, kernel 6.8.12-l4t-r39.2.0), 2026-07-02: no GR3D_FREQ
	// field at all, lowercase "gpu@" temperature, and the GPU power rail
	// renamed VDD_GPU_SOC → VDD_GPU.
	line := "07-02-2026 13:45:44 RAM 36529/125749MB (lfb 12x4MB) SWAP 11/4096MB (cached 9MB) " +
		"CPU [2%@972,1%@972,0%@972,0%@972,0%@972,0%@972,0%@972,0%@972,0%@972,0%@972,0%@972,0%@972,0%@972,2%@972] " +
		"cpu@60.406C tj@63.281C soc012@58.468C gpu@62.906C soc345@63.281C " +
		"VDD_GPU 37528mW/37528mW/37528mW VDD_CPU_SOC_MSS 18961mW/18961mW/18961mW " +
		"VIN_SYS_5V0 18331mW/18331mW/18331mW VIN 87426mW/43713mW/87426mW"
	got := ParseTegrastats(line)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1 (JP7 line must not be dropped)", len(got))
	}
	g := got[0]
	if g.UtilPercent != 0 {
		t.Errorf("util = %v, want 0 (JP7 tegrastats reports no GPU utilization)", g.UtilPercent)
	}
	if g.TempC == nil || *g.TempC != 62.906 {
		t.Errorf("tempC = %v, want 62.906", g.TempC)
	}
	if g.PowerW == nil || *g.PowerW != 37.528 { // VDD_GPU, not VDD_CPU_SOC_MSS
		t.Errorf("powerW = %v, want 37.528", g.PowerW)
	}
	if g.MemUsedBytes != 0 || g.MemTotalBytes != 0 {
		t.Errorf("mem = %d/%d, want 0/0 (unified memory)", g.MemUsedBytes, g.MemTotalBytes)
	}
}

func TestParseTegrastatsPowerOnlyStillYieldsEntry(t *testing.T) {
	// The GR3D_FREQ gate is gone: partial GPU data must survive.
	got := ParseTegrastats("RAM 1/2MB VDD_GPU 5000mW/5000mW")
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	if got[0].PowerW == nil || *got[0].PowerW != 5 {
		t.Errorf("powerW = %v, want 5", got[0].PowerW)
	}
	if got[0].TempC != nil {
		t.Errorf("tempC = %v, want nil", got[0].TempC)
	}
}

func TestParseTegrastatsCPURailNotMistakenForGPU(t *testing.T) {
	// A line with only CPU-ish fields must yield nothing — in particular the
	// VDD_CPU_SOC_MSS rail and cpu@ temperature must not read as GPU data.
	got := ParseTegrastats("RAM 1/2MB CPU [1%@100] cpu@50.5C VDD_CPU_SOC_MSS 9000mW/9000mW")
	if len(got) != 0 {
		t.Errorf("got %+v, want no gpus", got)
	}
}

func TestParseTegrastatsJetPack7ThorTempWithLimitSuffix(t *testing.T) {
	// Thor also emits temperatures as "gpu@41.843C/41.843C" (reading/limit);
	// the parser must capture the first reading. Live line, 2026-07-02.
	line := "07-02-2026 20:41:39 RAM 37755/125749MB (lfb 529x4MB) SWAP 31/4096MB (cached 16MB) " +
		"CPU [2%@972,0%@972] cpu@40.281C/40.281C tj@41.843C/41.843C soc012@39.281C/39.281C " +
		"gpu@41.843C/41.843C soc345@40.437C/40.437C " +
		"VDD_GPU 1975mW/1975mW/1975mW VDD_CPU_SOC_MSS 5530mW/5530mW/5530mW"
	got := ParseTegrastats(line)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	if got[0].TempC == nil || *got[0].TempC != 41.843 {
		t.Errorf("tempC = %v, want 41.843", got[0].TempC)
	}
	if got[0].PowerW == nil || *got[0].PowerW != 1.975 {
		t.Errorf("powerW = %v, want 1.975", got[0].PowerW)
	}
}
