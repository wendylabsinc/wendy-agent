package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

const defaultNativeBrewfile = "Brewfile.wendy"

func appendNativeBrewfileSyncEntry(entries []fileSyncEntry, cwd string, appCfg *appconfig.AppConfig) ([]fileSyncEntry, error) {
	entry, err := resolveNativeBrewfileSyncEntry(cwd, appCfg)
	if err != nil || entry == nil {
		return entries, err
	}

	for _, existing := range entries {
		coverage, err := syncEntryCoversBrewfile(existing, *entry)
		if err != nil {
			return entries, err
		}
		if !coverage.covered {
			continue
		}
		if coverage.sameSource {
			return entries, nil
		}
		return entries, fmt.Errorf(
			"brewfile %q conflicts with another synced file at %q; remove the duplicate files entry or point brewfile at the same source",
			appCfg.Brewfile,
			entry.remotePath,
		)
	}

	return append(entries, *entry), nil
}

// resolveNativeBrewfileSyncEntry returns the file-sync entry for a native Mac
// Brewfile and updates appCfg.Brewfile to the exact remote path the agent will
// use after sync.
func resolveNativeBrewfileSyncEntry(cwd string, appCfg *appconfig.AppConfig) (*fileSyncEntry, error) {
	if appCfg == nil {
		return nil, nil
	}

	configured := strings.TrimSpace(appCfg.Brewfile)
	if configured == "" {
		localPath := filepath.Join(cwd, defaultNativeBrewfile)
		if err := checkRegularBrewfile(localPath); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("checking %s: %w", defaultNativeBrewfile, err)
		}
		appCfg.Brewfile = defaultNativeBrewfile
		return &fileSyncEntry{localPath: localPath, remotePath: defaultNativeBrewfile}, nil
	}

	localPath := filepath.Join(cwd, configured)
	if err := checkRegularBrewfile(localPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("brewfile %q declared in wendy.json does not exist", configured)
		}
		return nil, fmt.Errorf("checking brewfile %q: %w", configured, err)
	}

	remotePath := effectiveRemotePath(configured, "")
	if !appconfig.IsSafeRelativeBrewfilePath(remotePath) {
		return nil, fmt.Errorf("brewfile path must be relative and must not contain '.', '..', or empty components")
	}
	appCfg.Brewfile = remotePath
	return &fileSyncEntry{localPath: localPath, remotePath: remotePath}, nil
}

func checkRegularBrewfile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("must be a regular file, got %s", info.Mode().Type())
	}
	return nil
}

type brewfileCoverage struct {
	covered    bool
	sameSource bool
}

func syncEntryCoversBrewfile(existing, brewfile fileSyncEntry) (brewfileCoverage, error) {
	info, err := os.Lstat(existing.localPath)
	if err != nil {
		return brewfileCoverage{}, fmt.Errorf("checking synced file %s: %w", existing.localPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if existing.remotePath == brewfile.remotePath {
			return brewfileCoverage{covered: true, sameSource: false}, nil
		}
		return brewfileCoverage{}, nil
	}

	if !info.IsDir() {
		if existing.remotePath != brewfile.remotePath {
			return brewfileCoverage{}, nil
		}
		same, err := sameLocalFile(existing.localPath, brewfile.localPath)
		if err != nil {
			return brewfileCoverage{}, err
		}
		return brewfileCoverage{covered: true, sameSource: same}, nil
	}

	rel, ok := remotePathRelativeToPrefix(brewfile.remotePath, existing.remotePath)
	if !ok {
		return brewfileCoverage{}, nil
	}

	candidate := filepath.Join(existing.localPath, rel)
	candidateInfo, err := os.Lstat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return brewfileCoverage{}, nil
		}
		return brewfileCoverage{}, fmt.Errorf("checking synced brewfile source %s: %w", candidate, err)
	}
	if !candidateInfo.Mode().IsRegular() {
		return brewfileCoverage{}, nil
	}

	same, err := sameLocalFile(candidate, brewfile.localPath)
	if err != nil {
		return brewfileCoverage{}, err
	}
	currentInfo, err := os.Lstat(existing.localPath)
	if err != nil || !currentInfo.IsDir() || !os.SameFile(info, currentInfo) {
		return brewfileCoverage{}, nil
	}
	return brewfileCoverage{covered: true, sameSource: same}, nil
}

func remotePathRelativeToPrefix(remotePath, prefix string) (string, bool) {
	remotePath = strings.TrimPrefix(remotePath, "./")
	prefix = strings.TrimPrefix(prefix, "./")
	if prefix == "" {
		return remotePath, remotePath != ""
	}
	if remotePath == prefix {
		return "", false
	}
	prefixWithSlash := prefix + "/"
	if !strings.HasPrefix(remotePath, prefixWithSlash) {
		return "", false
	}
	return strings.TrimPrefix(remotePath, prefixWithSlash), true
}

func sameLocalFile(a, b string) (bool, error) {
	aInfo, err := os.Lstat(a)
	if err != nil {
		return false, fmt.Errorf("checking synced brewfile source %s: %w", a, err)
	}
	if !aInfo.Mode().IsRegular() {
		return false, nil
	}
	bInfo, err := os.Lstat(b)
	if err != nil {
		return false, fmt.Errorf("checking brewfile source %s: %w", b, err)
	}
	if !bInfo.Mode().IsRegular() {
		return false, nil
	}
	return os.SameFile(aInfo, bInfo), nil
}
