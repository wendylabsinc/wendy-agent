package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

const (
	templateRepoOwner    = "wendylabsinc"
	templateRepoName     = "templates"
	templateRepoBranch   = "main"
	templateHostedBucket = "wendy-templates-public"
)

var (
	templateArchiveAttemptTimeout   = 2 * time.Minute
	templateArchiveMaxAttempts      = 3
	templateArchiveRetryDelay       = 750 * time.Millisecond
	templateArchiveBaseURL          = "https://codeload.github.com"
	templateRawBaseURL              = "https://raw.githubusercontent.com"
	templateHostedBaseURL           = "https://templates.wendy.dev"
	templateHostedObjectsAPIBaseURL = "https://storage.googleapis.com/storage/v1/b"
	templateLanguageProbeClient     = &http.Client{Timeout: 10 * time.Second}
)

// resolveTemplateBranch returns branch if non-empty, otherwise the default branch.
func resolveTemplateBranch(branch string) string {
	if branch == "" {
		return templateRepoBranch
	}
	return branch
}

func templateGitHubRawFileURL(branch string, pathParts ...string) string {
	parts := append([]string{templateRepoOwner, templateRepoName, resolveTemplateBranch(branch)}, pathParts...)
	return strings.TrimRight(templateRawBaseURL, "/") + "/" + escapedURLPath(parts...)
}

func templateGitHubArchiveURL(branch string) string {
	return strings.TrimRight(templateArchiveBaseURL, "/") + "/" + escapedURLPath(
		templateRepoOwner,
		templateRepoName,
		"tar.gz",
		"refs",
		"heads",
		resolveTemplateBranch(branch),
	)
}

func templateHostedMetaURL(branch string) string {
	return templateHostedURL(resolveTemplateBranch(branch), "meta.json")
}

func templateHostedTemplateURL(branch, language, templateName string) string {
	return templateHostedURL(resolveTemplateBranch(branch), language, templateName) + "/"
}

func templateHostedTemplateFileURL(branch, language, templateName, relPath string) string {
	return templateHostedURL(resolveTemplateBranch(branch), language, templateName, relPath)
}

func templateHostedURL(parts ...string) string {
	return strings.TrimRight(templateHostedBaseURL, "/") + "/" + escapedURLPath(parts...)
}

func templateHostedObjectURL(objectName string) string {
	return templateHostedURL(objectName)
}

func templateHostedObjectListURL(prefix, pageToken string) string {
	values := url.Values{}
	values.Set("prefix", prefix)
	if pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	return strings.TrimRight(templateHostedObjectsAPIBaseURL, "/") + "/" + url.PathEscape(templateHostedBucket) + "/o?" + values.Encode()
}

func templateHostedObjectPrefix(branch, language, templateName string) string {
	return strings.Trim(resolveTemplateBranch(branch), "/") + "/" + strings.Trim(language, "/") + "/" + strings.Trim(templateName, "/") + "/"
}

func escapedURLPath(parts ...string) string {
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		for _, segment := range strings.Split(part, "/") {
			if segment == "" {
				continue
			}
			segments = append(segments, url.PathEscape(segment))
		}
	}
	return strings.Join(segments, "/")
}

func shouldFallbackToHostedFiles(ctx context.Context, err error) bool {
	return err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled)
}

func logGitHubTemplateFallback(err error, hostedURL string) {
	cliLogln("%v fetching from github, falling back to hosted files in %s", err, hostedURL)
}

// repoMeta is the parsed meta.json from the templates repo root.
type repoMeta struct {
	Templates []repoMetaTemplate `json:"templates"`
	Languages []repoMetaLanguage `json:"languages"`
}

type repoMetaTemplate struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Targets     []string `json:"targets"`   // optional; empty means all targets
	Languages   []string `json:"languages"` // optional; empty means discover from repo layout
}

type repoMetaLanguage struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// templateManifest is the parsed template.json inside a specific template dir.
type templateManifest struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Variables   []templateVariable `json:"variables"`
	Schema      *templateSchema    `json:"-"` // populated from template.schema.json
}

// templateSchema is the parsed template.schema.json — defines multi-phase
// configuration questions whose answers become template variables.
type templateSchema struct {
	Phases []templateSchemaPhase `json:"phases"`
}

// templateSchemaPhase groups a set of questions under an optional condition.
type templateSchemaPhase struct {
	ID        string                   `json:"id"`
	Title     string                   `json:"title"`
	Questions []templateSchemaQuestion `json:"questions"`
	When      *templateSchemaCondition `json:"when,omitempty"`
}

