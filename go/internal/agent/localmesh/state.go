package localmesh

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/wendylabsinc/WendyOS/go/babel"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// PersistentState contains safety history, never active peer connections. The
// origin revision advances durably before signing a new directory announcement.
//
// Device is the full device principal and RouterID is the Babel routing identity
// derived for it. They are stored together because the checkpoint's feasibility
// history is only meaningful for the router ID that produced it.
type PersistentState struct {
	Version  int
	Device   string `json:"device"`
	RouterID uint64 `json:"routerID"`
	Revision uint64 `json:"revision"`
	Babel    *babel.Checkpoint
	Receipts []DirectoryReceipt
}

type StateStore struct {
	path     string
	previous []byte
}

// OpenStateStore loads the safety state pinned to one device principal.
//
// routerID is the freshly derived instance routing identity. A stored state for
// this principal pins that identity: the same device keeps the router ID its
// feasibility history was written under, including a tagged-random derivation,
// for as long as the state survives. A state written for a different principal
// is refused rather than adopted, so an identity change can never inherit - or
// silently replace - another instance's router ID, and a checkpoint whose
// router ID disagrees with the pinned one is refused too.
func OpenStateStore(path string, device meshid.Device, routerID babel.RouterID) (*StateStore, PersistentState, error) {
	s := &StateStore{path: path}
	principal := device.Principal()
	if principal == "" || len(principal) > maxPrincipalBytes || routerID == 0 || routerID == babel.RouterID(^uint64(0)) {
		return nil, PersistentState{}, errors.New("invalid local-mesh instance identity")
	}
	state := PersistentState{Version: 2, Device: principal, RouterID: uint64(routerID)}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, state, nil
	}
	if err != nil {
		return nil, state, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if err != nil {
		return nil, state, err
	}
	if len(data) > 4<<20 {
		return nil, state, errors.New("oversized local-mesh safety state")
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, state, err
	}
	if state.Version != 2 || state.Device != principal {
		return nil, state, errors.New("local-mesh safety state identity mismatch")
	}
	if _, err := meshid.DeviceFromPrincipal(state.Device); err != nil {
		return nil, state, errors.New("local-mesh safety state identity mismatch")
	}
	if state.RouterID == 0 || state.RouterID == uint64(babel.RouterID(^uint64(0))) {
		return nil, state, errors.New("local-mesh safety state has no router ID")
	}
	// The instance's own router ID is the pin: a file written under a different
	// one - after an identity change - is refused rather than silently adopted.
	if state.RouterID != uint64(routerID) {
		return nil, state, errors.New("local-mesh safety state was written by another router ID")
	}
	if state.Babel != nil && state.Babel.RouterID != babel.RouterID(state.RouterID) {
		return nil, state, errors.New("local-mesh checkpoint belongs to another router ID")
	}
	s.previous = append([]byte(nil), data...)
	return s, state, nil
}

func (s *StateStore) Save(state PersistentState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if bytes.Equal(data, s.previous) {
		return nil
	}
	if err = atomicStateWrite(s.path, data); err != nil {
		return err
	}
	s.previous = append(s.previous[:0], data...)
	return nil
}

func atomicStateWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".local-mesh-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
