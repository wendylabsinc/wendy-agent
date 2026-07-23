package services

import (
	"context"
	"os"
	"runtime"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type DeviceInfoService struct {
	agentpbv2.UnimplementedWendyDeviceInfoServiceServer
	logger             *zap.Logger
	hardwareDiscoverer HardwareDiscoverer
	watchStore         *HardwareWatchStore // nil when watch persistence is unavailable
	hardwareHub        *HardwareEventHub   // nil when live hardware events are unavailable
}

func NewDeviceInfoService(logger *zap.Logger, hd HardwareDiscoverer, watchStore *HardwareWatchStore, hub *HardwareEventHub) *DeviceInfoService {
	return &DeviceInfoService{logger: logger, hardwareDiscoverer: hd, watchStore: watchStore, hardwareHub: hub}
}

func (s *DeviceInfoService) GetDeviceInfo(_ context.Context, _ *agentpbv2.GetDeviceInfoRequest) (*agentpbv2.GetDeviceInfoResponse, error) {
	resp := &agentpbv2.GetDeviceInfoResponse{
		Version:         version.Version,
		Os:              detectOS(),
		CpuArchitecture: runtime.GOARCH,
		Featureset:      detectFeatureset(),
	}

	if v, ok := wendyOSVersion(); ok {
		resp.OsVersion = &v
	} else if _, distroVer := detectDistro(); distroVer != "" {
		resp.OsVersion = &distroVer
	}

	if data, err := os.ReadFile("/etc/wendyos/device-type"); err == nil {
		v := strings.TrimSpace(string(data))
		resp.DeviceType = &v
	}

	gpuInfo := detectGPUInfo()
	resp.HasGpu = &gpuInfo.hasGPU
	if gpuInfo.vendor != "" {
		resp.GpuVendor = &gpuInfo.vendor
	}
	if gpuInfo.jetpackVersion != "" {
		resp.JetpackVersion = &gpuInfo.jetpackVersion
	}
	if gpuInfo.cudaVersion != "" {
		resp.CudaVersion = &gpuInfo.cudaVersion
	}
	if gpuInfo.gpuArch != "" {
		resp.GpuArch = &gpuInfo.gpuArch
	}

	if usage, ok := rootDiskUsage(); ok {
		resp.DiskUsedBytes = &usage.usedBytes
		resp.DiskTotalBytes = &usage.totalBytes
	}

	resp.MemTotalBytes, resp.CpuCount = hostMemAndCPUCount()

	for _, p := range listDiskPartitions() {
		resp.Partitions = append(resp.Partitions, &agentpbv2.DiskPartition{
			Mountpoint: p.mountpoint,
			Filesystem: p.filesystem,
			Device:     p.device,
			UsedBytes:  p.usedBytes,
			TotalBytes: p.totalBytes,
		})
	}

	return resp, nil
}

// hostMemAndCPUCount reads the device's total RAM and online logical CPU core
// count from /proc for the device-info response. Values are zero when
// unreadable (non-Linux hosts) — a real host never has 0 RAM or 0 CPUs, so
// consumers treat zero as "unknown".
// REFACTOR: if zero ever needs to be distinguishable from unreadable, make
// mem_total_bytes/cpu_count `optional` the next time device_info_service.proto
// is touched, and return pointers here.
func hostMemAndCPUCount() (memTotal int64, cpuCount uint32) {
	if mem, err := hoststats.ReadMemory(); err == nil {
		memTotal = mem.TotalBytes
	}
	if cpu, err := hoststats.ReadCPU(); err == nil {
		cpuCount = cpu.CPUCount
	}
	return memTotal, cpuCount
}

func (s *DeviceInfoService) ListHardwareCapabilities(ctx context.Context, req *agentpbv2.ListHardwareCapabilitiesRequest) (*agentpbv2.ListHardwareCapabilitiesResponse, error) {
	caps, err := s.discoverV2(ctx, req.GetCategoryFilter())
	if err != nil {
		return nil, err
	}
	return &agentpbv2.ListHardwareCapabilitiesResponse{Capabilities: caps}, nil
}

func (s *DeviceInfoService) discoverV2(ctx context.Context, categoryFilter string) ([]*agentpbv2.ListHardwareCapabilitiesResponse_HardwareCapability, error) {
	caps, err := s.hardwareDiscoverer.Discover(ctx, categoryFilter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "hardware discovery failed: %v", err)
	}
	v2caps := make([]*agentpbv2.ListHardwareCapabilitiesResponse_HardwareCapability, len(caps))
	for i, c := range caps {
		v2caps[i] = &agentpbv2.ListHardwareCapabilitiesResponse_HardwareCapability{
			Category:    c.Category,
			DevicePath:  c.DevicePath,
			Description: c.Description,
			Properties:  c.Properties,
		}
	}
	return v2caps, nil
}

