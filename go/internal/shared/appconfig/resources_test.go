package appconfig

import "testing"

// pidsPtr is a test helper: ResourceLimits.PIDs is a pointer so an absent field
// (nil → default cap) is distinguishable from an explicit, rejected 0.
func pidsPtr(v int64) *int64 { return &v }

func TestLoadFromBytes_Resources(t *testing.T) {
	data := []byte(`{
		"appId": "demo",
		"resources": { "memory": "512Mi", "cpus": "1.5", "pids": 256 },
		"services": {
			"worker": { "context": "./worker", "resources": { "memory": "256Mi" } }
		}
	}`)
	cfg, err := LoadFromBytes(data)
	if err != nil {
		t.Fatalf("LoadFromBytes: %v", err)
	}
	if cfg.Resources == nil || cfg.Resources.Memory != "512Mi" || cfg.Resources.CPUs != "1.5" || cfg.Resources.PIDs == nil || *cfg.Resources.PIDs != 256 {
		t.Fatalf("app-level resources not decoded: %+v", cfg.Resources)
	}
	svc := cfg.Services["worker"]
	if svc == nil || svc.Resources == nil || svc.Resources.Memory != "256Mi" {
		t.Fatalf("service-level resources not decoded: %+v", svc)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestResourceLimits_MemoryLimitBytes(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantNil bool
		wantErr bool
	}{
		{in: "", wantNil: true},
		{in: "1048576", want: 1048576},
		{in: "512Mi", want: 512 * 1024 * 1024},
		{in: "1Gi", want: 1024 * 1024 * 1024},
		{in: "256Ki", want: 256 * 1024},
		{in: "1M", want: 1000 * 1000},
		{in: "2G", want: 2 * 1000 * 1000 * 1000},
		{in: "512mi", want: 512 * 1024 * 1024}, // case-insensitive suffix
		{in: "0", wantErr: true},               // zero is not a useful limit
		{in: "-1", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "12Xi", wantErr: true},
		{in: "1.5Gi", wantErr: true},                 // fractional bytes not allowed
		{in: "9223372036854775807Ti", wantErr: true}, // overflows int64 after suffix multiply
		{in: "9999999999999999Gi", wantErr: true},    // also overflows
	}
	for _, c := range cases {
		r := &ResourceLimits{Memory: c.in}
		got, err := r.MemoryLimitBytes()
		if c.wantErr {
			if err == nil {
				t.Errorf("Memory %q: expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Memory %q: unexpected error %v", c.in, err)
			continue
		}
		if c.wantNil {
			if got != nil {
				t.Errorf("Memory %q: expected nil, got %d", c.in, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("Memory %q: want %d, got %v", c.in, c.want, got)
		}
	}
}

func TestResourceLimits_CPUQuota(t *testing.T) {
	cases := []struct {
		in         string
		wantQuota  int64
		wantPeriod uint64
		wantNil    bool
		wantErr    bool
	}{
		{in: "", wantNil: true},
		{in: "1", wantQuota: 100000, wantPeriod: 100000},
		{in: "1.5", wantQuota: 150000, wantPeriod: 100000},
		{in: "0.5", wantQuota: 50000, wantPeriod: 100000},
		{in: "2", wantQuota: 200000, wantPeriod: 100000},
		{in: "0", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "abc", wantErr: true},
		// Overflow / non-finite guards (security review HIGH-1): a huge or
		// non-finite core count must error, never wrap int64 into a tiny or
		// negative CFS quota.
		{in: "1e308", wantErr: true},
		{in: "Inf", wantErr: true},
		{in: "inf", wantErr: true},
		{in: "NaN", wantErr: true},
	}
	for _, c := range cases {
		r := &ResourceLimits{CPUs: c.in}
		quota, period, err := r.CPUQuota()
		if c.wantErr {
			if err == nil {
				t.Errorf("CPUs %q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("CPUs %q: unexpected error %v", c.in, err)
			continue
		}
		if c.wantNil {
			if quota != nil || period != nil {
				t.Errorf("CPUs %q: expected nil quota/period", c.in)
			}
			continue
		}
		if quota == nil || *quota != c.wantQuota {
			t.Errorf("CPUs %q: want quota %d, got %v", c.in, c.wantQuota, quota)
		}
		if period == nil || *period != c.wantPeriod {
			t.Errorf("CPUs %q: want period %d, got %v", c.in, c.wantPeriod, period)
		}
	}
}

func TestResourceLimits_PIDsLimit(t *testing.T) {
	if got := (&ResourceLimits{}).PIDsLimit(); got != nil {
		t.Errorf("unset PIDs: want nil, got %d", *got)
	}
	if got := (&ResourceLimits{PIDs: pidsPtr(256)}).PIDsLimit(); got == nil || *got != 256 {
		t.Errorf("PIDs 256: want 256, got %v", got)
	}
}

func TestValidate_Resources(t *testing.T) {
	cases := []struct {
		name    string
		res     *ResourceLimits
		wantErr bool
	}{
		{name: "nil resources ok", res: nil},
		{name: "valid all fields", res: &ResourceLimits{Memory: "512Mi", CPUs: "1.5", PIDs: pidsPtr(256)}},
		{name: "unset pids ok", res: &ResourceLimits{Memory: "512Mi"}},
		{name: "bad memory", res: &ResourceLimits{Memory: "lots"}, wantErr: true},
		{name: "overflow memory", res: &ResourceLimits{Memory: "9223372036854775807Ti"}, wantErr: true},
		{name: "bad cpus", res: &ResourceLimits{CPUs: "-2"}, wantErr: true},
		{name: "negative pids", res: &ResourceLimits{PIDs: pidsPtr(-5)}, wantErr: true},
		{name: "explicit zero pids rejected", res: &ResourceLimits{PIDs: pidsPtr(0)}, wantErr: true},
		{name: "pids at kernel ceiling ok", res: &ResourceLimits{PIDs: pidsPtr(4194304)}},
		{name: "pids above kernel ceiling rejected", res: &ResourceLimits{PIDs: pidsPtr(4194305)}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &AppConfig{AppID: "demo", Resources: c.res}
			err := cfg.Validate()
			if c.wantErr && err == nil {
				t.Errorf("expected validation error")
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidate_ServiceResources(t *testing.T) {
	cfg := &AppConfig{
		AppID: "demo",
		Services: map[string]*ServiceConfig{
			"worker": {Context: "./worker", Resources: &ResourceLimits{Memory: "nonsense"}},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for invalid service resources")
	}
}

func TestResolveResourcesForService(t *testing.T) {
	// app-level sets all three; "worker" overrides only memory. Per-field merge
	// (security review HIGH-2) must inherit the app-level cpus and the
	// security-relevant pids cap rather than silently dropping them.
	cfg := &AppConfig{
		Resources: &ResourceLimits{Memory: "1Gi", CPUs: "2", PIDs: pidsPtr(512)},
		Services: map[string]*ServiceConfig{
			"worker": {Context: "./worker", Resources: &ResourceLimits{Memory: "256Mi"}},
			"pidsvc": {Context: "./pidsvc", Resources: &ResourceLimits{PIDs: pidsPtr(64)}},
			"web":    {Context: "./web"},
		},
	}

	worker := cfg.ResolveResourcesForService("worker")
	if worker == nil || worker.Memory != "256Mi" {
		t.Fatalf("worker memory: want 256Mi, got %+v", worker)
	}
	if worker.CPUs != "2" {
		t.Errorf("worker must inherit app-level cpus=2, got %q", worker.CPUs)
	}
	if worker.PIDs == nil || *worker.PIDs != 512 {
		t.Errorf("worker must inherit app-level pids=512 (not silently dropped), got %v", worker.PIDs)
	}

	// A service may still tighten a field: pidsvc overrides pids, inherits memory.
	pidsvc := cfg.ResolveResourcesForService("pidsvc")
	if pidsvc == nil || pidsvc.PIDs == nil || *pidsvc.PIDs != 64 {
		t.Errorf("pidsvc should override pids to 64, got %v", pidsvc)
	}
	if pidsvc.Memory != "1Gi" {
		t.Errorf("pidsvc should inherit app-level memory=1Gi, got %q", pidsvc.Memory)
	}

	// web declares no resources of its own → app-level applies.
	if got := cfg.ResolveResourcesForService("web"); got == nil || got.Memory != "1Gi" {
		t.Errorf("web should inherit app-level resources, got %+v", got)
	}
	// single-container app uses app-level.
	if got := cfg.ResolveResourcesForService(""); got == nil || got.Memory != "1Gi" {
		t.Errorf("single-container app should use app-level resources, got %+v", got)
	}
	// nothing set anywhere → nil.
	if got := (&AppConfig{}).ResolveResourcesForService("x"); got != nil {
		t.Errorf("absent resources should resolve to nil, got %+v", got)
	}
	// Merge must not mutate the app-level struct.
	if cfg.Resources.Memory != "1Gi" {
		t.Errorf("app-level Resources was mutated by merge: %+v", cfg.Resources)
	}
}
