package data

import (
	"strings"
	"testing"
)

// A camera source no longer deploy-warns that its pre-roll starts at the
// trigger: camera pre-roll is honored. Audio and ROS 2 sources still do.
func TestBufferWarningExcludesCameraButNotROS2(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cameraOnly := []byte(`version: 1
name: camera-only
fleet: lab
sources:
  - camera: front
capture:
  buffer: 10s
  after_trigger: 20s
  triggers:
    - event: emergency_stop
upload:
  when: wifi
  destination: episodes
export:
  annotation: cvat
`)
	campaign, err := manager.DeployCampaign(cameraOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range campaign.Warnings {
		if strings.Contains(warning, "start at the trigger") {
			t.Fatalf("camera-only campaign still warns that its stream starts at the trigger: %q", warning)
		}
	}

	withROS2 := []byte(`version: 1
name: with-ros2
fleet: lab
sources:
  - camera: front
  - ros2: /lidar/points
capture:
  buffer: 10s
  after_trigger: 20s
  triggers:
    - event: emergency_stop
upload:
  when: wifi
  destination: episodes
export:
  annotation: cvat
`)
	campaign, err = manager.DeployCampaign(withROS2)
	if err != nil {
		t.Fatal(err)
	}
	var warned bool
	for _, warning := range campaign.Warnings {
		if strings.Contains(warning, "audio and ROS 2 streams start at the trigger") {
			warned = true
			if strings.Contains(warning, "camera streams start at the trigger") {
				t.Fatalf("warning wrongly claims camera starts at the trigger: %q", warning)
			}
		}
	}
	if !warned {
		t.Fatalf("a buffered campaign with a ROS 2 source did not warn about trigger-start streams: %v", campaign.Warnings)
	}
}