// templateSchemaQuestion is a single question shown to the user.
// Type is one of "radio", "checkbox", or "input".
type templateSchemaQuestion struct {
	ID       string                   `json:"id"`
	Label    string                   `json:"label"`
	Type     string                   `json:"type"`
	Options  []templateSchemaOption   `json:"options,omitempty"`
	When     *templateSchemaCondition `json:"when,omitempty"`
	Required bool                     `json:"required"`
	Default  string                   `json:"default,omitempty"`
	Secret   bool                     `json:"secret,omitempty"`
}

// templateSchemaOption is a single selectable choice in a radio or checkbox question.
type templateSchemaOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Size        string `json:"size,omitempty"`
	Parameters  string `json:"parameters,omitempty"`
	Comments    string `json:"comments,omitempty"`
}

// templateSchemaCondition controls whether a phase or question is shown,
// based on a previously answered question's value.
// Exactly one of Equals, In, or Contains should be set.
type templateSchemaCondition struct {
	QuestionID string   `json:"questionId"`
	Equals     *string  `json:"equals,omitempty"`
	In         []string `json:"in,omitempty"`
	Contains   *string  `json:"contains,omitempty"`
}

// templateVariable declares a single template variable.
type templateVariable struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Type        string                 `json:"type"` // "string", "integer", "boolean"
	Default     interface{}            `json:"default"`
	Required    bool                   `json:"required"`
	Prompt      string                 `json:"prompt"`
	Validate    map[string]interface{} `json:"validate"`
}

// fetchRepoMeta downloads and parses meta.json from the templates repo.
// If branch is empty, it defaults to templateRepoBranch ("main").
// If ctx is cancelled, the in-flight request is aborted.
func fetchRepoMeta(ctx context.Context, branch string) (*repoMeta, error) {
	branch = resolveTemplateBranch(branch)
	githubURL := templateGitHubRawFileURL(branch, "meta.json")
	meta, err := fetchRepoMetaFromURL(ctx, githubURL, branch)
	if err == nil {
		return meta, nil
	}
	if !shouldFallbackToHostedFiles(ctx, err) {
		return nil, err
	}

	hostedURL := templateHostedMetaURL(branch)
	logGitHubTemplateFallback(err, hostedURL)
	meta, hostedErr := fetchRepoMetaFromURL(ctx, hostedURL, branch)
	if hostedErr != nil {
		return nil, fmt.Errorf("%w; hosted fallback failed: %v", err, hostedErr)
	}
	return meta, nil
}

func fetchRepoMetaFromURL(ctx context.Context, url, branch string) (*repoMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching template registry (branch %q): %w", branch, err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching template registry (branch %q): %w", branch, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("template registry not found for branch %q — check that the branch exists in %s/%s",
			branch, templateRepoOwner, templateRepoName)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching template registry (branch %q): HTTP %d", branch, resp.StatusCode)
	}

	var meta repoMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, fmt.Errorf("parsing template registry: %w", err)
	}
	return &meta, nil
}

// isTemplateLanguage checks if a language key exists in the meta.
func isTemplateLanguage(language string, meta *repoMeta) bool {
	for _, l := range meta.Languages {
		if l.Key == language {
			return true
		}
	}
	return false
}

func templateByName(meta *repoMeta, templateName string) (*repoMetaTemplate, bool) {
	if meta == nil {
		return nil, false
	}
	for i := range meta.Templates {
		if meta.Templates[i].Name == templateName {
			return &meta.Templates[i], true
		}
	}
	return nil, false
}

func templateLanguagesForTemplate(ctx context.Context, meta *repoMeta, templateName, branch string) ([]repoMetaLanguage, error) {
	tmpl, ok := templateByName(meta, templateName)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", templateName)
	}

	if len(tmpl.Languages) > 0 {
		return repoMetaLanguagesForKeys(meta, tmpl.Languages), nil
	}

	return probeTemplateLanguages(ctx, meta.Languages, templateName, branch)
}

func repoMetaLanguagesForKeys(meta *repoMeta, keys []string) []repoMetaLanguage {
	if meta == nil || len(keys) == 0 {
		return nil
	}

	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}

	languages := make([]repoMetaLanguage, 0, len(keys))
	for _, language := range meta.Languages {
		if _, ok := allowed[language.Key]; ok {
			languages = append(languages, language)
		}
	}
	return languages
}

