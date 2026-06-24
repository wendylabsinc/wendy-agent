package appconfig

import "testing"

func TestROS2AutoDomainID_StableAndInRange(t *testing.T) {
	first := ROS2AutoDomainID("com.example.robot")
	second := ROS2AutoDomainID("com.example.robot")
	if first != second {
		t.Errorf("auto domain ID not stable: %d vs %d", first, second)
	}
	for _, appID := range []string{"", "a", "com.example.robot", "com.other.app", "x.y.z"} {
		id := ROS2AutoDomainID(appID)
		if id < ROS2DomainIDMin || id > ROS2DomainIDMax {
			t.Errorf("ROS2AutoDomainID(%q) = %d, want in [%d,%d]", appID, id, ROS2DomainIDMin, ROS2DomainIDMax)
		}
	}
}

func TestROS2Config_ResolvedDomainID(t *testing.T) {
	explicit := 42
	if got := (&ROS2Config{DomainID: &explicit}).ResolvedDomainID("app"); got != 42 {
		t.Errorf("explicit domain ID = %d, want 42", got)
	}
	// Newly valid band (0–232): values above the old 101 cap resolve to themselves.
	for _, valid := range []int{102, 150, 232} {
		v := valid
		if got := (&ROS2Config{DomainID: &v}).ResolvedDomainID("app"); got != valid {
			t.Errorf("domain ID %d = %d, want %d", valid, got, valid)
		}
	}
	invalid := 233
	if got := (&ROS2Config{DomainID: &invalid}).ResolvedDomainID("app"); got != -1 {
		t.Errorf("out-of-range domain ID = %d, want -1", got)
	}
	negative := -1
	if got := (&ROS2Config{DomainID: &negative}).ResolvedDomainID("app"); got != -1 {
		t.Errorf("negative domain ID = %d, want -1", got)
	}
	if got := (&ROS2Config{}).ResolvedDomainID("app"); got != ROS2AutoDomainID("app") {
		t.Errorf("nil domain ID = %d, want auto %d", got, ROS2AutoDomainID("app"))
	}
}

func TestROS2Config_ResolvedRMW(t *testing.T) {
	cases := map[string]string{
		"":                  "rmw_cyclonedds_cpp",
		"cyclonedds":        "rmw_cyclonedds_cpp",
		"CycloneDDS":        "rmw_cyclonedds_cpp",
		"fastrtps":          "rmw_fastrtps_cpp",
		"fastdds":           "rmw_fastrtps_cpp",
		"rmw_connextdds":    "rmw_connextdds",
		"gurumdds":          "rmw_gurumdds_cpp",
		"evil; rm -rf /":    "",
		"rmw_unknown_thing": "",
	}
	for in, want := range cases {
		if got := (&ROS2Config{RMW: in}).ResolvedRMW(); got != want {
			t.Errorf("ResolvedRMW(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestROS2Config_ResolvedDistro(t *testing.T) {
	if got := (&ROS2Config{}).ResolvedDistro(); got != "humble" {
		t.Errorf("default distro = %q, want humble", got)
	}
	if got := (&ROS2Config{Distro: "Jazzy"}).ResolvedDistro(); got != "jazzy" {
		t.Errorf("distro = %q, want jazzy", got)
	}
}

func TestResolveROS2ConfigForService(t *testing.T) {
	groupID, svcID := 1, 7
	group := &ROS2Config{DomainID: &groupID}
	svc := &ROS2Config{DomainID: &svcID}
	cfg := &AppConfig{
		Frameworks: &FrameworksConfig{ROS2: group},
		Services: map[string]*ServiceConfig{
			"detector": {Frameworks: &FrameworksConfig{ROS2: svc}},
			"camera":   {},
		},
	}
	if got := cfg.ResolveROS2ConfigForService("detector"); got != svc {
		t.Errorf("detector should use service-level config")
	}
	if got := cfg.ResolveROS2ConfigForService("camera"); got != group {
		t.Errorf("camera should inherit group-level config")
	}
	if got := cfg.ResolveROS2ConfigForService(""); got != group {
		t.Errorf("single-container app should use group-level config")
	}
	if got := (&AppConfig{}).ResolveROS2ConfigForService("x"); got != nil {
		t.Errorf("no frameworks should resolve to nil, got %+v", got)
	}
}

func TestROS2Annotation_RoundTrip(t *testing.T) {
	explicit := 42
	value := ROS2AnnotationValue(&ROS2Config{DomainID: &explicit, Distro: "jazzy"}, "com.example.app")
	if value != "distro=jazzy,domain_id=42" {
		t.Errorf("annotation value = %q, want distro=jazzy,domain_id=42", value)
	}
	distro, domainID, ok := ParseROS2Annotation(value)
	if !ok || distro != "jazzy" || domainID != 42 {
		t.Errorf("ParseROS2Annotation(%q) = (%q, %d, %v)", value, distro, domainID, ok)
	}
}

func TestROS2AnnotationValue_Defaults(t *testing.T) {
	value := ROS2AnnotationValue(&ROS2Config{}, "com.example.app")
	distro, domainID, ok := ParseROS2Annotation(value)
	if !ok || distro != "humble" || domainID != ROS2AutoDomainID("com.example.app") {
		t.Errorf("default annotation = %q parsed to (%q, %d, %v)", value, distro, domainID, ok)
	}
}

func TestROS2AnnotationValue_InvalidDomain(t *testing.T) {
	invalid := 999
	if got := ROS2AnnotationValue(&ROS2Config{DomainID: &invalid}, "app"); got != "" {
		t.Errorf("annotation for invalid domain = %q, want empty", got)
	}
}

func TestParseROS2Annotation_Malformed(t *testing.T) {
	for _, value := range []string{"", "distro=humble", "domain_id=5", "distro=humble,domain_id=999", "garbage"} {
		if _, _, ok := ParseROS2Annotation(value); ok {
			t.Errorf("ParseROS2Annotation(%q) ok = true, want false", value)
		}
	}
}

func TestIsValidRMWImplementation(t *testing.T) {
	valid := []string{"rmw_cyclonedds_cpp", "rmw_fastrtps_cpp", "rmw_connextdds", "rmw_gurumdds_cpp"}
	for _, s := range valid {
		if !IsValidRMWImplementation(s) {
			t.Errorf("IsValidRMWImplementation(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "cyclonedds", "fastrtps", "rmw_fastrtps", "rmw_evil_cpp; rm -rf", "RMW_FASTRTPS_CPP"}
	for _, s := range invalid {
		if IsValidRMWImplementation(s) {
			t.Errorf("IsValidRMWImplementation(%q) = true, want false", s)
		}
	}
}
