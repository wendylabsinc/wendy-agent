package clitimesync

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

// proofCacheFile holds the most recent Roughtime proof this host obtained, for
// hosts with no route to a Roughtime server. Replaying it cannot harm a device:
// the agent only ever advances its clock, and moving a clock backward or past the
// true time would require the server's Ed25519 key.
//
// The proof only has to be as fresh as the certificate it is used with. Both are
// obtained at login, so the proof asserts a time at or after the cert's
// NotBefore — the advance that cert needs to pass a strict NotBefore check.
const proofCacheFile = "timeproof.json"

// The server's own fields are cached, not the encoded datagram: the wire format
// identifies the server by its index in timesync.Servers, so a cached packet
// would name the wrong verification key after that list is reordered.
type cachedProof struct {
	Server      string `json:"server"`
	Nonce       string `json:"nonce"`       // base64
	RawResponse string `json:"rawResponse"` // base64
	MidpointMS  int64  `json:"midpointMs"`
	RadiusMS    int64  `json:"radiusMs"`
	FetchedUnix int64  `json:"fetchedUnix"`
}

func proofCachePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, proofCacheFile), nil
}

// storeProof persists result so a later run without network can still broadcast
// it. Best-effort: a cache we cannot write costs nothing today.
func storeProof(result roughtime.Result, now time.Time) error {
	path, err := proofCachePath()
	if err != nil {
		return err
	}
	body, err := json.Marshal(cachedProof{
		Server:      result.Server,
		Nonce:       base64.StdEncoding.EncodeToString(result.Nonce),
		RawResponse: base64.StdEncoding.EncodeToString(result.RawResponse),
		MidpointMS:  result.Midpoint.UnixMilli(),
		RadiusMS:    result.Radius.Milliseconds(),
		FetchedUnix: now.Unix(),
	})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".timeproof-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename below succeeds
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// loadProof returns the cached proof and when it was obtained. A proof missing
// the fields needed to rebuild the datagram is reported as an error rather than
// relayed as an unverifiable packet.
func loadProof() (result roughtime.Result, fetchedAt time.Time, err error) {
	path, err := proofCachePath()
	if err != nil {
		return roughtime.Result{}, time.Time{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return roughtime.Result{}, time.Time{}, err
	}
	var c cachedProof
	if err := json.Unmarshal(body, &c); err != nil {
		return roughtime.Result{}, time.Time{}, fmt.Errorf("parsing cached time proof: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(c.Nonce)
	if err != nil {
		return roughtime.Result{}, time.Time{}, fmt.Errorf("decoding cached nonce: %w", err)
	}
	response, err := base64.StdEncoding.DecodeString(c.RawResponse)
	if err != nil {
		return roughtime.Result{}, time.Time{}, fmt.Errorf("decoding cached response: %w", err)
	}
	if len(nonce) == 0 || len(response) == 0 || c.Server == "" {
		return roughtime.Result{}, time.Time{}, fmt.Errorf("cached time proof is incomplete")
	}
	return roughtime.Result{
		Server:      c.Server,
		Nonce:       nonce,
		RawResponse: response,
		Midpoint:    time.UnixMilli(c.MidpointMS),
		Radius:      time.Duration(c.RadiusMS) * time.Millisecond,
	}, time.Unix(c.FetchedUnix, 0), nil
}