func probeTemplateLanguages(ctx context.Context, languages []repoMetaLanguage, templateName, branch string) ([]repoMetaLanguage, error) {
	branch = resolveTemplateBranch(branch)

	available := make([]repoMetaLanguage, 0, len(languages))
	for _, language := range languages {
		ok, err := probeTemplateLanguage(ctx, branch, language.Key, templateName)
		if err != nil {
			return nil, err
		}
		if ok {
			available = append(available, language)
		}
	}
	return available, nil
}

func probeTemplateLanguage(ctx context.Context, branch, language, templateName string) (bool, error) {
	branch = resolveTemplateBranch(branch)
	githubURL := templateLanguageManifestURL(branch, language, templateName)
	ok, err := probeTemplateLanguageAtURL(ctx, branch, githubURL)
	if err == nil {
		return ok, nil
	}
	if !shouldFallbackToHostedFiles(ctx, err) {
		return false, err
	}

	hostedURL := templateHostedTemplateFileURL(branch, language, templateName, "template.json")
	logGitHubTemplateFallback(err, templateHostedTemplateURL(branch, language, templateName))
	return probeTemplateLanguageAtURL(ctx, branch, hostedURL)
}

func probeTemplateLanguageAtURL(ctx context.Context, branch, manifestURL string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return false, fmt.Errorf("checking template language availability: %w", err)
	}

	resp, err := templateLanguageProbeClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("checking template language availability (branch %q): %w", branch, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("checking template language availability (branch %q): HTTP %d", branch, resp.StatusCode)
	}
}

func templateLanguageManifestURL(branch, language, templateName string) string {
	return templateGitHubRawFileURL(branch, language, templateName, "template.json")
}

// progressCallback reports download progress. total is the expected content
// length in bytes (0 if unknown); written is the cumulative number of bytes
// read from the response body so far.
type progressCallback func(written, total int64)

type progressReader struct {
	r          io.Reader
	total      int64
	written    int64
	onProgress progressCallback
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 {
		pr.written += int64(n)
		if pr.onProgress != nil {
			pr.onProgress(pr.written, pr.total)
		}
	}
	return n, err
}

// downloadTemplateArchive fetches the templates repo tarball and extracts
// the files for {language}/{templateName}/ into a map of relative path -> content.
// It also returns the parsed template.json manifest.
// If branch is empty, it defaults to templateRepoBranch ("main").
// If onProgress is non-nil, it is invoked as the response body is read.
// If ctx is cancelled, the in-flight request is aborted.
func downloadTemplateArchive(ctx context.Context, language, templateName, branch string, onProgress progressCallback) (map[string][]byte, *templateManifest, error) {
	branch = resolveTemplateBranch(branch)
	// Use codeload directly to avoid an extra redirect through github.com for the
	// repository archive download.
	githubURL := templateGitHubArchiveURL(branch)
	files, manifest, err := downloadTemplateArchiveFromURL(ctx, githubURL, branch, language, templateName, onProgress)
	if err == nil {
		return files, manifest, nil
	}
	if !shouldFallbackToHostedFiles(ctx, err) {
		return nil, nil, err
	}

	hostedURL := templateHostedTemplateURL(branch, language, templateName)
	logGitHubTemplateFallback(err, hostedURL)
	files, manifest, hostedErr := downloadTemplateHostedFiles(ctx, branch, language, templateName, onProgress)
	if hostedErr != nil {
		return nil, nil, fmt.Errorf("%w; hosted fallback failed: %v", err, hostedErr)
	}
	return files, manifest, nil
}

// downloadTemplateArchiveFromURL is the testable core of downloadTemplateArchive:
// it performs the HTTP GET against the caller-supplied URL and delegates
// tarball parsing to extractTemplateArchive.
func downloadTemplateArchiveFromURL(ctx context.Context, url, branch, language, templateName string, onProgress progressCallback) (map[string][]byte, *templateManifest, error) {
	var lastErr error
	for attempt := 1; attempt <= templateArchiveMaxAttempts; attempt++ {
		files, manifest, err := downloadTemplateArchiveAttempt(ctx, url, branch, language, templateName, onProgress)
		if err == nil {
			return files, manifest, nil
		}
		lastErr = err

		if ctx.Err() != nil || attempt == templateArchiveMaxAttempts || !shouldRetryTemplateArchiveError(err) {
			return nil, nil, err
		}

		if err := waitForTemplateArchiveRetry(ctx, templateArchiveRetryDelay); err != nil {
			return nil, nil, err
		}
	}

	return nil, nil, lastErr
}

