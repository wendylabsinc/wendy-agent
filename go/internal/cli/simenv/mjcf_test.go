package simenv

import (
	"encoding/xml"
	"strings"
	"testing"
)

// parseXML proves the rendered scene is well-formed by decoding every token.
func parseXML(t *testing.T, b []byte) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("scene.xml is not well-formed XML: %v\n---\n%s", err, b)
		}
	}
}

func TestRenderMJCFWellFormed(t *testing.T) {
	env := Env{
		SchemaVersion: SchemaVersion,
		Campaign:      "demo",
		Profile:       ProfileCustom,
		Observations: []Observation{
			{Name: "camera_front", Kind: "image", Source: "v4l2:/dev/video0", Dims: []int{720, 1280, 3}, PixelFormat: "yuyv422"},
			{Name: "ros2_imu_data", Kind: "vector", Source: "ros2:cyclone:domain-0:/imu/data", Dims: []int{10}},
			{Name: "ros2_joint_states", Kind: "vector", Source: "ros2:cyclone:domain-0:/joint_states"},
		},
		Action: &Action{
			Stream:   "/cmd_vel",
			Channels: []string{"linear.x", "linear.y", "angular.z"},
			Low:      []float64{-0.8, -0.5, -1},
			High:     []float64{0.8, 0.5, 1},
		},
	}
	out, err := RenderMJCF(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parseXML(t, out)

	s := string(out)
	for _, want := range []string{
		`<camera name="camera_front"`,
		`ctrlrange="-0.8 0.8"`,
		`ctrllimited="true"`,
		`PLACEHOLDER custom body`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("scene.xml missing %q\n---\n%s", want, s)
		}
	}
	// One motor per action channel.
	if got := strings.Count(s, "<motor "); got != 3 {
		t.Fatalf("motor count = %d, want 3", got)
	}
}

func TestRenderMJCFKnownProfileDiffersFromCustom(t *testing.T) {
	base := Env{SchemaVersion: SchemaVersion, Campaign: "demo"}

	base.Profile = ProfileCustom
	customOut, err := RenderMJCF(base)
	if err != nil {
		t.Fatalf("custom render: %v", err)
	}
	parseXML(t, customOut)

	base.Profile = "go2"
	knownOut, err := RenderMJCF(base)
	if err != nil {
		t.Fatalf("known render: %v", err)
	}
	parseXML(t, knownOut)

	custom, known := string(customOut), string(knownOut)
	if !strings.Contains(custom, "PLACEHOLDER custom body") {
		t.Fatalf("custom scene should carry a placeholder body:\n%s", custom)
	}
	if strings.Contains(custom, "<include") {
		t.Fatalf("custom scene should not include a model:\n%s", custom)
	}
	if !strings.Contains(known, `<include file="go2/scene.xml"`) {
		t.Fatalf("known scene should include the go2 model:\n%s", known)
	}
	if strings.Contains(known, "PLACEHOLDER custom body") {
		t.Fatalf("known scene should not carry a placeholder body:\n%s", known)
	}
}

func TestRenderMJCFEmpty(t *testing.T) {
	out, err := RenderMJCF(Env{SchemaVersion: SchemaVersion, Campaign: "bare", Profile: ProfileCustom})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parseXML(t, out)
	s := string(out)
	if strings.Contains(s, "<sensor>") {
		t.Fatalf("empty scene should have no sensor block:\n%s", s)
	}
	if strings.Contains(s, "<actuator>") {
		t.Fatalf("empty scene should have no actuator block:\n%s", s)
	}
}
