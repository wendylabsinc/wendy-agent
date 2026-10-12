package unitree

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/atomicfile"
)

// Pairing is a verified connection saved on the WendyOS device.
type Pairing struct {
	Device   Device    `json:"device"`
	PairedAt time.Time `json:"paired_at"`
}

// Store saves robot keys in a private file. Account passwords and cloud tokens
// never enter the store. Use one Store per agent to serialize modifications.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore sets the agent-owned pairing file path.
func NewStore(path string) *Store { return &Store{path: path} }

type storedPairing struct {
	Serial        string    `json:"serial"`
	Name          string    `json:"name"`
	Address       string    `json:"address"`
	Model         string    `json:"model,omitempty"`
	EncryptionKey string    `json:"encryption_key,omitempty"`
	PairedAt      time.Time `json:"paired_at"`
}

type storedPairings struct {
	Version int             `json:"version"`
	Devices []storedPairing `json:"devices"`
}

// List includes encryption keys for reconnecting. Device's JSON representation
// excludes those keys; callers must not format the whole struct for logging.
func (s *Store) List() ([]Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Save persists a pairing after the caller verifies the robot with Connect.
func (s *Store) Save(pairing Pairing) error {
	if err := validatePairing(pairing); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range list {
		if list[i].Device.Serial == pairing.Device.Serial {
			list[i] = pairing
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, pairing)
	}
	return s.write(list)
}

// Remove forgets a robot's saved address and encryption key.
func (s *Store) Remove(serial string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].Device.Serial == serial {
			return s.write(append(list[:i], list[i+1:]...))
		}
	}
	return os.ErrNotExist
}

func (s *Store) load() ([]Pairing, error) {
	if !filepath.IsAbs(s.path) {
		return nil, errors.New("Unitree pairing store path must be absolute")
	}
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading Unitree pairing store: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, errors.New("Unitree pairing store must be a private regular file no larger than 1 MiB")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("reading Unitree pairing store: %w", err)
	}
	var stored storedPairings
	if json.Unmarshal(raw, &stored) != nil || stored.Version != 1 || len(stored.Devices) > 256 {
		return nil, errors.New("invalid Unitree pairing store")
	}
	list := make([]Pairing, 0, len(stored.Devices))
	seen := make(map[string]bool)
	for _, record := range stored.Devices {
		pairing := Pairing{
			Device: Device{Serial: record.Serial, Name: record.Name, Address: record.Address,
				Model: record.Model, EncryptionKey: record.EncryptionKey},
			PairedAt: record.PairedAt,
		}
		if validatePairing(pairing) != nil || seen[record.Serial] {
			return nil, errors.New("invalid Unitree pairing store record")
		}
		seen[record.Serial] = true
		list = append(list, pairing)
	}
	return list, nil
}

func (s *Store) write(list []Pairing) error {
	if len(list) > 256 {
		return errors.New("at most 256 Unitree robots can be paired")
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Device.Serial < list[j].Device.Serial })
	stored := storedPairings{Version: 1, Devices: make([]storedPairing, 0, len(list))}
	for _, pair := range list {
		stored.Devices = append(stored.Devices, storedPairing{
			Serial: pair.Device.Serial, Name: pair.Device.Name, Address: pair.Device.Address,
			Model: pair.Device.Model, EncryptionKey: pair.Device.EncryptionKey, PairedAt: pair.PairedAt,
		})
	}
	raw, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("Unitree pairing store exceeds 1 MiB")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("creating Unitree pairing store directory: %w", err)
	}
	return atomicfile.Write(s.path, raw, 0600)
}

func validatePairing(pair Pairing) error {
	if !ValidSerial(pair.Device.Serial) || !isRobotIP(net.ParseIP(pair.Device.Address)) || pair.PairedAt.IsZero() {
		return errors.New("Unitree pairing requires a serial, unicast IP address, and verification time")
	}
	if pair.Device.EncryptionKey != "" && !validEncryptionKey(pair.Device.EncryptionKey) {
		return errors.New("Unitree encryption key must contain 32 hexadecimal characters")
	}
	return nil
}