func downloadTemplateArchiveAttempt(ctx context.Context, url, branch, language, templateName string, onProgress progressCallback) (map[string][]byte, *templateManifest, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, templateArchiveAttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading template (branch %q): %w", branch, err)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("downloading template (branch %q): %w", branch, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, fmt.Errorf("template archive not found for branch %q — check that the branch exists in %s/%s",
			branch, templateRepoOwner, templateRepoName)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("downloading template (branch %q): HTTP %d", branch, resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if onProgress != nil {
		// Normalize ContentLength to the progressCallback contract:
		// http.Response.ContentLength is -1 when unknown, but callers expect 0.
		total := resp.ContentLength
		if total < 0 {
			total = 0
		}
		reader = &progressReader{
			r:          resp.Body,
			total:      total,
			onProgress: onProgress,
		}
	}

	return extractTemplateArchive(reader, language, templateName)
}

func shouldRetryTemplateArchiveError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	msg := err.Error()
	return strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "while reading body")
}

func waitForTemplateArchiveRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func extractTemplateArchive(r io.Reader, language, templateName string) (map[string][]byte, *templateManifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("decompressing template archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// The tarball has a top-level dir like "templates-main/".
	// We want files under "templates-main/{language}/{templateName}/".
	prefix := language + "/" + templateName + "/"

	files := make(map[string][]byte)
	var manifest *templateManifest
	var schema *templateSchema

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading template archive: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Strip the top-level directory.
		name := header.Name
		slashIdx := strings.Index(name, "/")
		if slashIdx < 0 {
			continue
		}
		name = name[slashIdx+1:]

		if !strings.HasPrefix(name, prefix) {
			continue
		}

		relPath := strings.TrimPrefix(name, prefix)
		if relPath == "" {
			continue
		}

		// Sanitize: reject path traversal.
		cleaned := filepath.Clean(relPath)
		if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
			continue
		}
		relPath = cleaned

		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, fmt.Errorf("reading file %s: %w", relPath, err)
		}

		if relPath == "template.json" {
			var m templateManifest
			if err := json.Unmarshal(content, &m); err != nil {
				return nil, nil, fmt.Errorf("parsing template.json: %w", err)
			}
			manifest = &m
			continue // don't include template.json in output files
		}

		if relPath == "template.schema.json" {
			var s templateSchema
			if err := json.Unmarshal(content, &s); err != nil {
				return nil, nil, fmt.Errorf("parsing template.schema.json: %w", err)
			}
			schema = &s
			continue // don't include template.schema.json in output files
		}

		files[relPath] = content
	}

	if manifest == nil {
		return nil, nil, fmt.Errorf("template %q not found for language %q (no template.json)", templateName, language)
	}

	manifest.Schema = schema
	return files, manifest, nil
}

type hostedTemplateObject struct {
	Name string `json:"name"`
	Size string `json:"size"`
}

type hostedTemplateObjectList struct {
	Items         []hostedTemplateObject `json:"items"`
	NextPageToken string                 `json:"nextPageToken"`
}

func downloadTemplateHostedFiles(ctx context.Context, branch, language, templateName string, onProgress progressCallback) (map[string][]byte, *templateManifest, error) {
	prefix := templateHostedObjectPrefix(branch, language, templateName)
	objects, err := listHostedTemplateObjects(ctx, prefix)
	if err != nil {
		return nil, nil, err
	}
	if len(objects) == 0 {
		return nil, nil, fmt.Errorf("template %q not found for language %q (no hosted files at %s)",
			templateName, language, templateHostedTemplateURL(branch, language, templateName))
	}

	total := hostedTemplateObjectsSize(objects)
	var written int64
	files := make(map[string][]byte)
	var manifest *templateManifest
	var schema *templateSchema

	for _, object := range objects {
		relPath, ok := hostedTemplateRelPath(prefix, object.Name)
		if !ok {
			continue
		}

		content, err := downloadHostedTemplateObject(ctx, object.Name, written, total, onProgress)
		if err != nil {
			return nil, nil, err
		}
		written += int64(len(content))

		if relPath == "template.json" {
			var m templateManifest
			if err := json.Unmarshal(content, &m); err != nil {
				return nil, nil, fmt.Errorf("parsing template.json: %w", err)
			}
			manifest = &m
			continue
		}

		if relPath == "template.schema.json" {
			var s templateSchema
			if err := json.Unmarshal(content, &s); err != nil {
				return nil, nil, fmt.Errorf("parsing template.schema.json: %w", err)
			}
			schema = &s
			continue
		}

		files[relPath] = content
	}

	if manifest == nil {
		return nil, nil, fmt.Errorf("template %q not found for language %q (no template.json)", templateName, language)
	}

	manifest.Schema = schema
	return files, manifest, nil
}

