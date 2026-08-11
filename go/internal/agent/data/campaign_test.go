package data

import (
	"context"
	"testing"
)

const exampleCampaignYAML = `name: forklift-failures
fleet: warehouse-west
sources:
  - camera: front
  - ros2: /lidar/points
  - ros2: /vehicle/odometry
capture:
  buffer: 10s
  after_trigger: 20s
  triggers:
    - event: emergency_stop
    - model.uncertainty: "> 0.65"
upload:
  when: wifi
  destination: s3://acme-ml/forklift
export:
  annotation: cvat
models:
  detector: 4.2.0
privacy:
  - name: blur-faces
    revision: "3"
`

func TestParseCampaignExampleAndMatchTriggers(t *testing.T) {
	campaign, err := ParseCampaign([]byte(exampleCampaignYAML))
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Name != "forklift-failures" || campaign.State != "armed" || len(campaign.Revision) != 64 {
		t.Fatalf("bad campaign: %+v", campaign)
	}
	if reason, _, matched := campaign.Match(ApplicationRecord{Type: "event", Name: "emergency_stop"}); !matched || reason != "event:emergency_stop" {
		t.Fatalf("event did not match: %q %v", reason, matched)
	}
	if reason, _, matched := campaign.Match(ApplicationRecord{Type: "prediction", Model: "detector", Attributes: map[string]any{"uncertainty": 0.7}}); !matched || reason == "" {
		t.Fatalf("uncertainty did not match: %q %v", reason, matched)
	}
	if _, _, matched := campaign.Match(ApplicationRecord{Type: "prediction", Model: "detector", Value: 0.4}); matched {
		t.Fatal("low uncertainty unexpectedly matched")
	}
}

func TestParseCampaignRejectsUnknownFieldsAndBadThresholds(t *testing.T) {
	badField := exampleCampaignYAML + "mystery: true\n"
	if _, err := ParseCampaign([]byte(badField)); err == nil {
		t.Fatal("unknown field was accepted")
	}
	badThreshold := []byte(`name: bad
sources: [{telemetry: true}]
capture:
  buffer: 1s
  after_trigger: 1s
  triggers: [{model.uncertainty: "> 2"}]
upload: {when: wifi, destination: s3://bucket/path}
export: {annotation: cvat}
`)
	if _, err := ParseCampaign(badThreshold); err == nil {
		t.Fatal("out-of-range threshold was accepted")
	}
}

func TestCampaignPersistsAndResolvesSemanticSources(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager.SetSourceProvider(func(context.Context) []Source {
		return []Source{
			{ID: "v4l2:/dev/video2", Kind: "camera", ClockDomain: "V4L2", Healthy: true, Detail: "Logitech Brio"},
			{ID: "ros2:cyclone:domain-0", Kind: "ros2", ClockDomain: "ROS", Healthy: true},
		}
	})
	campaign, err := manager.DeployCampaign([]byte(exampleCampaignYAML))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := manager.Campaign(campaign.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != campaign.Revision || stored.DeployedUnixNanos == 0 {
		t.Fatalf("campaign was not durably stored: %+v", stored)
	}
	sources, topics, err := manager.ResolveCampaignSources(stored)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := map[string]bool{"applications": true, "ros2:cyclone:domain-0": true, "v4l2:/dev/video2": true}
	for _, source := range sources {
		delete(wantSources, source)
	}
	if len(wantSources) != 0 || len(topics) != 2 {
		t.Fatalf("sources=%v topics=%v missing=%v", sources, topics, wantSources)
	}
}
