package hoststats

import "testing"

func TestResolveThermalCombinesAndSortsSupplementalSource(t *testing.T) {
	root := t.TempDir()
	oldRoot := thermalRoot
	thermalRoot = root
	t.Cleanup(func() {
		thermalRoot = oldRoot
		SetSupplementalThermalSource(nil)
	})

	writeZone(t, root, "thermal_zone0", "cpu-thermal", "52000")
	SetSupplementalThermalSource(func() []ThermalZone {
		return []ThermalZone{
			{Name: "go2/motor/front-right-thigh", TempC: 67},
			{Name: "go2/imu", TempC: 61},
		}
	})

	zones := ResolveThermal()
	if len(zones) != 3 {
		t.Fatalf("ResolveThermal() returned %d zones, want 3: %+v", len(zones), zones)
	}
	if zones[0].Name != "go2/motor/front-right-thigh" || zones[1].Name != "go2/imu" || zones[2].Name != "cpu-thermal" {
		t.Fatalf("ResolveThermal() not sorted hottest-first: %+v", zones)
	}
}

func TestResolveThermalWithoutSupplementalSourcePreservesSysfs(t *testing.T) {
	root := t.TempDir()
	oldRoot := thermalRoot
	thermalRoot = root
	SetSupplementalThermalSource(nil)
	t.Cleanup(func() {
		thermalRoot = oldRoot
		SetSupplementalThermalSource(nil)
	})

	writeZone(t, root, "thermal_zone0", "soc-thermal", "49000")
	zones := ResolveThermal()
	if len(zones) != 1 || zones[0].Name != "soc-thermal" || zones[0].TempC != 49 {
		t.Fatalf("ResolveThermal() = %+v, want the existing sysfs zone", zones)
	}
}