func listHostedTemplateObjects(ctx context.Context, prefix string) ([]hostedTemplateObject, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var objects []hostedTemplateObject
	pageToken := ""

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, templateHostedObjectListURL(prefix, pageToken), nil)
		if err != nil {
			return nil, fmt.Errorf("listing hosted template files: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing hosted template files at %s: %w", templateHostedURL(prefix), err)
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("listing hosted template files at %s: HTTP %d", templateHostedURL(prefix), resp.StatusCode)
		}

		var listing hostedTemplateObjectList
		decodeErr := json.NewDecoder(resp.Body).Decode(&listing)
		closeErr := resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("parsing hosted template file listing: %w", decodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("reading hosted template file listing: %w", closeErr)
		}

		objects = append(objects, listing.Items...)
		if listing.NextPageToken == "" {
			return objects, nil
		}
		pageToken = listing.NextPageToken
	}
}

func hostedTemplateObjectsSize(objects []hostedTemplateObject) int64 {
	var total int64
	for _, object := range objects {
		size, err := strconv.ParseInt(object.Size, 10, 64)
		if err != nil {
			return 0
		}
		total += size
	}
	return total
}

func hostedTemplateRelPath(prefix, objectName string) (string, bool) {
	if !strings.HasPrefix(objectName, prefix) {
		return "", false
	}

	relPath := strings.TrimPrefix(objectName, prefix)
	if relPath == "" {
		return "", false
	}

	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", false
	}
	return cleaned, true
}

func downloadHostedTemplateObject(ctx context.Context, objectName string, previousWritten, total int64, onProgress progressCallback) ([]byte, error) {
	objectURL := templateHostedObjectURL(objectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, objectURL, nil)
	if err != nil {
		return nil, fmt.Errorf("downloading hosted template file %q: %w", objectName, err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading hosted template file %q: %w", objectName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("hosted template file not found: %s", objectURL)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading hosted template file %q: HTTP %d", objectName, resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if onProgress != nil {
		reader = &progressReader{
			r:     resp.Body,
			total: total,
			onProgress: func(written, total int64) {
				onProgress(previousWritten+written, total)
			},
		}
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading hosted template file %q: %w", objectName, err)
	}
	return content, nil
}

// collectTemplateValues gathers values for all template variables.
// It uses varOverrides (from --var flags) for non-interactive values,
// falling back to bubbletea prompts for anything missing.
// If all variables can be resolved without prompting (via overrides or defaults),
// no interactive prompts are shown. Otherwise all non-overridden variables are
// prompted interactively with defaults pre-filled.
func collectTemplateValues(manifest *templateManifest, appID string, varOverrides map[string]string) (map[string]interface{}, error) {
	vals := map[string]interface{}{
		"APP_ID": appID,
	}

	// Determine if any variables need interactive input.
	needsPrompt := false
	for _, v := range manifest.Variables {
		if v.Name == "APP_ID" {
			continue
		}
		if _, ok := varOverrides[v.Name]; ok {
			continue
		}
		if v.Default == nil {
			needsPrompt = true
			break
		}
	}

	for _, v := range manifest.Variables {
		if v.Name == "APP_ID" {
			continue
		}

		// Check --var overrides first.
		if raw, ok := varOverrides[v.Name]; ok {
			parsed, err := parseVariableValue(v, raw)
			if err != nil {
				return nil, fmt.Errorf("invalid value for %s: %w", v.Name, err)
			}
			if err := validateVariable(v, parsed); err != nil {
				return nil, err
			}
			vals[v.Name] = parsed
			continue
		}

		// If no prompting needed, use defaults silently.
		if !needsPrompt && v.Default != nil {
			vals[v.Name] = v.Default
			continue
		}

		// Interactive prompt with default pre-filled.
		val, err := promptForVariable(v)
		if err != nil {
			return nil, err
		}
		vals[v.Name] = val
	}

	return vals, nil
}

// promptForVariable shows a bubbletea prompt for a single template variable.
func promptForVariable(v templateVariable) (interface{}, error) {
	prompt := v.Prompt
	if prompt == "" {
		prompt = v.Name
	}

	switch v.Type {
	case "boolean":
		defVal := false
		if b, ok := v.Default.(bool); ok {
			defVal = b
		}
		_ = defVal // tui.Confirm doesn't support defaults, so we just ask
		result, err := tui.Confirm(prompt + "?")
		if err != nil {
			return nil, err
		}
		return result, nil

	case "integer":
		defStr := ""
		if v.Default != nil {
			defStr = fmt.Sprintf("%v", v.Default)
			// JSON numbers unmarshal as float64.
			if f, ok := v.Default.(float64); ok {
				defStr = strconv.Itoa(int(f))
			}
		}

		validate := func(input string) error {
			n, err := strconv.Atoi(strings.TrimSpace(input))
			if err != nil {
				return fmt.Errorf("must be an integer")
			}
			return validateVariable(v, n)
		}

		var result string
		var err error
		if defStr != "" {
			result, err = tui.PromptTextWithDefault(prompt, v.Description, defStr, validate)
		} else {
			result, err = tui.PromptText(prompt, v.Description, validate)
		}
		if err != nil {
			return nil, err
		}
		n, _ := strconv.Atoi(strings.TrimSpace(result))
		return n, nil

	default: // "string"
		defStr := ""
		if s, ok := v.Default.(string); ok {
			defStr = s
		}

		validate := func(input string) error {
			if v.Required && strings.TrimSpace(input) == "" {
				return fmt.Errorf("%s cannot be empty", prompt)
			}
			return validateVariable(v, strings.TrimSpace(input))
		}

		var result string
		var err error
		if defStr != "" {
			result, err = tui.PromptTextWithDefault(prompt, v.Description, defStr, validate)
		} else {
			result, err = tui.PromptText(prompt, v.Description, validate)
		}
		if err != nil {
			return nil, err
		}
		return strings.TrimSpace(result), nil
	}
}

// parseVariableValue converts a string flag value to the appropriate Go type.
func parseVariableValue(v templateVariable, raw string) (interface{}, error) {
	switch v.Type {
	case "integer":
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("expected integer, got %q", raw)
		}
		return n, nil
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("expected boolean, got %q", raw)
		}
		return b, nil
	default:
		return raw, nil
	}
}

