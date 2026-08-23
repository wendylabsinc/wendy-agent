// Package data implements trustworthy device-local episode capture. It keeps
// UTC correlation separate from the immutable CLOCK_BOOTTIME episode timeline.
package data

import (
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
)

const (
	ManifestVersion = 2
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
	// Capture is the per-episode capture policy a campaign attached to this
	// source, nil when none applies. It is deliberately excluded from the
	// manifest: the policy lives in the campaign plan, while the manifest
	// records the behavior the adapter actually achieved.
	Capture *SourceCapture `json:"-"`
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
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	SourceID  string `json:"source_id,omitempty"`
	Format    string `json:"format"`
	MediaType string `json:"media_type"`
}

type Calibration struct {
	Source   string `json:"source"`
	Revision string `json:"revision,omitempty"`
	Path     string `json:"path,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type DeviceIdentity struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname,omitempty"`
	BootID   string `json:"boot_id"`
}

type EpisodeTrigger struct {
	Reason           string `json:"reason"`
	CampaignName     string `json:"campaign_name,omitempty"`
	CampaignRevision string `json:"campaign_revision,omitempty"`
	Expression       string `json:"expression,omitempty"`
}

type PrivacyTransformation struct {
	Name     string `json:"name"`
	Revision string `json:"revision,omitempty"`
	State    string `json:"state"`
}

type WorkflowState struct {
	State       string `json:"state"`
	Destination string `json:"destination,omitempty"`
	UpdatedAt   int64  `json:"updated_unix_nanos,omitempty"`
	// Attempts counts the transfer attempts made so far for this workflow. It
	// drives the bounded retry ceiling; a successful transfer leaves it at the
	// value reached, a permanent failure records the final count.
	Attempts int `json:"attempts,omitempty"`
	// LastError records the human-readable reason for the most recent retryable
	// failure or the permanent failure that moved the workflow to "failed".
	LastError string `json:"last_error,omitempty"`
	// NextAttemptUnixNanos is the earliest wall-clock time the transfer worker
	// should retry a "pending" workflow after a retryable failure (backoff).
	NextAttemptUnixNanos int64 `json:"next_attempt_unix_nanos,omitempty"`
}

type Manifest struct {
	Version           int                     `json:"version"`
	ID                string                  `json:"id"`
	Name              string                  `json:"name,omitempty"`
	State             string                  `json:"state"`
	Interruption      string                  `json:"interruption,omitempty"`
	Device            DeviceIdentity          `json:"device"`
	CanonicalClock    string                  `json:"canonical_clock"`
	BootID            string                  `json:"boot_id"`
	RequestBootNanos  int64                   `json:"request_boottime_nanos"`
	StartedEpisodeNS  int64                   `json:"started_episode_nanos"`
	StartedUnixNanos  int64                   `json:"started_unix_nanos"`
	StoppedEpisodeNS  int64                   `json:"stopped_episode_nanos,omitempty"`
	Trigger           EpisodeTrigger          `json:"trigger"`
	CollectorVersion  string                  `json:"collector_version"`
	ModelVersions     map[string]string       `json:"model_versions"`
	RequestedTopics   []string                `json:"requested_ros2_topics"`
	UTCObservations   []UTCObservation        `json:"utc_observations"`
	Roughtime         []timesync.Consensus    `json:"roughtime_observations,omitempty"`
	SystemClockStatus string                  `json:"system_clock_status"`
	Sources           []SourceStats           `json:"sources"`
	Calibrations      []Calibration           `json:"calibrations"`
	Files             []File                  `json:"files"`
	Privacy           []PrivacyTransformation `json:"privacy_transformations"`
	Upload            WorkflowState           `json:"upload"`
	Labeling          WorkflowState           `json:"labeling"`
	RecoveryActions   []string                `json:"recovery_actions,omitempty"`
	PreRollLost       uint64                  `json:"pre_roll_lost"`
	PreRollAccounting string                  `json:"pre_roll_accounting"`
}

type StartOptions struct {
	Name           string
	Sources        []string
	ExcludeSources []string
	// SourceCaptures carries per-source capture policies keyed by resolved
	// source ID (see Manager.ResolveCampaignSources).
	SourceCaptures        map[string]*SourceCapture
	RequireUTCUncertainty time.Duration
	Calibrations          map[string][]byte
	CalibrationRevisions  map[string]string
	PreRollDuration       time.Duration
	Trigger               EpisodeTrigger
	CollectorVersion      string
	ModelVersions         map[string]string
	RequestedTopics       []string
	Privacy               []PrivacyTransformation
	Upload                WorkflowState
	Labeling              WorkflowState
}

type EpisodeInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	State            string `json:"state"`
	StartedUnixNanos int64  `json:"started_unix_nanos"`
	SizeBytes        int64  `json:"size_bytes"`
	BootID           string `json:"boot_id"`
}
