package services

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

func TestROS2AdapterRecordsAndMovesEachRMWBag(t *testing.T) {
	bagRoot := t.TempDir()
	episodeRoot := t.TempDir()
	runtime := &fakeROS2Runtime{sidecar: ROS2Sidecar{Name: "sc-cyc", RMW: "rmw_cyclonedds_cpp", DomainID: 7}}
	runtime.execFn = func(ctx context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
		if strings.Join(opts.Args, " ") == "topic list" {
			_, _ = io.WriteString(stdout, "/chatter\n")
			return 0, nil
		}
		if len(opts.Args) >= 5 && opts.Args[0] == "bag" && opts.Args[1] == "record" {
			output := opts.Args[3]
			if err := os.MkdirAll(output, 0o750); err != nil {
				return 1, err
			}
			if err := os.WriteFile(filepath.Join(output, "metadata.yaml"), []byte("rosbag2_bagfile_information:\n"), 0o640); err != nil {
				return 1, err
			}
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return 1, nil
	}
	service := NewROS2Service(nil, runtime, bagRoot)
	adapter := newROS2DataAdapter(service)
	source := ros2DataSource(ros2SC{name: "sc-cyc", rmw: "rmw_cyclonedds_cpp", domainID: 7})
	capture, err := adapter.Start(context.Background(), data.CaptureSession{ID: "episode-test", Directory: episodeRoot}, []data.Source{source})
	if err != nil {
		t.Fatal(err)
	}
	results, err := capture.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SourceID != source.ID || results[0].DropAccounting != "unavailable" {
		t.Fatalf("results = %+v", results)
	}
	metadata := filepath.Join(episodeRoot, "ros2", safeCaptureName(source.ID), "metadata.yaml")
	if _, err := os.Stat(metadata); err != nil {
		t.Fatalf("moved bag missing: %v", err)
	}
}
