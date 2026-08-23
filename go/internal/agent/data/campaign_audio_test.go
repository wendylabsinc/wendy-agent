package data

import (
	"os"
	"strings"
	"testing"
)

const audioThresholdCampaign = `version: 1
name: cabin-audio
sources:
  - audio: cabin-mic
    capture:
      mode: threshold
      trigger: "level_db > -20"
      fragment: 30s
capture:
  buffer: 0s
  after_trigger: 20s
  triggers:
    - event: loud_event
upload:
  when: wifi
export:
  annotation: cvat
`

// TestAudioThresholdCampaignParsesAndDeploysWithoutModeWarning proves the audio
// source kind and threshold capture mode are first-class: the plan parses and
// deployment does not warn that the mode is unimplemented for that source.
func TestAudioThresholdCampaignParsesAndDeploysWithoutModeWarning(t *testing.T) {
	campaign, err := ParseCampaign([]byte(audioThresholdCampaign))
	if err != nil {
		t.Fatalf("audio threshold campaign did not parse: %v", err)
	}
	if len(campaign.Sources) != 1 || campaign.Sources[0].Audio != "cabin-mic" {
		t.Fatalf("expected one audio source, got %+v", campaign.Sources)
	}
	if campaign.Sources[0].kind() != "audio" {
		t.Fatalf("source kind = %q, want audio", campaign.Sources[0].kind())
	}
	if campaign.Sources[0].Capture.EffectiveMode() != "threshold" {
		t.Fatalf("capture mode = %q, want threshold", campaign.Sources[0].Capture.EffectiveMode())
	}

	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := manager.DeployCampaign([]byte(audioThresholdCampaign))
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for _, w := range deployed.Warnings {
		if strings.Contains(w, "not implemented yet for their source kind") {
			t.Fatalf("audio threshold should be implemented, but deploy warned: %q", w)
		}
	}
}

// TestShippedAudioCampaignParses guards the real
// Examples/WendyDataCampaign/campaign.yaml, which now enables an audio
// threshold source, against the schema this agent parses.
func TestShippedAudioCampaignParses(t *testing.T) {
	raw, err := os.ReadFile("../../../../Examples/WendyDataCampaign/campaign.yaml")
	if err != nil {
		t.Fatalf("reading campaign example: %v", err)
	}
	campaign, err := ParseCampaign(raw)
	if err != nil {
		t.Fatalf("WendyDataCampaign/campaign.yaml does not parse: %v", err)
	}
	var audio *CampaignSource
	for i := range campaign.Sources {
		if campaign.Sources[i].Audio != "" {
			audio = &campaign.Sources[i]
			break
		}
	}
	if audio == nil {
		t.Fatal("expected an enabled audio source in the shipped campaign")
	}
	if audio.Capture == nil || audio.Capture.EffectiveMode() != "threshold" {
		t.Fatalf("audio source is not in threshold mode: %+v", audio.Capture)
	}
	if _, _, _, err := ParseFieldThreshold(audio.Capture.Trigger); err != nil {
		t.Fatalf("audio trigger %q does not parse: %v", audio.Capture.Trigger, err)
	}
}
