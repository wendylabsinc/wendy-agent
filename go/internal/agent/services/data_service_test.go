package services

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type fakeDataAdapter struct {
	started bool
	stopped bool
}

func (f *fakeDataAdapter) Discover(context.Context) []data.Source {
	return []data.Source{{ID: "fake:camera", Kind: "camera", ClockDomain: "FAKE_NATIVE", Healthy: true}}
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
