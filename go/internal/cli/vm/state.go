package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockAcquireWindow is how long a start waits out a transient holder before
// declaring the VM already running.
const lockAcquireWindow = 250 * time.Millisecond

// ErrAlreadyRunning reports that another process already holds a VM's run lock.
var ErrAlreadyRunning = errors.New("VM is already running")

// Meta is a VM's durable record, written once when it is created. Kept apart
// from State so a stopped VM can still report where its image came from.
type Meta struct {
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"createdAt"`
	ImageVersion string    `json:"imageVersion,omitempty"`
	ImageSource  string    `json:"imageSource,omitempty"`
	DiskBytes    int64     `json:"diskBytes,omitempty"`

	// AgentPort is the host port this VM last bound successfully. Sticky, so a
	// VM forced off the default port keeps the address the user wrote down.
	AgentPort int `json:"agentPort,omitempty"`
}

// State describes one run of a VM. Its absence means the VM is not running.
type State struct {
	Name string `json:"name"`

	// PID is 0 between reserving the run lock and QEMU actually starting,
	// which readers should treat as "starting", not as a stale record.
	PID       int       `json:"pid"`
	AgentPort int       `json:"agentPort,omitempty"`
	NetMode   NetMode   `json:"netMode,omitempty"`
	MAC       string    `json:"mac,omitempty"`
	Accel     Accel     `json:"accel,omitempty"`
	MemoryMiB int       `json:"memoryMiB,omitempty"`
	CPUs      int       `json:"cpus,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	Detached  bool      `json:"detached,omitempty"`
	LogPath   string    `json:"logPath,omitempty"`
}

// Status is everything known about one VM.
type Status struct {
	Name    string
	Exists  bool
	Running bool
	Meta    Meta
	State   State
}

// MetaPath, StatePath, LockPath and LogPath live inside the VM's own directory
// so Store.List keeps working (it filters ReadDir to directories) and Remove
// still cleans everything up.
func (s *Store) MetaPath(name string) string  { return filepath.Join(s.Dir(name), "meta.json") }
func (s *Store) StatePath(name string) string { return filepath.Join(s.Dir(name), "state.json") }
func (s *Store) LockPath(name string) string  { return filepath.Join(s.Dir(name), "run.lock") }
func (s *Store) LogPath(name string) string   { return filepath.Join(s.Dir(name), "console.log") }

// writeJSON writes v through a temporary file in the same directory, so a
// reader never observes a half-written record.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// A unique name per write: two processes sharing one ".tmp" can interleave
	// write and rename and publish a truncated record.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	// These two discard the close error deliberately: a write already failed,
	// and the close error would only mask the one worth reporting. The close on
	// the success path below IS checked -- on a file this size it can be the
	// first sign of a failed writeback.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteMeta records a VM's durable provenance.
func (s *Store) WriteMeta(m Meta) error { return writeJSON(s.MetaPath(m.Name), m) }

// ReadMeta returns a VM's durable record and whether it was actually read; a
// missing or unreadable file yields a zero Meta, which stays usable.
//
// A caller that writes the record back must check ok, or a transient read
// failure would persist the empty value and lose the VM's provenance.
func (s *Store) ReadMeta(name string) (m Meta, ok bool) {
	data, err := os.ReadFile(s.MetaPath(name))
	if err != nil {
		return Meta{Name: name}, false
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{Name: name}, false
	}
	m.Name = name
	return m, true
}

// WriteState records the current run.
func (s *Store) WriteState(st State) error { return writeJSON(s.StatePath(st.Name), st) }

// ReadState returns the recorded run, or an error if there is none.
func (s *Store) ReadState(name string) (State, error) {
	data, err := os.ReadFile(s.StatePath(name))
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("reading %s: %w", s.StatePath(name), err)
	}
	return st, nil
}

