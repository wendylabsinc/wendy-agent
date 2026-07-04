package hoststats

import (
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
