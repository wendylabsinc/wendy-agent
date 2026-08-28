package rosbattery

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/rtps"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An absent config is the normal case and must yield working defaults, not an
// error and not a disabled monitor.
func TestLoadConfig_AbsentFileIsDefaults(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("absent config must not error: %v", err)
	}
	if !cfg.Enabled {
		t.Error("default must be enabled")
	}
	if cfg.DomainID != 0 {
		t.Errorf("DomainID = %d; want 0", cfg.DomainID)
	}
}

func TestLoadConfig_Overrides(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{
	  "interfaces": ["enP8p1s0"],
	  "domainId": 7,
	  "topic": "/lf/lowstate",
	  "type": "unitree_go::msg::dds_::LowState_"
	}`)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled {
		t.Error("Enabled should stay true when the key is absent")
	}
	if len(cfg.Interfaces) != 1 || cfg.Interfaces[0] != "enP8p1s0" {
		t.Errorf("Interfaces = %v", cfg.Interfaces)
	}
	if cfg.DomainID != 7 {
		t.Errorf("DomainID = %d; want 7", cfg.DomainID)
	}
	if cfg.Topic != "/lf/lowstate" {
		t.Errorf("Topic = %q", cfg.Topic)
	}
}

// An explicit false must be distinguishable from an absent key, which is why
// the on-disk field is a pointer.
func TestLoadConfig_ExplicitDisable(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"enabled": false}`)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Error("enabled:false must disable the monitor")
	}
}

// A config someone deliberately wrote and got wrong should say so rather than
// be silently ignored — but callers still get usable defaults back.
func TestLoadConfig_MalformedErrorsButReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"domainId": `)

	cfg, err := LoadConfig(dir)
	if err == nil {
		t.Error("expected an error for malformed JSON")
	}
	if !cfg.Enabled {
		t.Error("a malformed config must still yield usable defaults")
	}
}

func ep(topic, typeName string) rtps.Endpoint {
	return rtps.Endpoint{Topic: topic, Type: typeName}
}

func TestPickBatteryTopic_PrefersBatteryState(t *testing.T) {
	found := map[string]rtps.Endpoint{
		"rt/lf/lowstate":  ep("rt/lf/lowstate", "unitree_go::msg::dds_::LowState_"),
		"rt/battery_stat": ep("rt/battery_stat", "sensor_msgs::msg::dds_::BatteryState_"),
	}
	got, ok := PickBatteryTopic(found, "", "")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Topic != "rt/battery_stat" {
		t.Errorf("picked %q; want the standard BatteryState", got.Topic)
	}
}

// /lowstate is the high-rate control topic; /lf/lowstate carries the same type
// at a fraction of the traffic.
func TestPickBatteryTopic_PrefersLowFrequencyLowState(t *testing.T) {
	found := map[string]rtps.Endpoint{
		"rt/lowstate":    ep("rt/lowstate", "unitree_go::msg::dds_::LowState_"),
		"rt/lf/lowstate": ep("rt/lf/lowstate", "unitree_go::msg::dds_::LowState_"),
	}
	got, ok := PickBatteryTopic(found, "", "")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Topic != "rt/lf/lowstate" {
		t.Errorf("picked %q; want rt/lf/lowstate", got.Topic)
	}
}

func TestPickBatteryTopic_FallsBackToPlainLowState(t *testing.T) {
	found := map[string]rtps.Endpoint{
		"rt/lowstate": ep("rt/lowstate", "unitree_go::msg::dds_::LowState_"),
	}
	got, ok := PickBatteryTopic(found, "", "")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Topic != "rt/lowstate" {
		t.Errorf("picked %q", got.Topic)
	}
}

// ROS 2 mangles topic names on the DDS wire by prefixing "rt/", so a config
// written in ROS spelling must still match.
func TestPickBatteryTopic_ConfiguredTopicMatchesMangledName(t *testing.T) {
	found := map[string]rtps.Endpoint{
		"rt/lf/lowstate": ep("rt/lf/lowstate", "unitree_go::msg::dds_::LowState_"),
	}
	got, ok := PickBatteryTopic(found, "/lf/lowstate", "")
	if !ok {
		t.Fatal("expected the ROS-spelled topic to match its DDS name")
	}
	if got.Topic != "rt/lf/lowstate" {
		t.Errorf("picked %q", got.Topic)
	}
}

func TestPickBatteryTopic_ConfiguredTopicMissing(t *testing.T) {
	found := map[string]rtps.Endpoint{
		"rt/lf/lowstate": ep("rt/lf/lowstate", "unitree_go::msg::dds_::LowState_"),
	}
	if _, ok := PickBatteryTopic(found, "/nope", ""); ok {
		t.Error("a configured topic that is absent must not silently fall back")
	}
}

func TestPickBatteryTopic_ConfiguredTypeNarrows(t *testing.T) {
	found := map[string]rtps.Endpoint{
		"rt/battery_stat": ep("rt/battery_stat", "sensor_msgs::msg::dds_::BatteryState_"),
		"rt/lf/lowstate":  ep("rt/lf/lowstate", "unitree_go::msg::dds_::LowState_"),
	}
	got, ok := PickBatteryTopic(found, "", "LowState")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Topic != "rt/lf/lowstate" {
		t.Errorf("picked %q; a pinned type must override the BatteryState preference", got.Topic)
	}
}

func TestPickBatteryTopic_NoBatteryTopic(t *testing.T) {
	found := map[string]rtps.Endpoint{
		"rt/utlidar/cloud": ep("rt/utlidar/cloud", "sensor_msgs::msg::dds_::PointCloud2_"),
	}
	if _, ok := PickBatteryTopic(found, "", ""); ok {
		t.Error("expected no match on a domain with no battery topic")
	}
}

func TestDecoderFor(t *testing.T) {
	if decoderFor("sensor_msgs::msg::dds_::BatteryState_") == nil {
		t.Error("BatteryState should have a decoder")
	}
	if decoderFor("unitree_go::msg::dds_::LowState_") == nil {
		t.Error("LowState should have a decoder")
	}
	if decoderFor("sensor_msgs::msg::dds_::PointCloud2_") != nil {
		t.Error("PointCloud2 should have no decoder")
	}
}
