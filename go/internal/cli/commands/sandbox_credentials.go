package commands

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sandboxAdminCredentials is the exact JSON shape desktop-native's
// AdminCredentialStore reads/writes (Sources/WendySandbox/AdminCredentials.swift)
// — plain "user"/"password" keys, no key transformation.
type sandboxAdminCredentials struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

func sandboxCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "WendySandboxNative", "admin-credentials.json"), nil
}

func readOrGenerateSandboxCredentials() (sandboxAdminCredentials, error) {
	path, err := sandboxCredentialsPath()
	if err != nil {
		return sandboxAdminCredentials{}, err
	}
	return readOrGenerateSandboxCredentialsAt(path)
}

// readOrGenerateSandboxCredentialsAt reads path if it already holds valid
// credentials (written by either the Swift app or a prior CLI run), or
// generates and persists a fresh one — so whichever side runs first defines
// the shared secret and the other always reads it back.
func readOrGenerateSandboxCredentialsAt(path string) (sandboxAdminCredentials, error) {
	if data, err := os.ReadFile(path); err == nil {
		var creds sandboxAdminCredentials
		if err := json.Unmarshal(data, &creds); err == nil && creds.User != "" && creds.Password != "" {
			return creds, nil
		}
	}
	password, err := generateSandboxPassword()
	if err != nil {
		return sandboxAdminCredentials{}, fmt.Errorf("generating admin password: %w", err)
	}
	creds := sandboxAdminCredentials{User: "admin", Password: password}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return sandboxAdminCredentials{}, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return sandboxAdminCredentials{}, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return sandboxAdminCredentials{}, fmt.Errorf("writing %s: %w", path, err)
	}
	return creds, nil
}

// generateSandboxPassword matches AdminCredentialStore.randomPassword in
// desktop-native: 18 random bytes, base64 URL-safe, no padding.
func generateSandboxPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