// WatchHardware sends a HardwareSnapshot (current USB devices + watch list)
// and then pushes every hardware event in real time until the client goes
// away. Snapshot-only clients read the first message and cancel.
func (s *DeviceInfoService) WatchHardware(_ *agentpbv2.WatchHardwareRequest, stream agentpbv2.WendyDeviceInfoService_WatchHardwareServer) error {
	ctx := stream.Context()

	usbDevices, err := s.discoverV2(ctx, "usb")
	if err != nil {
		return err
	}
	snapshot := &agentpbv2.HardwareSnapshot{UsbDevices: usbDevices}
	if s.watchStore != nil {
		watches, err := s.watchStore.Load()
		if err != nil {
			return status.Errorf(codes.Internal, "loading hardware watch list: %v", err)
		}
		for _, w := range watches {
			snapshot.WatchList = append(snapshot.WatchList, watchedDeviceToProto(w))
		}
	}
	if err := stream.Send(&agentpbv2.WatchHardwareResponse{
		Payload: &agentpbv2.WatchHardwareResponse_Snapshot{Snapshot: snapshot},
	}); err != nil {
		return err
	}

	if s.hardwareHub == nil {
		// No live event source on this platform; hold the stream open so the
		// contract (snapshot, then events) stays uniform for clients.
		<-ctx.Done()
		return nil
	}
	events, cancel := s.hardwareHub.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-events:
			if err := stream.Send(&agentpbv2.WatchHardwareResponse{
				Payload: &agentpbv2.WatchHardwareResponse_Event{Event: ev},
			}); err != nil {
				return err
			}
		}
	}
}

func (s *DeviceInfoService) SetHardwareWatchList(_ context.Context, req *agentpbv2.SetHardwareWatchListRequest) (*agentpbv2.SetHardwareWatchListResponse, error) {
	if s.watchStore == nil {
		return nil, status.Error(codes.Unavailable, "hardware watch list is not available on this device")
	}
	devices := make([]WatchedDevice, 0, len(req.GetDevices()))
	for i, d := range req.GetDevices() {
		w := WatchedDevice{
			VendorID:  strings.ToLower(d.GetVendorId()),
			ProductID: strings.ToLower(d.GetProductId()),
			Serial:    d.GetSerial(),
			Label:     d.GetLabel(),
		}
		if err := ValidateWatchedDevice(w); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "devices[%d]: %v", i, err)
		}
		devices = append(devices, w)
	}
	if err := s.watchStore.Save(devices); err != nil {
		return nil, status.Errorf(codes.Internal, "saving hardware watch list: %v", err)
	}
	s.logger.Info("hardware watch list updated", zap.Int("devices", len(devices)))
	return &agentpbv2.SetHardwareWatchListResponse{}, nil
}

func watchedDeviceToProto(d WatchedDevice) *agentpbv2.WatchedUSBDevice {
	p := &agentpbv2.WatchedUSBDevice{
		VendorId:  d.VendorID,
		ProductId: d.ProductID,
	}
	if d.Serial != "" {
		p.Serial = &d.Serial
	}
	if d.Label != "" {
		p.Label = &d.Label
	}
	return p
}