// Nothing is ever written through a run-lock handle -- it exists only to carry
// the advisory lock -- so its Close can lose no data and is deferred bare. The
// one handle in this file that IS written, writeJSON's temporary, checks Close
// explicitly.
//
// acquireRunLock takes name's exclusive run lock. The returned file MUST be
// passed to the emulator via Cmd.ExtraFiles: the lock lives on the open file
// description, which fork+exec inherits and the kernel frees when the last
// descriptor closes, so a recycled pid can never fake liveness.
func (s *Store) acquireRunLock(name string) (*os.File, error) {
	f, err := os.OpenFile(s.LockPath(name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening run lock: %w", err)
	}
	// Retry briefly. A status poll holds a shared lock for microseconds, and a
	// shared lock still blocks this exclusive one -- so without a window, any
	// start racing the simulator tab's 2-second refresh would report the VM
	// already running. A VM that really is running holds the lock for its whole
	// life, so it never becomes available inside this window.
	deadline := time.Now().Add(lockAcquireWindow)
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", s.LockPath(name), err)
		}
		if locked {
			return f, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runLockHeld reports whether some process still holds name's run lock.
func (s *Store) runLockHeld(name string) (bool, error) {
	f, err := os.OpenFile(s.LockPath(name), os.O_RDWR, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()

	// Shared, not exclusive: this is a read. Taking the write lock here would
	// make a start racing with any status poll fail spuriously.
	free, err := trySharedLock(f)
	if err != nil {
		return false, err
	}
	if free {
		_ = unlockFile(f)
		return false, nil
	}
	return true, nil
}

// Status reports what a VM is doing, and reaps the state of one that died
// without cleaning up. Every abnormal exit -- SIGKILL, an OOM kill, a host
// reboot -- releases the kernel lock, so no daemon or boot-time sweep is
// needed: the next caller tidies up.
func (s *Store) Status(name string) (Status, error) {
	if err := ValidName(name); err != nil {
		return Status{}, err
	}
	st := Status{Name: name}
	if _, err := os.Stat(s.Dir(name)); err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return Status{}, err
	}
	st.Exists = true
	st.Meta, _ = s.ReadMeta(name)

	held, err := s.runLockHeld(name)
	if err != nil {
		return Status{}, err
	}
	if !held {
		reaped, err := s.reapStaleState(name)
		if err != nil {
			return Status{}, err
		}
		if reaped {
			return st, nil
		}
		// A start or cleanup won the lock after our probe. Do not report a
		// stopped VM while another owner is still modifying its run record.
	}
	st.Running = true
	if run, err := s.ReadState(name); err == nil {
		st.State = run
	}
	return st, nil
}

// reapStaleState clears the record of a VM that died without cleaning up.
//
// Under the exclusive lock, because the probe that decided the VM was gone is
// lock-free: a start can win the lock and write its own record in between, and
// deleting that would leave a live VM reading as "starting" forever, which
// every command refuses to act on. Failing to take the lock means exactly that
// happened, so there is nothing stale left to reap.
func (s *Store) reapStaleState(name string) (bool, error) {
	// Nothing recorded, nothing to reap -- and checking first keeps a status
	// read of a never-started VM from creating its lock file below.
	if _, err := os.Stat(s.StatePath(name)); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	// O_CREATE, not a missing-file shortcut: a start racing us creates this
	// same file, so taking the lock through it is what orders us against it.
	f, err := os.OpenFile(s.LockPath(name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	locked, err := tryLockFile(f)
	if err != nil || !locked {
		return false, err
	}
	defer func() { _ = unlockFile(f) }()

	if err := os.Remove(s.StatePath(name)); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

// Statuses reports every VM in the store, sorted by name.
func (s *Store) Statuses() ([]Status, error) {
	names, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(names))
	for _, name := range names {
		st, err := s.Status(name)
		if err != nil {
			// One unreadable directory must not hide every healthy VM, which
			// is what returning here did: the whole list, and the Simulator
			// tab with it, went empty.
			st = Status{Name: name, Exists: true}
		}
		out = append(out, st)
	}
	return out, nil
}
