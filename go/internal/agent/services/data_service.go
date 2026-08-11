package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type DataService struct {
	agentpbv2.UnimplementedDataServiceServer
	manager        *data.Manager
	adapterMu      sync.RWMutex
	adapters       []dataCaptureAdapter
	captureMu      sync.Mutex
	activeCaptures []runningDataCapture
}

type dataCaptureAdapter interface {
	Discover(context.Context) []data.Source
	Start(context.Context, data.CaptureSession, []data.Source) (runningDataCapture, error)
}

type runningDataCapture interface {
	Stop(context.Context) ([]data.CaptureResult, error)
}

func NewDataService(m *data.Manager) *DataService {
	s := &DataService{manager: m}
	m.SetSourceProvider(s.discoverAdapterSources)
	return s
}

func (s *DataService) addAdapter(adapter dataCaptureAdapter) {
	if adapter == nil {
		return
	}
	s.adapterMu.Lock()
	s.adapters = append(s.adapters, adapter)
	s.adapterMu.Unlock()
}

// SetVideoService enables local V4L2/CSI and registered IP-camera capture.
func (s *DataService) SetVideoService(video *VideoService) {
	s.addAdapter(newCameraDataAdapter(video))
}

// SetROS2Service enables one managed rosbag2 recorder per live RMW graph.
func (s *DataService) SetROS2Service(ros2 *ROS2Service) {
	if ros2 != nil {
		s.addAdapter(newROS2DataAdapter(ros2))
	}
}

func (s *DataService) adapterSnapshot() []dataCaptureAdapter {
	s.adapterMu.RLock()
	defer s.adapterMu.RUnlock()
	return append([]dataCaptureAdapter(nil), s.adapters...)
}

func (s *DataService) discoverAdapterSources(ctx context.Context) []data.Source {
	var out []data.Source
	for _, adapter := range s.adapterSnapshot() {
		out = append(out, adapter.Discover(ctx)...)
	}
	return out
}

func (s *DataService) Sources(ctx context.Context, _ *agentpbv2.DataSourcesRequest) (*agentpbv2.DataSourcesResponse, error) {
	r := &agentpbv2.DataSourcesResponse{}
	for _, source := range s.manager.Sources(ctx) {
		r.Sources = append(r.Sources, &agentpbv2.DataSource{Id: source.ID, Kind: source.Kind, ClockDomain: source.ClockDomain, Healthy: source.Healthy, Detail: source.Detail})
	}
	return r, nil
}

func (s *DataService) Start(ctx context.Context, req *agentpbv2.DataStartRequest) (*agentpbv2.DataEpisode, error) {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	cal := map[string][]byte{}
	for _, c := range req.GetCalibrations() {
		cal[c.GetSource()] = c.GetContents()
	}
	m, err := s.manager.Start(data.StartOptions{Name: req.GetName(), Sources: req.GetSources(), ExcludeSources: req.GetExcludeSources(), RequireUTCUncertainty: time.Duration(req.GetRequireUtcUncertaintyNanos()), Calibrations: cal})
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	session, ok := s.manager.ActiveSession()
	if !ok {
		return nil, status.Error(codes.Internal, "episode session disappeared during capture startup")
	}
	selected := make([]data.Source, 0, len(m.Sources))
	for _, stats := range m.Sources {
		selected = append(selected, stats.Source)
	}
	var captures []runningDataCapture
	for _, adapter := range s.adapterSnapshot() {
		capture, startErr := adapter.Start(ctx, session, selected)
		if startErr == nil {
			if capture != nil {
				captures = append(captures, capture)
			}
			continue
		}
		var results []data.CaptureResult
		for i := len(captures) - 1; i >= 0; i-- {
			r, _ := captures[i].Stop(context.Background())
			results = append(results, r...)
		}
		_ = s.manager.ApplyCaptureResults(results)
		_, _ = s.manager.Interrupt("capture_adapter_start_failed")
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("starting capture adapter: %v", startErr))
	}
	s.activeCaptures = captures
	return manifestEpisode(m), nil
}

func (s *DataService) Stop(ctx context.Context, _ *agentpbv2.DataStopRequest) (*agentpbv2.DataEpisode, error) {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	var results []data.CaptureResult
	var captureErrs []error
	for i := len(s.activeCaptures) - 1; i >= 0; i-- {
		r, err := s.activeCaptures[i].Stop(ctx)
		results = append(results, r...)
		if err != nil {
			captureErrs = append(captureErrs, err)
		}
	}
	s.activeCaptures = nil
	if len(results) > 0 {
		_ = s.manager.ApplyCaptureResults(results)
	}
	var m data.Manifest
	var err error
	if len(captureErrs) > 0 {
		m, err = s.manager.Interrupt("capture_adapter_failed")
	} else {
		m, err = s.manager.Stop()
	}
	if errors.Is(err, data.ErrNoActiveEpisode) {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return manifestEpisode(m), nil
}

func (s *DataService) Status(context.Context, *agentpbv2.DataStatusRequest) (*agentpbv2.DataStatusResponse, error) {
	m := s.manager.Status()
	if m == nil {
		return &agentpbv2.DataStatusResponse{}, nil
	}
	return &agentpbv2.DataStatusResponse{Active: manifestEpisode(*m)}, nil
}

func (s *DataService) Episodes(context.Context, *agentpbv2.DataEpisodesRequest) (*agentpbv2.DataEpisodesResponse, error) {
	list, err := s.manager.List()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	r := &agentpbv2.DataEpisodesResponse{}
	for _, e := range list {
		r.Episodes = append(r.Episodes, infoEpisode(e))
	}
	return r, nil
}

func (s *DataService) Inspect(_ context.Context, req *agentpbv2.DataInspectRequest) (*agentpbv2.DataInspectResponse, error) {
	m, failures, err := s.manager.Inspect(req.GetEpisode(), req.GetVerify())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &agentpbv2.DataInspectResponse{ManifestJson: append(b, '\n'), VerificationErrors: failures}, nil
}

func (s *DataService) Download(req *agentpbv2.DataDownloadRequest, stream agentpbv2.DataService_DownloadServer) error {
	s.manager.BeginDownload(req.GetEpisode())
	defer s.manager.EndDownload(req.GetEpisode())
	f, meta, err := s.manager.OpenFile(req.GetEpisode(), req.GetPath(), req.GetOffset())
	if err != nil {
		return status.Error(codes.NotFound, err.Error())
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	offset := req.GetOffset()
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			if err := stream.Send(&agentpbv2.DataDownloadChunk{Path: meta.Path, Offset: offset, Data: buf[:n], Size: meta.Size, Sha256: meta.SHA256}); err != nil {
				return err
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			return stream.Send(&agentpbv2.DataDownloadChunk{Path: meta.Path, Offset: offset, Size: meta.Size, Sha256: meta.SHA256, Eof: true})
		}
		if readErr != nil {
			return status.Error(codes.Internal, readErr.Error())
		}
	}
}

func manifestEpisode(m data.Manifest) *agentpbv2.DataEpisode {
	return &agentpbv2.DataEpisode{Id: m.ID, Name: m.Name, State: m.State, StartedUnixNanos: m.StartedUnixNanos, BootId: m.BootID}
}
func infoEpisode(e data.EpisodeInfo) *agentpbv2.DataEpisode {
	return &agentpbv2.DataEpisode{Id: e.ID, Name: e.Name, State: e.State, StartedUnixNanos: e.StartedUnixNanos, SizeBytes: e.SizeBytes, BootId: e.BootID}
}
