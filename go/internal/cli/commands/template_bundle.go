package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxTemplateBundleSize = 128 << 20

var templateBundleBaseURL = "https://templates.wendy.dev"
var templateBundleClient = &http.Client{Timeout: 30 * time.Second}
var templateCacheRoot = func() (string, error) {
	root, err := os.UserCacheDir()
	return filepath.Join(root, "wendy", "templates"), err
}

type templateBundle struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type templateBundleIndex struct {
	Version  int                       `json:"version"`
	Revision string                    `json:"revision"`
	Catalog  repoMeta                  `json:"catalog"`
	Bundles  map[string]templateBundle `json:"bundles"`
}

func templateBranchURL(branch string) string {
	parts := strings.Split(resolveTemplateBranch(branch), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.TrimRight(templateBundleBaseURL, "/") + "/" + strings.Join(parts, "/")
}

func templateIndexCachePath(branch string) string {
	root, err := templateCacheRoot()
	if err != nil {
		return ""
	}
	key := sha256.Sum256([]byte(templateBranchURL(branch)))
	return filepath.Join(root, "indexes", hex.EncodeToString(key[:])+".json")
}

func parseTemplateIndex(data []byte) (*templateBundleIndex, error) {
	var index templateBundleIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	if index.Version != 1 || index.Revision == "" || len(index.Catalog.Templates) == 0 || len(index.Bundles) == 0 {
		return nil, fmt.Errorf("unsupported or incomplete template bundle index")
	}
	return &index, nil
}

// A nil index means this branch predates bundle publishing. Keep the GitHub
// archive fallback so branch previews and older template repositories work.
func fetchTemplateIndex(ctx context.Context, branch string) (*templateBundleIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cachePath := templateIndexCachePath(branch)
	data, _ := os.ReadFile(cachePath)
	cached, _ := parseTemplateIndex(data)
	if cached != nil {
		if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < 5*time.Minute {
			return cached, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, templateBranchURL(branch)+"/template-index.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := templateBundleClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cached != nil {
			cliNotice("Offline: using cached templates at revision %s.", cached.Revision)
			return cached, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 && cached != nil {
			cliNotice("Template server unavailable: using cached revision %s.", cached.Revision)
			return cached, nil
		}
		return nil, fmt.Errorf("fetching template index: HTTP %d", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("template index exceeds 4 MiB")
	}
	index, err := parseTemplateIndex(data)
	if err != nil {
		return nil, err
	}
	cacheTemplateFile(cachePath, data)
	return index, nil
}

// Cache writes are best-effort and atomic; concurrent init processes must never
// read half an index/archive. An unwritable cache must not prevent scaffolding.
func cacheTemplateFile(path string, data []byte) {
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".template-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(f.Name(), path)
	}
}

func validTemplateBundle(bundle templateBundle) bool {
	digest, err := hex.DecodeString(bundle.SHA256)
	return err == nil && len(digest) == sha256.Size && bundle.Size > 0 && bundle.Size <= maxTemplateBundleSize && bundle.Path == "bundles/"+bundle.SHA256+".tar.gz"
}

func verifyTemplateBundle(data []byte, bundle templateBundle) bool {
	digest := sha256.Sum256(data)
	return int64(len(data)) == bundle.Size && hex.EncodeToString(digest[:]) == bundle.SHA256
}

func downloadTemplateBundle(ctx context.Context, index *templateBundleIndex, language, name, branch string, progress progressCallback) (map[string][]byte, *templateManifest, error) {
	bundle, ok := index.Bundles[language+"/"+name]
	if !ok || !validTemplateBundle(bundle) {
		return nil, nil, fmt.Errorf("invalid or missing bundle for %s/%s at revision %s", language, name, index.Revision)
	}
	cachePath := ""
	if root, err := templateCacheRoot(); err == nil {
		cachePath = filepath.Join(root, "bundles", bundle.SHA256+".tar.gz")
	}
	if data, err := os.ReadFile(cachePath); err == nil && verifyTemplateBundle(data, bundle) {
		if progress != nil {
			progress(bundle.Size, bundle.Size)
		}
		return extractTemplateArchive(bytes.NewReader(data), language, name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, templateBranchURL(branch)+"/"+bundle.Path, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := templateBundleClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading template bundle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("downloading template bundle: HTTP %d", resp.StatusCode)
	}
	var reader io.Reader = io.LimitReader(resp.Body, bundle.Size+1)
	if progress != nil {
		reader = &progressReader{r: reader, total: bundle.Size, onProgress: progress}
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, err
	}
	if !verifyTemplateBundle(data, bundle) {
		return nil, nil, fmt.Errorf("template bundle checksum or size mismatch")
	}
	files, manifest, err := extractTemplateArchive(bytes.NewReader(data), language, name)
	if err != nil {
		return nil, nil, err
	}
	cacheTemplateFile(cachePath, data)
	return files, manifest, nil
}

func templateFetchCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
