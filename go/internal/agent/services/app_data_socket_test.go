package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

func TestDataProtocolLengthLimit(t *testing.T) {
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], dataProtocolMaxRecord+1)
	if _, err := readDataFrame(bytes.NewReader(h[:])); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestAppDataSocketIsPrivateAndRecordsIdentity(t *testing.T) {
	capture, err := data.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	socketRoot, err := os.MkdirTemp("/tmp", "wendy-data-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketRoot)
	oldRoot := AppDataSocketRootPath
	AppDataSocketRootPath = socketRoot
	defer func() { AppDataSocketRootPath = oldRoot }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewAppDataSocketManager(ctx, nil, capture)
	dirA, err := manager.Ensure("com.example.a", "")
	if err != nil {
		t.Fatal(err)
	}
	dirB, err := manager.Ensure("com.example.b", "")
	if err != nil {
		t.Fatal(err)
	}
	if dirA == dirB {
		t.Fatal("cross-app sockets share a directory")
	}
	conn, err := net.Dial("unix", filepath.Join(dirA, DataSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	record := data.ApplicationRecord{Version: 1, Type: "event", Name: "ready", ClientBootID: "unavailable"}
	body, _ := json.Marshal(record)
	if err = writeDataFrame(conn, json.RawMessage(body)); err != nil {
		t.Fatal(err)
	}
	ackBody, err := readDataFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	var ack dataAck
	if err = json.Unmarshal(ackBody, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.State != "buffered" {
		t.Fatalf("ack=%+v", ack)
	}
	started, err := capture.Start(data.StartOptions{Sources: []string{"applications"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = capture.Stop(data.AdHocEpisodeKey); err != nil {
		t.Fatal(err)
	}
	manifest, failures, err := capture.Inspect(started.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) > 0 {
		t.Fatal(failures)
	}
	if len(manifest.Files) == 0 {
		t.Fatal("no sealed files")
	}
}

func TestValidateApplicationRecord(t *testing.T) {
	record := data.ApplicationRecord{Version: 1, Type: "event", Name: "started"}
	if err := validateApplicationRecord(record); err != nil {
		t.Fatal(err)
	}
	record.Version = 2
	if err := validateApplicationRecord(record); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("got %v", err)
	}
}
