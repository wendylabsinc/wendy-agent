package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/wendylabsinc/wendy/go/internal/agent/localsocket"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	eventSocketFilename = "events.sock"
	appEventSocketGID   = 2000
)

// AppEventSocketRootPath is disk-backed so existing event-entitled containers
// continue resolving their mounted socket directory across Agent restarts.
var AppEventSocketRootPath = "/var/lib/wendy/app-events"

type appEventSocketIdentity struct {
	AppID       string `json:"app_id"`
	ServiceName string `json:"service_name,omitempty"`
}

type appEventSocket struct {
	server *grpc.Server
}

// AppEventSocketManager owns one narrow gRPC server per entitled app service.
// Separate socket directories make the mount itself the caller credential.
type AppEventSocketManager struct {
	ctx       context.Context
	logger    *zap.Logger
	publisher EventPublisher
	mu        sync.Mutex
	sockets   map[string]appEventSocket
}

func NewAppEventSocketManager(
	ctx context.Context,
	logger *zap.Logger,
	publisher EventPublisher,
) *AppEventSocketManager {
	manager := &AppEventSocketManager{
		ctx:       ctx,
		logger:    logger,
		publisher: publisher,
		sockets:   make(map[string]appEventSocket),
	}
	go func() {
		<-ctx.Done()
		manager.stopAll()
	}()
	return manager
}

// Restore restarts listeners recorded before an Agent restart. Invalid records
// are ignored rather than widening access or guessing source identity.
func (m *AppEventSocketManager) Restore() {
	entries, err := os.ReadDir(AppEventSocketRootPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		m.logger.Warn("cannot scan app event sockets", zap.Error(err))
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		identityPath := filepath.Join(AppEventSocketRootPath, entry.Name(), "identity.json")
		data, err := os.ReadFile(identityPath)
		if err != nil {
			m.logger.Warn("cannot read app event socket identity", zap.String("path", identityPath), zap.Error(err))
			continue
		}
		var identity appEventSocketIdentity
		if err := json.Unmarshal(data, &identity); err != nil {
			m.logger.Warn("invalid app event socket identity", zap.String("path", identityPath), zap.Error(err))
			continue
		}
		// SECURITY: Bind persisted identity to its collision-proof directory name;
		// never trust identity.json independently after an Agent restart.
		if entry.Name() != appEventSocketKey(identity.AppID, identity.ServiceName) {
			m.logger.Warn("app event socket identity path mismatch", zap.String("path", identityPath))
			continue
		}
		if _, err := m.Ensure(identity.AppID, identity.ServiceName); err != nil {
			m.logger.Warn("cannot restore app event socket", zap.String("app_id", identity.AppID), zap.Error(err))
		}
	}
}

// Ensure starts the source-attributed socket and returns its host directory for
// a read-only bind mount into exactly one container.
func (m *AppEventSocketManager) Ensure(appID, serviceName string) (string, error) {
	if err := appconfig.ValidateAppID(appID); err != nil {
		return "", fmt.Errorf("invalid app ID: %w", err)
	}
	if serviceName != "" {
		if err := appconfig.ValidateServiceName(serviceName); err != nil {
			return "", fmt.Errorf("invalid service name: %w", err)
		}
	}
	socketKey := appEventSocketKey(appID, serviceName)
	directory := filepath.Join(AppEventSocketRootPath, socketKey)

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sockets[socketKey]; exists {
		return directory, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", fmt.Errorf("create app event socket directory: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(directory, 0, appEventSocketGID); err != nil {
			return "", fmt.Errorf("set app event socket directory ownership: %w", err)
		}
	}
	identity, err := json.Marshal(appEventSocketIdentity{AppID: appID, ServiceName: serviceName})
	if err != nil {
		return "", fmt.Errorf("encode app event socket identity: %w", err)
	}
	if err := syncWriteFile(filepath.Join(directory, "identity.json"), identity, 0o600); err != nil {
		return "", fmt.Errorf("persist app event socket identity: %w", err)
	}

	socketPath := filepath.Join(directory, eventSocketFilename)
	listener, err := localsocket.Listen(socketPath)
	if err != nil {
		return "", fmt.Errorf("listen for app events: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(socketPath, 0, appEventSocketGID); err != nil {
			listener.Close()
			return "", fmt.Errorf("set app event socket ownership: %w", err)
		}
	}
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(4*1024),
		grpc.MaxConcurrentStreams(16),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 2 * time.Minute,
		}),
	)
	agentpb.RegisterWendyEventServiceServer(
		server,
		NewWendyEventService(appID, m.publisher),
	)
	m.sockets[socketKey] = appEventSocket{server: server}
	go func() {
		m.logger.Info("app event socket listening",
			zap.String("app_id", appID),
			zap.String("service_name", serviceName),
			zap.String("path", filepath.Join(directory, eventSocketFilename)))
		if err := server.Serve(listener); err != nil && m.ctx.Err() == nil {
			m.logger.Error("app event socket failed", zap.String("app_id", appID), zap.Error(err))
		}
	}()
	return directory, nil
}

func appEventSocketKey(appID, serviceName string) string {
	identityDigest := sha256.Sum256([]byte(appID + "\x00" + serviceName))
	return hex.EncodeToString(identityDigest[:16])
}

func (m *AppEventSocketManager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, socket := range m.sockets {
		socket.server.Stop()
		delete(m.sockets, name)
	}
}