// validateVariable checks a value against the variable's validation rules.
func validateVariable(v templateVariable, val interface{}) error {
	if v.Validate == nil {
		return nil
	}

	switch v.Type {
	case "integer":
		n, ok := val.(int)
		if !ok {
			return nil
		}
		if minRaw, ok := v.Validate["min"]; ok {
			if minF, ok := minRaw.(float64); ok && n < int(minF) {
				return fmt.Errorf("%s must be at least %d", v.Name, int(minF))
			}
		}
		if maxRaw, ok := v.Validate["max"]; ok {
			if maxF, ok := maxRaw.(float64); ok && n > int(maxF) {
				return fmt.Errorf("%s must be at most %d", v.Name, int(maxF))
			}
		}

	case "string":
		s, ok := val.(string)
		if !ok {
			return nil
		}
		if patternRaw, ok := v.Validate["pattern"]; ok {
			if pattern, ok := patternRaw.(string); ok {
				re, err := regexp.Compile(pattern)
				if err != nil {
					return fmt.Errorf("invalid validation pattern %q: %w", pattern, err)
				}
				if !re.MatchString(s) {
					return fmt.Errorf("%s does not match pattern %s", v.Name, pattern)
				}
			}
		}
	}

	return nil
}

// renderAndWriteTemplate takes the raw file map, evaluates each text file as a
// Go text/template (so {{.VAR}}, {{if}}, {{range}}, etc. all work), and writes
// to destDir. It renames directories named after the template to the app ID.
func renderAndWriteTemplate(files map[string][]byte, destDir, appID, templateName string, vals map[string]interface{}) error {
	for relPath, content := range files {
		// Rename template-named directories to app ID.
		relPath = renameTemplatePath(relPath, templateName, appID)

		destPath := filepath.Join(destDir, relPath)

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", relPath, err)
		}

		// Only render text files. Binary files (images, fonts, wasm) are written as-is.
		output := content
		if isTextFile(relPath) {
			rendered, err := renderTemplateContent(relPath, content, vals)
			if err != nil {
				return err
			}
			output = rendered
		}

		if err := os.WriteFile(destPath, output, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", destPath, err)
		}
	}

	return nil
}

// renderTemplateContent evaluates content as a Go text/template against vals.
// Parse errors are surfaced (scoped to path) so template-authoring mistakes
// like a broken {{if}} don't silently produce files with unrendered actions.
// missingkey=error causes references to undeclared variables to fail rather
// than render as "<no value>".
func renderTemplateContent(path string, content []byte, vals map[string]interface{}) ([]byte, error) {
	tmpl, err := template.New(path).Option("missingkey=error").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vals); err != nil {
		return nil, fmt.Errorf("rendering %s: %w", path, err)
	}
	return buf.Bytes(), nil
}

