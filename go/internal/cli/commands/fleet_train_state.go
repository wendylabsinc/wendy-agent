package commands

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// trainFleetTokenEnvKey is the shared secret every training endpoint requires.
// A fleet's coordinator serves model parameters and accepts contributions that
// steer the update, so an endpoint without it lets anyone who can reach the
// port read the model or poison the run.
const trainFleetTokenEnvKey = "WT_FLEET_TOKEN"

// trainState is what a deploy decided, kept so a later status or stop against
// the same fleet agrees with it. It lives under the configuration directory
// rather than the cache because the token is functional state: losing it means
// losing access to a running fleet, not just a slow rebuild.
type trainState struct {
	Token     string `json:"token"`
	AppID     string `json:"appId"`
	Template  string `json:"template"`
	Transport string `json:"transport"`
	MeshPort  int    `json:"meshPort"`
	Group     string `json:"group"`
	UpdatedAt string `json:"updatedAt"`
}

// trainStatePath is the state file for one fleet, keyed by group and
// application id so two training runs on overlapping devices keep their own
// tokens.
func trainStatePath(group, appID string) (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s__%s.json", sanitizeCacheKey(group), sanitizeCacheKey(appID))
	return filepath.Join(dir, "train", name), nil
}

// loadTrainState reads a fleet's saved state. A missing or unreadable file is
// not an error: the caller falls back to flags, or generates a new token.
func loadTrainState(group, appID string) (*trainState, bool) {
	path, err := trainStatePath(group, appID)
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var st trainState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, false
	}
	return &st, true
}

// saveTrainState writes the fleet's state with owner-only permissions, since
// it holds a bearer token.
func saveTrainState(st trainState) error {
	path, err := trainStatePath(st.Group, st.AppID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating training state directory: %w", err)
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// Write then rename so a crash cannot leave a half-written token behind.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing training state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("saving training state: %w", err)
	}
	return nil
}

// newFleetToken returns a fresh 32 character hexadecimal bearer token.
func newFleetToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating fleet token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ensureFleetToken resolves the fleet's bearer token, in precedence order: an
// explicit token from the operator, then the token this fleet already uses,
// then a fresh one.
//
// persist is false for a dry run, which must not leave anything behind; the
// returned ephemeral flag says the rendered token was invented for that render
// alone and will not match a later deploy.
func ensureFleetToken(explicit string, baseEnv map[string]string, group, appID string, persist bool) (token string, ephemeral bool, err error) {
	if explicit == "" {
		explicit = strings.TrimSpace(baseEnv[trainFleetTokenEnvKey])
	}
	if explicit != "" {
		return explicit, false, nil
	}
	if st, ok := loadTrainState(group, appID); ok && st.Token != "" {
		return st.Token, false, nil
	}
	generated, err := newFleetToken()
	if err != nil {
		return "", false, err
	}
	return generated, !persist, nil
}
