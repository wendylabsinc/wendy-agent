package services

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"github.com/wendylabsinc/wendy/go/internal/agent/localsocket"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"go.uber.org/zap"
)

const (
	DataSocketFilename    = "data.sock"
	dataProtocolMaxRecord = 64 << 10
	dataSocketGroupGID    = 2000
)

var AppDataSocketRootPath = "/var/lib/wendy/app-data"

type appDataSocket struct {
	appID    string
	listener net.Listener
	owners   map[string]struct{}
}
type AppDataSocketManager struct {
	ctx     context.Context
	logger  *zap.Logger
	capture *data.Manager
	mu      sync.Mutex
	sockets map[string]*appDataSocket
}

func NewAppDataSocketManager(ctx context.Context, logger *zap.Logger, capture *data.Manager) *AppDataSocketManager {
	m := &AppDataSocketManager{ctx: ctx, logger: logger, capture: capture, sockets: map[string]*appDataSocket{}}
	go func() { <-ctx.Done(); m.stopAll() }()
	return m
}

func (m *AppDataSocketManager) Ensure(appID, service string) (string, error) {
	if err := appconfig.ValidateAppID(appID); err != nil {
		return "", err
	}
	if service != "" {
		if err := appconfig.ValidateServiceName(service); err != nil {
			return "", err
		}
	}
	key := appDataKey(appID)
	dir := filepath.Join(AppDataSocketRootPath, key)
	owner := systemAPIOwner(service)
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sockets[key]; s != nil {
		if s.appID != appID {
			return "", errors.New("app data identity collision")
		}
		s.owners[owner] = struct{}{}
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, 0, dataSocketGroupGID); err != nil {
			return "", err
		}
	}
	id, _ := json.Marshal(map[string]string{"app_id": appID})
	if err := syncWriteFile(filepath.Join(dir, "identity.json"), id, 0o600); err != nil {
		return "", err
	}
	l, err := localsocket.Listen(filepath.Join(dir, DataSocketFilename))
	if err != nil {
		return "", err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(filepath.Join(dir, DataSocketFilename), 0, dataSocketGroupGID); err != nil {
			l.Close()
			return "", err
		}
	}
	s := &appDataSocket{appID: appID, listener: l, owners: map[string]struct{}{owner: {}}}
	m.sockets[key] = s
	go m.serve(s)
	return dir, nil
}

func (m *AppDataSocketManager) Release(appID, service string) {
	key := appDataKey(appID)
	m.mu.Lock()
	s := m.sockets[key]
	if s == nil || s.appID != appID {
		m.mu.Unlock()
		return
	}
	delete(s.owners, systemAPIOwner(service))
	if len(s.owners) > 0 {
		m.mu.Unlock()
		return
	}
	delete(m.sockets, key)
	m.mu.Unlock()
	s.listener.Close()
	_ = os.RemoveAll(filepath.Join(AppDataSocketRootPath, key))
}
func (m *AppDataSocketManager) ReleaseApp(appID string) {
	key := appDataKey(appID)
	m.mu.Lock()
	s := m.sockets[key]
	delete(m.sockets, key)
	m.mu.Unlock()
	if s != nil {
		s.listener.Close()
		_ = os.RemoveAll(filepath.Join(AppDataSocketRootPath, key))
	}
}
func (m *AppDataSocketManager) stopAll() {
	m.mu.Lock()
	var all []*appDataSocket
	for k, s := range m.sockets {
		all = append(all, s)
		delete(m.sockets, k)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.listener.Close()
	}
}
func appDataKey(id string) string { h := sha256.Sum256([]byte(id)); return hex.EncodeToString(h[:16]) }

func (m *AppDataSocketManager) serve(s *appDataSocket) {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			if m.ctx.Err() == nil && m.logger != nil {
				m.logger.Warn("app data socket accept failed", zap.Error(err))
			}
			return
		}
		go m.serveConn(s.appID, c)
	}
}

type dataAck struct {
	Version int    `json:"version"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
}

func (m *AppDataSocketManager) serveConn(appID string, c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	window := time.Now()
	count := 0
	for {
		if time.Since(window) >= time.Second {
			window = time.Now()
			count = 0
		}
		count++
		if count > 200 {
			_ = writeDataFrame(c, dataAck{Version: 1, State: "rejected", Error: "rate limit exceeded"})
			return
		}
		payload, err := readDataFrame(r)
		if err != nil {
			return
		}
		var rec data.ApplicationRecord
		if err = json.Unmarshal(payload, &rec); err != nil {
			_ = writeDataFrame(c, dataAck{Version: 1, State: "rejected", Error: "invalid JSON"})
			continue
		}
		if err = validateApplicationRecord(rec); err != nil {
			_ = writeDataFrame(c, dataAck{Version: 1, State: "rejected", Error: err.Error()})
			continue
		}
		state, err := m.capture.RecordApplication(appID, rec)
		ack := dataAck{Version: 1, State: state}
		if err != nil {
			ack.State = "rejected"
			ack.Error = err.Error()
		}
		if writeDataFrame(c, ack) != nil {
			return
		}
	}
}

func readDataFrame(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	if n == 0 || n > dataProtocolMaxRecord {
		return nil, errors.New("record exceeds 64 KiB")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
func writeDataFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > dataProtocolMaxRecord {
		return errors.New("response too large")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, err = w.Write(h[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
func validateApplicationRecord(r data.ApplicationRecord) error {
	if r.Version != 1 {
		return fmt.Errorf("unsupported protocol version %d", r.Version)
	}
	if r.Type != "event" && r.Type != "prediction" {
		return errors.New("type must be event or prediction")
	}
	if r.Type == "event" && r.Name == "" {
		return errors.New("event name is required")
	}
	if r.Type == "prediction" && r.Model == "" {
		return errors.New("prediction model is required")
	}
	if len(r.Attributes) > 128 {
		return errors.New("too many attributes")
	}
	for k := range r.Attributes {
		if k == "" || len(k) > 128 {
			return errors.New("invalid attribute key")
		}
	}
	return nil
}