// renameTemplatePath replaces occurrences of the template name in path
// components with the app ID (e.g. Sources/simple-api/ -> Sources/my-app/).
func renameTemplatePath(relPath, templateName, appID string) string {
	parts := strings.Split(relPath, "/")
	for i, part := range parts {
		if part == templateName {
			parts[i] = appID
		}
	}
	return strings.Join(parts, "/")
}

// isTextFile returns true if a file path looks like a text file that should
// have template tokens replaced. Binary files are left as-is. JSX/TSX files
// are excluded because they routinely contain `{{ … }}` object expressions
// (e.g. `icons={{ success: ... }}`) that collide with Go template syntax;
// they don't need variable interpolation in practice.
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json", ".toml", ".yaml", ".yml", ".md", ".txt", ".html", ".css",
		".js", ".ts", ".py", ".rs", ".swift", ".go",
		".cpp", ".c", ".h", ".hpp", ".cmake", ".sh", ".bash", ".zsh",
		".dockerfile", ".gitignore", ".env", ".cfg", ".ini", ".xml",
		".svg", ".lock":
		return true
	}
	// Files without extension (Dockerfile, Makefile, etc.)
	base := filepath.Base(path)
	switch base {
	case "Dockerfile", "Makefile", "CMakeLists.txt", "Package.swift",
		"Cargo.toml", "Cargo.lock", ".swift-version", ".gitignore":
		return true
	}
	return false
}

// collectSchemaAnswers walks a templateSchema and collects answers for each
// applicable phase and question, storing results in vals. Phase and question
// conditions are evaluated against the already-collected vals, so earlier
// answers can gate later questions.
//
// For "radio" questions, vals[q.ID] is set to the selected option value.
// For "input" questions, vals[q.ID] is set to the entered string.
// For "checkbox" questions, vals[q.ID] is a comma-separated list of selected
// values, and vals[q.ID+"_"+optionValue] is true/false for each option.
//
// In non-interactive mode, questions already answered in vals (e.g. via --var)
// are skipped, and unanswered questions fall back to their defaults. Required
// questions with no default and no pre-supplied value return an error.
func collectSchemaAnswers(schema *templateSchema, vals map[string]interface{}) error {
	interactive := isInteractiveTerminal()
	for _, phase := range schema.Phases {
		if !evaluateSchemaCondition(phase.When, vals) {
			continue
		}

		if phase.Title != "" {
			fmt.Printf("\n%s\n", phase.Title)
		}

		for _, q := range phase.Questions {
			if !evaluateSchemaCondition(q.When, vals) {
				continue
			}
			if _, answered := vals[q.ID]; answered {
				continue
			}
			if !interactive {
				if err := applySchemaDefault(q, vals); err != nil {
					return err
				}
				continue
			}
			if err := promptSchemaQuestion(q, vals); err != nil {
				return err
			}
		}
	}
	return nil
}

// applySchemaDefault sets a schema question's answer in vals using its declared
// default. For radio questions with no default, the first option is used. For
// required questions that have no fallback, an error is returned directing the
// user to supply the answer via --var.
func applySchemaDefault(q templateSchemaQuestion, vals map[string]interface{}) error {
	switch q.Type {
	case "radio":
		if q.Default != "" {
			vals[q.ID] = q.Default
		} else if len(q.Options) > 0 {
			vals[q.ID] = q.Options[0].Value
		} else if q.Required {
			return fmt.Errorf("schema question %q requires input in non-interactive mode (use --var %s=VALUE)", q.Label, q.ID)
		}
	case "checkbox":
		if q.Required && q.Default == "" && len(q.Options) == 0 {
			return fmt.Errorf("schema question %q requires input in non-interactive mode (use --var %s=VALUE)", q.Label, q.ID)
		}
		selectedSet := map[string]bool{}
		if q.Default != "" {
			for _, p := range strings.Split(q.Default, ",") {
				selectedSet[strings.TrimSpace(p)] = true
			}
		}
		vals[q.ID] = q.Default
		for _, opt := range q.Options {
			vals[q.ID+"_"+opt.Value] = selectedSet[opt.Value]
		}
	default: // "input"
		if q.Default != "" {
			vals[q.ID] = q.Default
		} else if q.Required {
			return fmt.Errorf("schema question %q requires input in non-interactive mode (use --var %s=VALUE)", q.Label, q.ID)
		} else {
			vals[q.ID] = ""
		}
	}
	return nil
}

