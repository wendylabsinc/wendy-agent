// Package data implements trustworthy device-local episode capture. It keeps
// UTC correlation separate from the immutable CLOCK_BOOTTIME episode timeline.
package data

import (
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
)

const (
	ManifestVersion = 1
	ClockAlgorithm  = "wendy-sandwich-v1"
)

type ClockSample struct {
	BootBeforeNanos int64 `json:"boot_before_nanos"`
	TargetNanos     int64 `json:"target_nanos"`
	BootAfterNanos  int64 `json:"boot_after_nanos"`
}

type UTCObservation struct {
	EpisodeNanos     int64       `json:"episode_nanos"`
	OffsetLowerNanos int64       `json:"offset_lower_nanos"`
	OffsetUpperNanos int64       `json:"offset_upper_nanos"`
	OffsetMidNanos   int64       `json:"offset_midpoint_nanos"`
	UncertaintyNanos int64       `json:"uncertainty_nanos"`
	Confidence       string      `json:"confidence"`
	EvidenceSource   string      `json:"evidence_source"`
	Algorithm        string      `json:"algorithm"`
	ObservedUnixNano int64       `json:"observed_unix_nanos"`
	AgeNanos         int64       `json:"age_nanos"`
	Sample           ClockSample `json:"sample"`
}

type Source struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	ClockDomain string `json:"clock_domain"`
	Healthy     bool   `json:"healthy"`
	Detail      string `json:"detail,omitempty"`
}

type SourceStats struct {
	Source          Source         `json:"source"`
	RequestedOffset int64          `json:"requested_offset_nanos"`
	ActualOffset    int64          `json:"actual_offset_nanos"`
	Count           uint64         `json:"count"`
	Drops           *uint64        `json:"drops,omitempty"`
	DropAccounting  string         `json:"drop_accounting"`
	MappingError    *int64         `json:"mapping_error_nanos,omitempty"`
	Discontinuities uint64         `json:"discontinuities"`
	Mappings        []ClockMapping `json:"clock_mappings,omitempty"`
}

// ClockMapping describes one immutable source-clock to CLOCK_BOOTTIME mapping
// segment. Raw samples live beside the captured stream; this summary makes the
// achieved bound and any discontinuity visible without parsing the index.
type ClockMapping struct {
	ID                string `json:"id"`
	SourceClockDomain string `json:"source_clock_domain"`
	CanonicalClock    string `json:"canonical_clock"`
	StartedEpisodeNS  int64  `json:"started_episode_nanos"`
	EndedEpisodeNS    int64  `json:"ended_episode_nanos,omitempty"`
	MaxErrorNanos     int64  `json:"max_error_nanos"`
	Samples           uint64 `json:"samples"`
	Algorithm         string `json:"algorithm"`
	Discontinuity     string `json:"discontinuity,omitempty"`
}

// CaptureSession is the immutable episode context handed to source adapters.
// Directory names the journaled .partial directory and is never user supplied.
type CaptureSession struct {
	ID               string
	Directory        string
	RequestBootNanos int64
	BootID           string
}

// CaptureResult is reported by an adapter before the episode is sealed.
type CaptureResult struct {
	SourceID        string
	ClockDomain     string
	SourceDetail    string
	ActualOffset    *int64
	Count           uint64
	Drops           *uint64
	DropAccounting  string
	MappingError    *int64
	Discontinuities uint64
	Mappings        []ClockMapping
}

// MonotonicMappingSample is a sandwich sample mapping Linux CLOCK_MONOTONIC
// into canonical CLOCK_BOOTTIME. The interval captures syscall/read latency.
type MonotonicMappingSample struct {
	BootBeforeNanos  int64 `json:"boot_before_nanos"`
	MonotonicNanos   int64 `json:"monotonic_nanos"`
	BootAfterNanos   int64 `json:"boot_after_nanos"`
	OffsetLowerNanos int64 `json:"offset_lower_nanos"`
	OffsetUpperNanos int64 `json:"offset_upper_nanos"`
}

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Calibration struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version           int                  `json:"version"`
	ID                string               `json:"id"`
	Name              string               `json:"name,omitempty"`
	State             string               `json:"state"`
	Interruption      string               `json:"interruption,omitempty"`
	CanonicalClock    string               `json:"canonical_clock"`
	BootID            string               `json:"boot_id"`
	RequestBootNanos  int64                `json:"request_boottime_nanos"`
	StartedUnixNanos  int64                `json:"started_unix_nanos"`
	StoppedEpisodeNS  int64                `json:"stopped_episode_nanos,omitempty"`
	UTCObservations   []UTCObservation     `json:"utc_observations"`
	Roughtime         []timesync.Consensus `json:"roughtime_observations,omitempty"`
	SystemClockStatus string               `json:"system_clock_status"`
	Sources           []SourceStats        `json:"sources"`
	Calibrations      []Calibration        `json:"calibrations,omitempty"`
	Files             []File               `json:"files"`
	RecoveryActions   []string             `json:"recovery_actions,omitempty"`
	PreRollLost       uint64               `json:"pre_roll_lost"`
	PreRollAccounting string               `json:"pre_roll_accounting"`
}

type StartOptions struct {
	Name                  string
	Sources               []string
	ExcludeSources        []string
	RequireUTCUncertainty time.Duration
	Calibrations          map[string][]byte
}

type EpisodeInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	State            string `json:"state"`
	StartedUnixNanos int64  `json:"started_unix_nanos"`
	SizeBytes        int64  `json:"size_bytes"`
	BootID           string `json:"boot_id"`
}
