package hoststats

import "testing"

func TestParseProcStat(t *testing.T) {
	// cpu  <user> <nice> <system> <idle> <iowait> <irq> <softirq> <steal> ...
	data := []byte("cpu  100 0 200 700 50 0 10 0 0 0\n" +
		"cpu0 50 0 100 350 25 0 5 0 0 0\n" +
		"cpu1 50 0 100 350 25 0 5 0 0 0\n" +
		"intr 12345\n")
	got, err := ParseProcStat(data)
	if err != nil {
		t.Fatalf("ParseProcStat: %v", err)
	}
	// total = 100+0+200+700+50+0+10+0+0+0 = 1060
	if got.TotalJiffies != 1060 {
		t.Errorf("TotalJiffies = %d, want 1060", got.TotalJiffies)
	}
	// idle = idle(700) + iowait(50) = 750
	if got.IdleJiffies != 750 {
		t.Errorf("IdleJiffies = %d, want 750", got.IdleJiffies)
	}
	if got.CPUCount != 2 {
		t.Errorf("CPUCount = %d, want 2", got.CPUCount)
	}
}

func TestParseMemInfo(t *testing.T) {
	data := []byte("MemTotal:       16384000 kB\n" +
		"MemFree:         1000000 kB\n" +
		"MemAvailable:    8192000 kB\n")
	got, err := ParseMemInfo(data)
	if err != nil {
		t.Fatalf("ParseMemInfo: %v", err)
	}
	if got.TotalBytes != 16384000*1024 {
		t.Errorf("TotalBytes = %d, want %d", got.TotalBytes, 16384000*1024)
	}
	if got.AvailableBytes != 8192000*1024 {
		t.Errorf("AvailableBytes = %d, want %d", got.AvailableBytes, 8192000*1024)
	}
}
