package ros2camera

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	IDBandStart = 128
	IDBandEnd   = 199
)

type assignment struct {
	Key string `json:"key"`
	ID  uint32 `json:"id"`
}

type registry struct {
	path string
	mu   sync.Mutex
	ids  map[string]uint32
}

func newRegistry(path string) *registry { return &registry{path: path, ids: map[string]uint32{}} }

func (r *registry) load() error {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading ROS 2 camera registry: %w", err)
	}
	var entries []assignment
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parsing ROS 2 camera registry: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	used := map[uint32]bool{}
	for _, entry := range entries {
		if entry.Key == "" || entry.ID < IDBandStart || entry.ID > IDBandEnd || used[entry.ID] {
			continue
		}
		r.ids[entry.Key] = entry.ID
		used[entry.ID] = true
	}
	return nil
}

func (r *registry) idFor(key string) (uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.ids[key]; ok {
		return id, nil
	}
	used := map[uint32]bool{}
	for _, id := range r.ids {
		used[id] = true
	}
	var id uint32
	for candidate := uint32(IDBandStart); candidate <= IDBandEnd; candidate++ {
		if !used[candidate] {
			id = candidate
			break
		}
	}
	if id == 0 {
		return 0, errors.New("no free ROS 2 camera device IDs")
	}
	r.ids[key] = id
	if err := r.saveLocked(); err != nil {
		delete(r.ids, key)
		return 0, err
	}
	return id, nil
}

func (r *registry) saveLocked() error {
	entries := make([]assignment, 0, len(r.ids))
	for key, id := range r.ids {
		entries = append(entries, assignment{Key: key, ID: id})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return err
	}
	return nil
}