// evaluateSchemaCondition returns true when cond is nil (no condition) or when
// the condition matches the current vals.
func evaluateSchemaCondition(cond *templateSchemaCondition, vals map[string]interface{}) bool {
	if cond == nil {
		return true
	}

	raw, ok := vals[cond.QuestionID]
	if !ok {
		return false
	}
	answer := fmt.Sprintf("%v", raw)

	if cond.Equals != nil {
		return answer == *cond.Equals
	}

	if len(cond.In) > 0 {
		for _, v := range cond.In {
			if answer == v {
				return true
			}
		}
		return false
	}

	if cond.Contains != nil {
		parts := strings.Split(answer, ",")
		for _, p := range parts {
			if strings.TrimSpace(p) == *cond.Contains {
				return true
			}
		}
		return false
	}

	return true
}

// promptSchemaQuestion shows the appropriate TUI prompt for a single question
func promptSchemaQuestion(q templateSchemaQuestion, vals map[string]interface{}) error {
	fmt.Println()

	switch q.Type {
	case "radio":
		items := schemaOptionPickerItems(q.Options)
		var val string
		var err error
		if schemaOptionsHaveModelMetadata(q.Options) {
			val, err = pickFromItemsWithColumns(q.Label, items, schemaModelPickerColumns())
		} else {
			val, err = pickFromItems(q.Label, items)
		}
		if err != nil {
			return err
		}
		vals[q.ID] = val

	case "checkbox":
		items := make([]tui.ChecklistItem, len(q.Options))
		for i, opt := range q.Options {
			items[i] = tui.ChecklistItem{Label: opt.Label, Value: opt.Value}
		}
		selected, err := tui.RunChecklist(q.Label, items)
		if err != nil {
			return err
		}
		// Build comma-separated list and per-option booleans.
		selectedSet := make(map[string]bool, len(selected))
		selectedValues := make([]string, 0, len(selected))
		for _, item := range selected {
			selectedSet[item.Value] = true
			selectedValues = append(selectedValues, item.Value)
		}
		vals[q.ID] = strings.Join(selectedValues, ",")
		for _, opt := range q.Options {
			vals[q.ID+"_"+opt.Value] = selectedSet[opt.Value]
		}

	default: // "input"
		validate := func(input string) error {
			if q.Required && strings.TrimSpace(input) == "" {
				return fmt.Errorf("%s cannot be empty", q.Label)
			}
			return nil
		}
		var val string
		var err error
		if q.Default != "" {
			val, err = tui.PromptTextWithDefault(q.Label, "", q.Default, validate)
		} else {
			val, err = tui.PromptText(q.Label, "", validate)
		}
		if err != nil {
			return err
		}
		vals[q.ID] = strings.TrimSpace(val)
	}

	return nil
}

func schemaOptionPickerItems(options []templateSchemaOption) []tui.PickerItem {
	items := make([]tui.PickerItem, len(options))
	for i, opt := range options {
		name := opt.Label
		if name == "" {
			name = opt.Value
		}
		items[i] = tui.PickerItem{
			Name:        name,
			Description: opt.Description,
			Size:        opt.Size,
			Parameters:  opt.Parameters,
			Comments:    opt.Comments,
			Value:       opt.Value,
		}
	}
	return items
}

func schemaOptionsHaveModelMetadata(options []templateSchemaOption) bool {
	for _, opt := range options {
		if opt.Size != "" || opt.Parameters != "" || opt.Comments != "" {
			return true
		}
	}
	return false
}

func schemaModelPickerColumns() []tui.PickerColumn {
	return []tui.PickerColumn{
		{
			Title:    "model",
			MinWidth: 32,
			Required: true,
			Value: func(item tui.PickerItem) string {
				return item.Name
			},
		},
		{
			Title:    "size",
			MinWidth: 10,
			Value: func(item tui.PickerItem) string {
				return item.Size
			},
		},
		{
			Title:    "parameters",
			MinWidth: 14,
			Value: func(item tui.PickerItem) string {
				return item.Parameters
			},
		},
		{
			Title:    "comments",
			MinWidth: 44,
			Value: func(item tui.PickerItem) string {
				if item.Comments != "" {
					return item.Comments
				}
				return item.Description
			},
		},
	}
}

// parseVarFlags parses --var KEY=VALUE flags into a map.
func parseVarFlags(vars []string) (map[string]string, error) {
	result := make(map[string]string, len(vars))
	for _, v := range vars {
		eq := strings.IndexByte(v, '=')
		if eq < 1 {
			return nil, fmt.Errorf("invalid --var format %q (expected KEY=VALUE)", v)
		}
		key := strings.TrimSpace(v[:eq])
		if key == "" {
			return nil, fmt.Errorf("invalid --var format %q (empty key)", v)
		}
		val := v[eq+1:]
		result[key] = val
	}
	return result, nil
}
