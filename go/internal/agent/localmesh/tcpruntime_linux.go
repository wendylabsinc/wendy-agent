//go:build linux

package localmesh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	quic "github.com/quic-go/quic-go"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const tcpConfigName = "local-mesh.json"

// RunConfiguredTCP runs the explicitly configured topology. The device with the
// lower UUID initiates each edge, so a symmetric peer list creates one
// connection. A missing config leaves the local mesh disabled.
func RunConfiguredTCP(parent context.Context, configDir string, id TCPIdentity) error {
	credentials, err := NewCredentials(id.Certificate, id.Chain, id.Key)
	if err != nil {
		return err
	}
	cfg, err := LoadTCPConfig(filepath.Join(configDir, tcpConfigName), credentials.Device)
	if err != nil || cfg == nil {
		return err
	}
	stateDir := filepath.Join(configDir, "local-mesh")
	if err = os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	node, err := NewNode(ctx, stateDir, credentials, id.Name, id.AgentPort)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); listener.Close() }()
	nodeDone := make(chan error, 1)
	go func() { nodeDone <- node.Run(); cancel() }()
	allowed := make(map[string]meshid.Device, len(cfg.Peers))
	for _, peer := range cfg.devices {
		allowed[peer.Principal()] = peer
	}
	active := make(map[string]bool)
	var activeMu sync.Mutex
	claim := func(device meshid.Device) bool {
		activeMu.Lock()
		defer activeMu.Unlock()
		if active[device.Principal()] {
			return false
		}
		active[device.Principal()] = true
		return true
	}
	release := func(device meshid.Device) { activeMu.Lock(); delete(active, device.Principal()); activeMu.Unlock() }
	var wg sync.WaitGroup
	for _, peer := range cfg.devices {
		if meshid.Compare(credentials.Device, peer) >= 0 {
			continue
		}
		wg.Add(1)
		go func(peer meshid.Device) {
			defer wg.Done()
			address := cfg.Peers[indexOfDevice(cfg.devices, peer)].Address
			backoff := time.Second
			for ctx.Err() == nil {
				if claim(peer) {
					conn, err := net.DialTimeout("tcp", address, 5*time.Second)
					if err == nil {
						err = writeTCPPreface(conn, credentials.Device)
						if err == nil {
							err = attachTCP(ctx, node, credentials, peer, conn, false)
						}
						conn.Close()
					}
					release(peer)
				}
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
			}
		}(peer)
	}
	sem := make(chan struct{}, 64)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					defer conn.Close()
					id, err := readTCPPreface(conn, credentials.Device.Tenant())
					if err != nil {
						return
					}
					peer, err := meshid.ParseDevice(credentials.Device.Tenant(), id.String())
					if err != nil || meshid.Compare(peer, credentials.Device) >= 0 {
						// Only the lower UUID of an edge dials; anything else is a
						// duplicate or a peer that did not read its own ordering.
						return
					}
					if _, ok := allowed[peer.Principal()]; !ok || !claim(peer) {
						return
					}
					defer release(peer)
					_ = attachTCP(ctx, node, credentials, peer, conn, true)
				}()
			default:
				conn.Close()
			}
		}
	}()
	nodeStopped := false
	select {
	case <-ctx.Done():
	case err = <-nodeDone:
		nodeStopped = true
	}
	cancel()
	listener.Close()
	<-acceptDone
	wg.Wait()
	if !nodeStopped {
		err = <-nodeDone
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func indexOfDevice(devices []meshid.Device, want meshid.Device) int {
	for i, d := range devices {
		if d.Principal() == want.Principal() {
			return i
		}
	}
	return -1
}

// writeTCPPreface announces this device's tenant and device UUID before the
// QUIC handshake, so a listener can order the edge and reject a peer it has no
// configured relationship with. The preface is not trusted identity: the TLS
// handshake pins the full principal afterwards.
func writeTCPPreface(conn net.Conn, device meshid.Device) error {
	tenant, id := device.Tenant().UUID(), device.UUID()
	data := make([]byte, 0, 4+16+16)
	data = append(data, "WMT2"...)
	data = append(data, tenant[:]...)
	data = append(data, id[:]...)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	err := writeFull(conn, data)
	conn.SetDeadline(time.Time{})
	return err
}

func readTCPPreface(conn net.Conn, tenant meshid.Tenant) (uuid.UUID, error) {
	data := make([]byte, 4+16+16)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err := io.ReadFull(conn, data)
	conn.SetDeadline(time.Time{})
	if err != nil {
		return uuid.Nil, err
	}
	want := tenant.UUID()
	if string(data[:4]) != "WMT2" || !bytes.Equal(data[4:20], want[:]) {
		return uuid.Nil, errors.New("invalid mesh TCP preface")
	}
	var id uuid.UUID
	copy(id[:], data[20:])
	if id == uuid.Nil {
		return uuid.Nil, errors.New("invalid mesh TCP preface")
	}
	return id, nil
}

func attachTCP(ctx context.Context, node *Node, credentials *Credentials, peer meshid.Device, tcp net.Conn, server bool) error {
	pc, err := NewTCPPacketConn(tcp)
	if err != nil {
		return err
	}
	transport := &quic.Transport{Conn: pc}
	defer transport.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			transport.Close()
		case <-done:
		}
	}()
	tlsConfig, err := credentials.PeerTLS(peer)
	if err != nil {
		return err
	}
	var conn *quic.Conn
	handshake, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if server {
		listener, err := transport.Listen(tlsConfig, QUICConfig())
		if err != nil {
			return err
		}
		defer listener.Close()
		conn, err = listener.Accept(handshake)
	} else {
		conn, err = transport.Dial(handshake, pc.RemoteAddr(), tlsConfig, QUICConfig())
	}
	if err != nil {
		return fmt.Errorf("mesh QUIC peer %s: %w", peer.Principal(), err)
	}
	return node.Attach(ctx, peer, conn)
}
