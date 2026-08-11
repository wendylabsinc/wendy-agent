package clitimesync

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// proofCacheFile holds the most recent Roughtime proof this host obtained, so a
// device can still be given a verified time when the host has no route to a
// Roughtime server — the case that matters, because a device whose clock is too
// far behind to accept our certificate is usually offline and so are we.
//
// Replaying an old proof cannot harm the device: the agent's AdvanceTo only ever
// moves the clock forward, so a stale proof either advances it (good) or does
// nothing. Moving a clock backward, or forward past the true time, would require
// the Roughtime server's Ed25519 key.
//
// A cached proof only has to be as fresh as the certificate it is used with. Both
// are obtained at login, so the proof asserts a time at or after the cert's
// NotBefore — exactly the advance needed for that cert to satisfy a strict
// NotBefore check on the device.
const proofCacheFile = "timeproof.json"

type cachedProof struct {
	// Packet is the encoded WendyDatagram the agent verifies, base64 for JSON.
	Packet string `json:"packet"`
	// MidpointUnix and Server describe the proof for display only; the device
	// re-derives both from Packet and verifies the signature itself.
	MidpointUnix int64  `json:"midpointUnix"`
	Server       string `json:"server"`
	FetchedUnix  int64  `json:"fetchedUnix"`
}

func proofCachePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, proofCacheFile), nil
}

// storeProof persists pkt so a later run without network can still broadcast it.
// Best-effort: a cache we cannot write costs nothing today.
func storeProof(pkt []byte, midpoint time.Time, server string) error {
	path, err := proofCachePath()
	if err != nil {
		return err
	}
	body, err := json.Marshal(cachedProof{
		Packet:       base64.StdEncoding.EncodeToString(pkt),
		MidpointUnix: midpoint.Unix(),
		Server:       server,
		FetchedUnix:  time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadProof returns the cached proof packet and the time it asserts.
func loadProof() (pkt []byte, midpoint time.Time, server string, err error) {
	path, err := proofCachePath()
	if err != nil {
		return nil, time.Time{}, "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, "", err
	}
	var c cachedProof
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, time.Time{}, "", fmt.Errorf("parsing cached time proof: %w", err)
	}
	pkt, err = base64.StdEncoding.DecodeString(c.Packet)
	if err != nil {
		return nil, time.Time{}, "", fmt.Errorf("decoding cached time proof: %w", err)
	}
	if len(pkt) == 0 {
		return nil, time.Time{}, "", fmt.Errorf("cached time proof is empty")
	}
	return pkt, time.Unix(c.MidpointUnix, 0), c.Server, nil
}
