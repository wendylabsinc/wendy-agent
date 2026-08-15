package services

import (
	"context"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type fakeDataAdapter struct {
	started bool
	stopped bool
}

func (f *fakeDataAdapter) Discover(context.Context) []data.Source {
	return []data.Source{{ID: "fake:camera", Kind: "camera", ClockDomain: "FAKE_NATIVE", Healthy: true, Detail: "front test camera"}}
}

func TestCampaignDeployTriggerAndTimedFinalization(t *testing.T) {
	manager, err := data.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewDataService(manager)
	adapter := &fakeDataAdapter{}
	service.addAdapter(adapter)
	yaml := []byte(`name: test-flight
fleet: test-lab
sources:
  - camera: front
capture:
  buffer: 10ms
  after_trigger: 30ms
  triggers:
    - event: emergency_stop
upload:
  when: wifi
  destination: s3://example/episodes
export:
  annotation: cvat
`)
	campaign, err := service.CampaignDeploy(context.Background(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: yaml})
	if err != nil {
		t.Fatal(err)
	}
	if campaign.GetState() != "armed" || campaign.GetRevision() == "" {
		t.Fatalf("bad campaign response: %+v", campaign)
	}
	episode, err := service.CampaignTrigger(context.Background(), &agentpbv2.DataCampaignTriggerRequest{Name: "test-flight", Reason: "hardware_test"})
	if err != nil {
		t.Fatal(err)
	}
	if !adapter.started {
		t.Fatal("campaign did not start camera adapter")
	}
	deadline := time.Now().Add(2 * time.Second)
	for manager.Status() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.Status() != nil || !adapter.stopped {
		t.Fatal("campaign did not stop after after_trigger")
	}
	manifest, failures, err := manager.Inspect(episode.GetId(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("verification failures: %v", failures)
	}
	if manifest.Trigger.CampaignName != "test-flight" || manifest.Trigger.Reason != "hardware_test" || manifest.Upload.Destination != "s3://example/episodes" || manifest.Labeling.Destination != "cvat" {
		t.Fatalf("campaign metadata missing from episode: %+v", manifest)
	}
	if manifest.CollectorVersion == "" || manifest.Device.ID == "" {
		t.Fatalf("episode identity/version missing: %+v", manifest)
	}
	for _, source := range manifest.Sources {
		if source.Source.ID == "fake:camera" && source.RequestedOffset != -(10*time.Millisecond).Nanoseconds() {
			t.Fatalf("camera requested offset = %d", source.RequestedOffset)
		}
	}
}

func TestCampaignApplicationEventAutomaticallyTriggers(t *testing.T) {
	manager, err := data.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewDataService(manager)
	yaml := []byte(`name: event-flight
sources:
  - telemetry: true
capture:
  buffer: 1s
  after_trigger: 30ms
  triggers:
    - event: emergency_stop
upload: {when: wifi, destination: s3://example/episodes}
export: {annotation: cvat}
`)
	if _, err = service.CampaignDeploy(context.Background(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: yaml}); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.RecordApplication("test.app", data.ApplicationRecord{Version: 1, Type: "event", Name: "emergency_stop"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, listErr := manager.List()
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(list) == 1 {
			manifest, _, inspectErr := manager.Inspect(list[0].ID, false)
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			if manifest.Trigger.Reason != "event:emergency_stop" {
				t.Fatalf("wrong trigger: %+v", manifest.Trigger)
			}
			for _, source := range manifest.Sources {
				if source.Source.ID == "applications" && (source.Count != 1 || source.ActualOffset >= 0 || source.RequestedOffset != -time.Second.Nanoseconds()) {
					t.Fatalf("application pre-roll accounting is not exact: %+v", source)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("application event did not produce a finalized episode")
}
func (f *fakeDataAdapter) Start(_ context.Context, _ data.CaptureSession, selected []data.Source) (runningDataCapture, error) {
	for _, source := range selected {
		if source.ID == "fake:camera" {
			f.started = true
			return f, nil
		}
	}
	return nil, nil
}
func (f *fakeDataAdapter) Stop(context.Context) ([]data.CaptureResult, error) {
	f.stopped = true
	drops, mapping := uint64(3), int64(42)
	return []data.CaptureResult{{SourceID: "fake:camera", Count: 9, Drops: &drops, DropAccounting: "exact", MappingError: &mapping}}, nil
}

func TestDataServiceRunsAdaptersAndSealsResults(t *testing.T) {
	manager, err := data.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewDataService(manager)
	adapter := &fakeDataAdapter{}
	service.addAdapter(adapter)
	sources, err := service.Sources(context.Background(), &agentpbv2.DataSourcesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources.GetSources()) != 3 {
		t.Fatalf("sources = %d, want 3", len(sources.GetSources()))
	}
	started, err := service.Start(context.Background(), &agentpbv2.DataStartRequest{Sources: []string{"fake:camera"}})
	if err != nil {
		t.Fatal(err)
	}
	if !adapter.started {
		t.Fatal("adapter was not started")
	}
	stopped, err := service.Stop(context.Background(), &agentpbv2.DataStopRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !adapter.stopped || stopped.GetId() != started.GetId() {
		t.Fatalf("bad stop: %+v", stopped)
	}
	manifest, failures, err := manager.Inspect(stopped.GetId(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("verification failures: %v", failures)
	}
	if len(manifest.Sources) != 1 || manifest.Sources[0].Count != 9 || manifest.Sources[0].Drops == nil || *manifest.Sources[0].Drops != 3 {
		t.Fatalf("adapter results not sealed: %+v", manifest.Sources)
	}
}
