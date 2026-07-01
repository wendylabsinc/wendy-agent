// Package config manages the CLI configuration stored at ~/.wendy/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config represents the top-level CLI configuration.
type Config struct {
	Auth          []AuthConfig     `json:"auth,omitempty"`
	Analytics     *AnalyticsConfig `json:"analytics,omitempty"`
	DefaultDevice string           `json:"defaultDevice,omitempty"`
	// DefaultCloudGRPC names the auth session (by its gRPC endpoint) used when
	// several sessions exist and no --cloud-grpc flag is given. Empty means no
	// default; resolution then falls back to an interactive picker or an error.
	DefaultCloudGRPC   string `json:"defaultCloudGRPC,omitempty"`
	LastCLIUpdateCheck string `json:"lastCLIUpdateCheck,omitempty"` // RFC3339
	AvailableCLIUpdate string `json:"availableCLIUpdate,omitempty"` // tag of a newer release, if any
	// LastMCPSetupVersion records the CLI version that last ran `wendy mcp
	// setup`. It lets the root command detect when an upgrade should refresh
	// the MCP server config and bundled skills. Empty means the user has never
	// run setup, so auto-refresh stays off.
	LastMCPSetupVersion string `json:"lastMCPSetupVersion,omitempty"`
	// CompletionInstalled is set once shell completions have been installed
	// through the CLI (via `wendy completion install` or an accepted prompt).
	// While false, the CLI may offer to install completions.
	CompletionInstalled bool `json:"completionInstalled,omitempty"`
	// CompletionPromptDismissed is set when the user declines the ambient
	// "install completions?" prompt with "n". Once true, that prompt never
	// reappears.
	CompletionPromptDismissed bool `json:"completionPromptDismissed,omitempty"`
	// LastCompletionPromptCheck records when the ambient completion prompt was
	// last shown (RFC3339). It throttles the prompt so an unanswered prompt
	// (e.g. Ctrl-C) doesn't reappear on every invocation.
	LastCompletionPromptCheck string `json:"lastCompletionPromptCheck,omitempty"`
	// OptimizeTipShownAt throttles the `wendy project optimize` tip to once per
	// day per project. Keyed by the project directory, value is an RFC3339 date
	// (YYYY-MM-DD) of the last time the tip (or a build-time optimize scan) was
	// surfaced for that project.
	OptimizeTipShownAt map[string]string `json:"optimizeTipShownAt,omitempty"`
	// DevicePins binds a device hostname to the organisation + cloud host its
	// TLS identity must belong to (WDY-1149), so a different trust domain
	// answering at that hostname is caught. Renewal/re-enrollment within the
	// same org+cloud does not trip it. Keyed by normalized hostname.
	DevicePins map[string]DevicePin `json:"devicePins,omitempty"`
	// DefaultOrgID is the organization used when a command needs to target a
	// specific org and the user belongs to more than one. Zero means no default;
	// the CLI will then show a picker or use the sole available org.
	DefaultOrgID int32 `json:"defaultOrgId,omitempty"`
}

// AuthConfig holds authentication details for a cloud environment.
type AuthConfig struct {
	CloudDashboard string            `json:"cloudDashboard"`
	CloudGRPC      string            `json:"cloudGRPC"`
	APIKey         string            `json:"apiKey,omitempty"`
	Certificates   []CertificateInfo `json:"certificates,omitempty"`
}

// CertificateInfo holds certificate material for mTLS authentication.
type CertificateInfo struct {
	PemCertificate      string `json:"pemCertificate,omitempty"`
	PemCertificateChain string `json:"pemCertificateChain,omitempty"`
	PemPrivateKey       string `json:"pemPrivateKey,omitempty"`
	OrganizationID      int    `json:"organizationId"`
	UserID              string `json:"userId,omitempty"`
	AssetID             int    `json:"assetId,omitempty"`
}

// AnalyticsConfig holds analytics preferences.
type AnalyticsConfig struct {
	Enabled bool `json:"enabled"`
}

// ConfigDir returns the path to the ~/.wendy directory, creating it if necessary.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}

	dir := filepath.Join(home, ".wendy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}

	return dir, nil
}

// CacheDir returns the platform-appropriate cache directory for wendy, creating
// it if necessary.
//
//   - macOS:   ~/Library/Caches/wendy
//   - Linux:   $XDG_CACHE_HOME/wendy  (falls back to ~/.cache/wendy)
func CacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determining cache directory: %w", err)
	}

	cacheDir := filepath.Join(dir, "wendy")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("creating cache directory: %w", err)
	}

	return cacheDir, nil
}

// LogDir returns the directory for CLI-written log files (e.g. the Thor flash log),
// creating it if needed.
func LogDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determining log directory: %w", err)
	}
	logDir := filepath.Join(dir, "wendy", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", fmt.Errorf("creating log directory: %w", err)
	}
	return logDir, nil
}

// configPath returns the full path to config.json.
func configPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the CLI configuration from ~/.wendy/config.json.
// If the file does not exist, an empty Config is returned without error.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

// Save writes the configuration to ~/.wendy/config.json.
func Save(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// authEntryOrgID returns the organization ID from the first certificate in an
// auth entry, or 0 if none is present.
func authEntryOrgID(a AuthConfig) int {
	if len(a.Certificates) > 0 {
		return a.Certificates[0].OrganizationID
	}
	return 0
}

// AddAuth adds or replaces an auth entry. Matching is by (cloudDashboard,
// cloudGRPC, orgID) so that multiple orgs on the same cloud endpoint each
// keep their own entry instead of overwriting one another.
func (c *Config) AddAuth(auth AuthConfig) {
	incomingOrg := authEntryOrgID(auth)
	for i, existing := range c.Auth {
		if existing.CloudDashboard == auth.CloudDashboard &&
			existing.CloudGRPC == auth.CloudGRPC &&
			authEntryOrgID(existing) == incomingOrg {
			c.Auth[i] = auth
			return
		}
	}
	c.Auth = append(c.Auth, auth)
}
