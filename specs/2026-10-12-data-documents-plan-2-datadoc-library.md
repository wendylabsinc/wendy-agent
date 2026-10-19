# Data documents PR 3: the shared document library, implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One library parses every Wendy data document's header and dispatches to its kind, with today's campaigns as the first kind and no change to how they behave.

**Architecture:** A new package `go/internal/shared/datadoc` holds the header, a registry of kinds and a two-pass parser, and knows no kind itself. `go/internal/agent/data` builds the registry with one kind, `collection`, whose decoder is `ParseCampaign`, and exposes `ParseDocument` and `DocumentHeader`. `ParseCampaign` learns to accept `kind: collection`. `wendy data campaign deploy` parses through the library, and its no-path picker offers only files whose header is a collection's.

**Tech Stack:** Go 1.27, `gopkg.in/yaml.v3` (already a dependency), cobra CLI in `go/internal/cli/commands`.

**Spec:** `specs/2026-10-12-data-documents-design.md` §5 (envelope), §8 (collection), §10 (CLI), §14 row 3. PR 4 adds the `inference` and `watch` kinds to the registry built here.

## Global Constraints

- Branch `ed/datadoc` from `origin/main`, in worktree `/Users/ethan/Documents/WendyAgent-datadoc`. Commits authored as `24462281+EBro912@users.noreply.github.com`.
- A missing `kind` means `collection` (`datadoc.DefaultKind`).
- Header rules (spec §5.1): `version` required and a whole number in the kind's `Versions`; `name` matches `^[A-Za-z0-9._-]{1,128}$`; `owner`, when set, is `<kind>/<name>` naming an ownable kind, on an ownable kind, never itself. Collections are not ownable.
- One YAML document per file; a second document is an error, with today's `ParseCampaign` rule.
- A campaign file without `kind`, and the same file with `kind: collection` added, has exactly today's revision. Golden revisions on `main` at `fbcbb402a`:
  - `Examples/WendyDataCampaign/campaign.yaml`: `9b52fc99f73319c7f9a6207a5fd242c12ab9ef67ebd9176de7b8ada814446012`
  - `Examples/WendyDataPeople/campaign.yaml`: `5bdd06fff93d5871fd29b77c85a8a9f17d28486b06c2a4af4d21fa610f1ca625`
  - `Examples/WendyDataModelApp/campaign.yaml`: `39fda09f79a85b497b1efebf33cae2c83bea8ef61a2d81240e7446e60299a2d5`
- `kind` is never stored in the campaign plan JSON and never feeds the revision.
- No proto change. `wendy data campaign deploy` keeps calling `CampaignDeploy`.
- `datadoc` imports nothing from `go/internal/agent` or `go/internal/cli`.
- Docs for `kind` wait for PR 4, when there is more than one kind to describe.
- Run `gofmt -l .` from `go/` before every push.

## Review Focus

1. **Existing campaign files.** Every example campaign parses through the library with its golden revision, with and without `kind: collection`. Test: Task 2, `TestParseDocumentKeepsCampaignRevisions`.
2. **Other YAML in the project folder.** A `compose.yml`, a stagefile or an empty file must not be offered by the deploy picker. Test: Task 3, `TestCampaignDeployFilePickerOffersOnlyCampaigns`.
3. **`kind: collection` sent to an agent from before this PR.** The agent answers "field kind not found"; the CLI must say to update the agent or drop the line. Test: Task 3, `TestCampaignDeployErrorExplainsKindOnOldAgent`.
4. **`version: 1.5` or `version: "1"`.** yaml.v3 reads `1.5` into an int as 1 without complaint; the header must refuse both. Test: Task 1, `TestParseHeaderErrors` cases "version fractional" and "version quoted".
5. **`owner` on a campaign file.** Refused with a message that names collections, not a yaml "field not found". Test: Task 1 case "owner on collection" and Task 2 `TestParseDocumentRefusesOwnerOnCampaign`.

---

## File map

| File | Responsibility |
|---|---|
| `go/internal/shared/datadoc/datadoc.go` (new) | `Header`, `Document`, `Kind`, `Registry`, `ParseHeader`, `Parse` |
| `go/internal/shared/datadoc/datadoc_test.go` (new) | header and dispatch tests with test kinds |
| `go/internal/agent/data/documents.go` (new) | the registry this build knows; `ParseDocument`, `DocumentHeader` |
| `go/internal/agent/data/documents_test.go` (new) | golden revisions, kind handling, stored plan |
| `go/internal/agent/data/campaign.go` | `Campaign.Kind`; `validate` accepts only `collection` |
| `go/internal/cli/commands/data.go` | deploy parses through `ParseDocument`; picker filters by header; old-agent hint |
| `go/internal/cli/commands/data_campaign_file_test.go` | picker and hint tests |

---

### Task 0: Worktree

- [ ] **Step 1: Create the worktree**

```bash
cd /Users/ethan/Documents/WendyAgent
git fetch origin
git worktree add -b ed/datadoc ../WendyAgent-datadoc origin/main
cd ../WendyAgent-datadoc
git config user.email
```

Expected: the last line prints `24462281+EBro912@users.noreply.github.com`.

---

### Task 1: The `datadoc` package

**Files:**
- Create: `go/internal/shared/datadoc/datadoc.go`
- Test: `go/internal/shared/datadoc/datadoc_test.go`

**Interfaces:**
- Produces (PR 4 and Task 2 rely on these exact names):
  - `const DefaultKind = "collection"`
  - `type Header struct { Kind string; Version int; Name string; Owner string }`
  - `type Document struct { Header Header; Body any; Revision string; Warnings []string }`
  - `type Kind struct { Name string; Versions []int; Ownable bool; Decode func(header Header, contents []byte) (body any, revision string, warnings []string, err error) }`
  - `func NewRegistry(kinds ...Kind) *Registry` (panics on an empty name, no versions, nil `Decode` or a duplicate)
  - `func (r *Registry) Kinds() []string` (sorted)
  - `func (r *Registry) ParseHeader(contents []byte) (Header, error)`
  - `func (r *Registry) Parse(contents []byte) (Document, error)`

- [ ] **Step 1: Write the failing tests**

Create `go/internal/shared/datadoc/datadoc_test.go`:

```go
package datadoc

import (
	"errors"
	"strings"
	"testing"
)

type testBody struct{ contents string }

func testRegistry(decodeErr error) *Registry {
	decode := func(header Header, contents []byte) (any, string, []string, error) {
		if decodeErr != nil {
			return nil, "", nil, decodeErr
		}
		return testBody{string(contents)}, "rev-" + header.Kind + "-" + header.Name, []string{"warned"}, nil
	}
	return NewRegistry(
		Kind{Name: "collection", Versions: []int{1}, Decode: decode},
		Kind{Name: "inference", Versions: []int{1}, Ownable: true, Decode: decode},
		Kind{Name: "watch", Versions: []int{1, 2}, Ownable: true, Decode: decode},
	)
}

func TestParseDefaultsToCollectionAndPassesTheWholeDocument(t *testing.T) {
	doc, err := testRegistry(nil).Parse([]byte("version: 1\nname: people\nsources: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Header != (Header{Kind: "collection", Version: 1, Name: "people"}) {
		t.Fatalf("header = %+v", doc.Header)
	}
	if doc.Revision != "rev-collection-people" || len(doc.Warnings) != 1 || doc.Warnings[0] != "warned" {
		t.Fatalf("document = %+v", doc)
	}
	if body, ok := doc.Body.(testBody); !ok || !strings.Contains(body.contents, "sources: []") {
		t.Fatalf("the decoder did not get the whole document: %+v", doc.Body)
	}
}

func TestParseAcceptsAnOwner(t *testing.T) {
	doc, err := testRegistry(nil).Parse([]byte("kind: inference\nversion: 1\nname: door\nowner: watch/door\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Header != (Header{Kind: "inference", Version: 1, Name: "door", Owner: "watch/door"}) {
		t.Fatalf("header = %+v", doc.Header)
	}
}

func TestParseAcceptsAWholeFloatVersion(t *testing.T) {
	// yaml reads 1.0 as a float; it names version 1 exactly.
	header, err := testRegistry(nil).ParseHeader([]byte("version: 1.0\nname: x\n"))
	if err != nil || header.Version != 1 {
		t.Fatalf("version 1.0 = %+v, %v", header, err)
	}
}

func TestParseHeaderErrors(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"empty", "", "document is empty"},
		{"comment only", "# nothing\n", "document is empty"},
		{"not a mapping", "- a\n- b\n", "parsing document header"},
		{"unknown kind", "kind: wach\nversion: 1\nname: x\n", `unknown kind "wach"; known kinds: collection, inference, watch`},
		{"version missing", "kind: watch\nname: x\n", "version is required (watch documents support version 1, 2)"},
		{"version zero", "version: 0\nname: x\n", "version is required (collection documents support version 1)"},
		{"version unsupported", "kind: inference\nversion: 2\nname: x\n", "inference version 2 is not supported (supported: 1)"},
		{"version fractional", "version: 1.5\nname: x\n", "version must be a whole number"},
		{"version quoted", "version: \"1\"\nname: x\n", "version must be a whole number"},
		{"version a word", "version: one\nname: x\n", "version must be a whole number"},
		{"name missing", "version: 1\n", "name is required"},
		{"name with a space", "version: 1\nname: front door\n", "name must use 1 to 128 letters, numbers, '.', '-' or '_'"},
		{"name too long", "version: 1\nname: " + strings.Repeat("a", 129) + "\n", "name must use 1 to 128 letters"},
		{"second document", "version: 1\nname: x\n---\nversion: 1\nname: y\n", "must contain exactly one YAML document"},
		{"broken second document", "version: 1\nname: x\n---\n[unclosed\n", "the text after the first one does not parse"},
		{"owner on collection", "version: 1\nname: x\nowner: watch/w\n", "collection documents cannot have an owner"},
		{"owner without slash", "kind: inference\nversion: 1\nname: x\nowner: w\n", `owner "w" must be <kind>/<name> naming a document of kind inference or watch`},
		{"owner of unknown kind", "kind: inference\nversion: 1\nname: x\nowner: robot/w\n", `owner "robot/w" must be <kind>/<name>`},
		{"owner is a collection", "kind: inference\nversion: 1\nname: x\nowner: collection/c\n", `owner "collection/c" must be <kind>/<name>`},
		{"owner name invalid", "kind: inference\nversion: 1\nname: x\nowner: watch/a b\n", `owner "watch/a b" must be <kind>/<name>`},
		{"owns itself", "kind: watch\nversion: 1\nname: x\nowner: watch/x\n", "a document cannot own itself"},
	}
	registry := testRegistry(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registry.ParseHeader([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if _, parseErr := registry.Parse([]byte(tc.doc)); parseErr == nil || parseErr.Error() != err.Error() {
				t.Fatalf("Parse error = %v, want the header error %v", parseErr, err)
			}
		})
	}
}

func TestParseHeaderDoesNotDecodeTheBody(t *testing.T) {
	registry := testRegistry(errors.New("decoder ran"))
	if _, err := registry.ParseHeader([]byte("kind: watch\nversion: 2\nname: x\n")); err != nil {
		t.Fatalf("ParseHeader ran the decoder: %v", err)
	}
}

func TestParseReturnsTheDecoderError(t *testing.T) {
	want := errors.New("lease must be at least 15s")
	_, err := testRegistry(want).Parse([]byte("kind: watch\nversion: 1\nname: x\n"))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want the decoder's", err)
	}
}

func TestNewRegistryRefusesBadKinds(t *testing.T) {
	decode := func(Header, []byte) (any, string, []string, error) { return nil, "", nil, nil }
	for name, kinds := range map[string][]Kind{
		"no name":     {{Versions: []int{1}, Decode: decode}},
		"no versions": {{Name: "watch", Decode: decode}},
		"no decoder":  {{Name: "watch", Versions: []int{1}}},
		"duplicate":   {{Name: "watch", Versions: []int{1}, Decode: decode}, {Name: "watch", Versions: []int{1}, Decode: decode}},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewRegistry accepted it")
				}
			}()
			NewRegistry(kinds...)
		})
	}
}

func TestKindsAreSorted(t *testing.T) {
	if got := strings.Join(testRegistry(nil).Kinds(), ","); got != "collection,inference,watch" {
		t.Fatalf("Kinds() = %s", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/shared/datadoc/ -v`
Expected: build failure, `undefined: NewRegistry`.

- [ ] **Step 3: Implement**

Create `go/internal/shared/datadoc/datadoc.go`:

```go
// Package datadoc parses Wendy data documents: one-document YAML files that
// share a header (kind, version, name, owner) and are dispatched on kind.
//
// The package knows no kind itself. The packages that own the kinds build a
// Registry from them, so the CLI and the agent parse with the same rules.
// See specs/2026-10-12-data-documents-design.md §5.
package datadoc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultKind is the kind of a document without a kind field: every campaign
// file written before data documents existed.
const DefaultKind = "collection"

// Header is what every data document starts with.
type Header struct {
	Kind    string
	Version int
	Name    string
	// Owner is "<kind>/<name>" of the document whose removal removes this
	// one, or empty.
	Owner string
}

// Document is a parsed data document. Body holds the kind's own type.
type Document struct {
	Header   Header
	Body     any
	Revision string
	Warnings []string
}

// Kind describes one kind of document.
type Kind struct {
	Name     string
	Versions []int
	// Ownable kinds may have an owner and may own other documents.
	Ownable bool
	// Decode parses and validates the whole document, strictly. The header
	// has already passed the registry's checks.
	Decode func(header Header, contents []byte) (body any, revision string, warnings []string, err error)
}

// Registry is the set of kinds one build understands.
type Registry struct {
	kinds map[string]Kind
}

// NewRegistry panics on a kind without a name, versions or decoder, and on a
// duplicate: those are programming errors, not input errors.
func NewRegistry(kinds ...Kind) *Registry {
	r := &Registry{kinds: map[string]Kind{}}
	for _, kind := range kinds {
		if kind.Name == "" || len(kind.Versions) == 0 || kind.Decode == nil {
			panic("datadoc: a kind needs a name, versions and a decoder")
		}
		if _, duplicate := r.kinds[kind.Name]; duplicate {
			panic("datadoc: kind " + kind.Name + " registered twice")
		}
		r.kinds[kind.Name] = kind
	}
	return r
}

// Kinds returns the registered kind names, sorted.
func (r *Registry) Kinds() []string {
	names := make([]string, 0, len(r.kinds))
	for name := range r.kinds {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// rawHeader reads only the header fields and ignores the rest of the document.
// Version is decoded loosely because yaml.v3 reads 1.5 into an int as 1.
type rawHeader struct {
	Kind    string `yaml:"kind"`
	Version any    `yaml:"version"`
	Name    string `yaml:"name"`
	Owner   string `yaml:"owner"`
}

// ParseHeader checks a document's header without decoding its body.
func (r *Registry) ParseHeader(contents []byte) (Header, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var raw rawHeader
	if err := decoder.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return Header{}, errors.New("document is empty")
		}
		return Header{}, fmt.Errorf("parsing document header: %w", err)
	}
	var trailing any
	switch err := decoder.Decode(&trailing); {
	case err == nil:
		return Header{}, errors.New("a data document must contain exactly one YAML document")
	case !errors.Is(err, io.EOF):
		return Header{}, fmt.Errorf("a data document must contain exactly one YAML document; the text after the first one does not parse: %w", err)
	}
	version, ok := wholeNumber(raw.Version)
	if !ok {
		return Header{}, errors.New("version must be a whole number")
	}
	header := Header{Kind: raw.Kind, Version: version, Name: raw.Name, Owner: raw.Owner}
	if header.Kind == "" {
		header.Kind = DefaultKind
	}
	kind, ok := r.kinds[header.Kind]
	if !ok {
		return Header{}, fmt.Errorf("unknown kind %q; known kinds: %s", header.Kind, strings.Join(r.Kinds(), ", "))
	}
	versions := joinInts(kind.Versions)
	if header.Version == 0 {
		return Header{}, fmt.Errorf("version is required (%s documents support version %s)", header.Kind, versions)
	}
	if !slices.Contains(kind.Versions, header.Version) {
		return Header{}, fmt.Errorf("%s version %d is not supported (supported: %s)", header.Kind, header.Version, versions)
	}
	if header.Name == "" {
		return Header{}, errors.New("name is required")
	}
	if !nameRE.MatchString(header.Name) {
		return Header{}, errors.New("name must use 1 to 128 letters, numbers, '.', '-' or '_'")
	}
	if header.Owner != "" {
		if err := r.checkOwner(kind, header); err != nil {
			return Header{}, err
		}
	}
	return header, nil
}

// Parse checks the header, then decodes the whole document with its kind.
func (r *Registry) Parse(contents []byte) (Document, error) {
	header, err := r.ParseHeader(contents)
	if err != nil {
		return Document{}, err
	}
	body, revision, warnings, err := r.kinds[header.Kind].Decode(header, contents)
	if err != nil {
		return Document{}, err
	}
	return Document{Header: header, Body: body, Revision: revision, Warnings: warnings}, nil
}

func (r *Registry) checkOwner(kind Kind, header Header) error {
	if !kind.Ownable {
		return fmt.Errorf("%s documents cannot have an owner", kind.Name)
	}
	var ownable []string
	for _, name := range r.Kinds() {
		if r.kinds[name].Ownable {
			ownable = append(ownable, name)
		}
	}
	invalid := fmt.Errorf("owner %q must be <kind>/<name> naming a document of kind %s", header.Owner, strings.Join(ownable, " or "))
	ownerKind, ownerName, found := strings.Cut(header.Owner, "/")
	if !found || !slices.Contains(ownable, ownerKind) || !nameRE.MatchString(ownerName) {
		return invalid
	}
	if ownerKind == header.Kind && ownerName == header.Name {
		return errors.New("a document cannot own itself")
	}
	return nil
}

// wholeNumber accepts an absent version (0), an int, or a float with no
// fractional part, such as 1.0.
func wholeNumber(value any) (int, bool) {
	switch v := value.(type) {
	case nil:
		return 0, true
	case int:
		return v, true
	case float64:
		if v == math.Trunc(v) && v >= math.MinInt32 && v <= math.MaxInt32 {
			return int(v), true
		}
	}
	return 0, false
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go && go test ./internal/shared/datadoc/ -v`
Expected: PASS, every case.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/datadoc/
git commit -m "feat(datadoc): parse the shared header of Wendy data documents

A registry of kinds and a two-pass parser: the header (kind, version,
name, owner) first, then the kind's own strict decoder. The package knows
no kind itself; the agent's data package registers them (spec §5).

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Campaigns become the `collection` kind

**Files:**
- Create: `go/internal/agent/data/documents.go`
- Test: `go/internal/agent/data/documents_test.go`
- Modify: `go/internal/agent/data/campaign.go` (`Campaign` struct ~line 242; `validate` ~line 408)

**Interfaces:**
- Consumes: Task 1's `datadoc.NewRegistry`, `datadoc.Kind`, `datadoc.Document`, `datadoc.Header`, `datadoc.DefaultKind`.
- Produces: `func ParseDocument(contents []byte) (datadoc.Document, error)`; `func DocumentHeader(contents []byte) (datadoc.Header, error)`; `Campaign.Kind string` (`json:"-" yaml:"kind,omitempty"`). For a collection, `Document.Body` is a `data.Campaign` value. PR 4 adds its kinds to `documentKinds` in `documents.go`.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/data/documents_test.go`:

```go
package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"
)

// Revisions of the example campaigns on main before data documents
// (fbcbb402a). A file without kind, and the same file with kind: collection,
// must keep them: a moved revision looks like an edited plan to operators and
// to the fleet backend.
var goldenCampaignRevisions = map[string]string{
	"../../../../Examples/WendyDataCampaign/campaign.yaml": "9b52fc99f73319c7f9a6207a5fd242c12ab9ef67ebd9176de7b8ada814446012",
	"../../../../Examples/WendyDataPeople/campaign.yaml":   "5bdd06fff93d5871fd29b77c85a8a9f17d28486b06c2a4af4d21fa610f1ca625",
	"../../../../Examples/WendyDataModelApp/campaign.yaml": "39fda09f79a85b497b1efebf33cae2c83bea8ef61a2d81240e7446e60299a2d5",
}

func TestParseDocumentKeepsCampaignRevisions(t *testing.T) {
	for path, want := range goldenCampaignRevisions {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for label, contents := range map[string][]byte{"without kind": raw, "with kind": append([]byte("kind: collection\n"), raw...)} {
			doc, err := ParseDocument(contents)
			if err != nil {
				t.Fatalf("%s %s: %v", path, label, err)
			}
			campaign, ok := doc.Body.(Campaign)
			if !ok {
				t.Fatalf("%s %s: body is %T, want Campaign", path, label, doc.Body)
			}
			if doc.Header.Kind != datadoc.DefaultKind || doc.Header.Name != campaign.Name || doc.Header.Version != 1 {
				t.Fatalf("%s %s: header = %+v", path, label, doc.Header)
			}
			if doc.Revision != want || campaign.Revision != want {
				t.Fatalf("%s %s: revision %s, want %s", path, label, doc.Revision, want)
			}
		}
	}
}

func TestParseCampaignAcceptsOnlyTheCollectionKind(t *testing.T) {
	base := string(minimalCampaign(1, "telemetry: true", "wifi"))
	if _, err := ParseCampaign([]byte("kind: collection\n" + base)); err != nil {
		t.Fatalf("kind: collection refused: %v", err)
	}
	_, err := ParseCampaign([]byte("kind: watch\n" + base))
	if err == nil || !strings.Contains(err.Error(), `kind "watch" is not a campaign`) {
		t.Fatalf("kind: watch error = %v", err)
	}
}

func TestParseDocumentRefusesOwnerOnCampaign(t *testing.T) {
	_, err := ParseDocument([]byte("owner: watch/w\n" + string(minimalCampaign(1, "telemetry: true", "wifi"))))
	if err == nil || !strings.Contains(err.Error(), "collection documents cannot have an owner") {
		t.Fatalf("owner error = %v", err)
	}
}

func TestParseDocumentReportsCampaignErrors(t *testing.T) {
	// The collection decoder is ParseCampaign: its validation reaches callers
	// unchanged.
	_, err := ParseDocument([]byte(strings.Replace(string(minimalCampaign(1, "telemetry: true", "wifi")), "upload: {when: wifi}", "upload: {when: sometimes}", 1)))
	if err == nil || !strings.Contains(err.Error(), "upload.when") {
		t.Fatalf("campaign validation error = %v", err)
	}
}

func TestDeployedPlanDoesNotStoreKind(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := m.DeployCampaign([]byte("kind: collection\n" + string(minimalCampaign(1, "telemetry: true", "wifi"))))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(m.campaignDir(), campaign.Name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), `"kind"`) {
		t.Fatalf("stored plan carries kind:\n%s", stored)
	}
}
```

(`campaign.go` reports an invalid `upload.when` as "upload.when must be one of always, wifi, or manual".)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/data/ -run 'ParseDocument|ParseCampaignAcceptsOnly|DeployedPlanDoesNotStoreKind' -v`
Expected: build failure, `undefined: ParseDocument`.

- [ ] **Step 3: Accept `kind` in campaigns**

In `campaign.go`, in `type Campaign struct`, directly above `Version int ...` add:

```go
	// Kind is the data document header's kind. A campaign is a collection;
	// the field exists so ParseCampaign's strict decoding accepts
	// "kind: collection", and it is never stored or hashed.
	Kind string `json:"-" yaml:"kind,omitempty"`
```

At the top of `func (c Campaign) validate() error`, before the version check, add:

```go
	if c.Kind != "" && c.Kind != "collection" {
		return fmt.Errorf("kind %q is not a campaign; a campaign has kind collection or no kind", c.Kind)
	}
```

- [ ] **Step 4: Register the kind**

Create `go/internal/agent/data/documents.go`:

```go
package data

import "github.com/wendylabsinc/wendy/go/internal/shared/datadoc"

// documentKinds are the data document kinds this build understands. A
// collection is a campaign: its decoder is ParseCampaign, so collections parse,
// validate and hash exactly as campaigns always have.
var documentKinds = datadoc.NewRegistry(
	datadoc.Kind{
		Name:     datadoc.DefaultKind,
		Versions: []int{CampaignVersion},
		Decode: func(_ datadoc.Header, contents []byte) (any, string, []string, error) {
			campaign, err := ParseCampaign(contents)
			if err != nil {
				return nil, "", nil, err
			}
			return campaign, campaign.Revision, nil, nil
		},
	},
)

// ParseDocument parses any data document this build knows. A file without
// kind is a collection.
func ParseDocument(contents []byte) (datadoc.Document, error) {
	return documentKinds.Parse(contents)
}

// DocumentHeader checks only a document's header, for callers that sort files
// by kind without validating them.
func DocumentHeader(contents []byte) (datadoc.Header, error) {
	return documentKinds.ParseHeader(contents)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/data/ -run 'ParseDocument|ParseCampaign|DeployedPlanDoesNotStoreKind|Example' -v`
Expected: PASS.

Then the whole package: `cd go && go test ./internal/agent/data/`
Expected: PASS. (The golden revisions prove `planDigestInput` did not pick up `Kind`.)

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/data/documents.go go/internal/agent/data/documents_test.go go/internal/agent/data/campaign.go
git commit -m "feat(data): campaigns are the collection kind of data documents

ParseDocument parses through the shared datadoc registry, with
ParseCampaign as the collection decoder. ParseCampaign accepts
kind: collection, never stores or hashes it, and the example campaigns
keep their revisions from main.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `wendy data campaign deploy` parses through the library

**Files:**
- Modify: `go/internal/cli/commands/data.go` (`newDataCampaignDeployCmd` ~line 75; `resolveCampaignDeployFile` ~line 116)
- Test: `go/internal/cli/commands/data_campaign_file_test.go`

**Interfaces:**
- Consumes: Task 2's `data.ParseDocument`, `data.DocumentHeader`, `data.Campaign`.
- Produces: `func campaignDeployError(err error) error` (wraps an older agent's refusal of `kind`); `resolveCampaignDeployFile` keeps its signature.

- [ ] **Step 1: Write the failing tests**

In `data_campaign_file_test.go`, add `"google.golang.org/grpc/codes"` and `"google.golang.org/grpc/status"` to the imports. Replace `TestCampaignDeployFilePicker` and `TestCampaignDeployFileMissingPathGuidance` with:

```go
const pickerCampaign = "version: 1\nname: %s\n"

func writePickerFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for name, contents := range files {
		if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCampaignDeployFilePickerOffersOnlyCampaigns(t *testing.T) {
	t.Chdir(t.TempDir())
	writePickerFiles(t, map[string]string{
		"people.yaml":         fmt.Sprintf(pickerCampaign, "people"),
		"doors.yml":           "kind: collection\n" + fmt.Sprintf(pickerCampaign, "doors"),
		"night.YAML":          fmt.Sprintf(pickerCampaign, "night"),
		"notes.txt":           fmt.Sprintf(pickerCampaign, "notes"),
		"compose.yml":         "services:\n  web:\n    image: nginx\n",
		"build.stagefile.yaml": "version: 1\nstages: []\n",
		"empty.yaml":          "",
		"front.watch.yaml":    "kind: watch\nversion: 1\nname: front\n",
	})
	if err := os.Mkdir("nested.yaml", 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := resolveCampaignDeployFile(nil, true, func(_ string, items []tui.PickerItem) (string, error) {
		var names []string
		for _, item := range items {
			names = append(names, item.Value.(string))
		}
		if !reflect.DeepEqual(names, []string{"doors.yml", "night.YAML", "people.yaml"}) {
			t.Fatalf("picker files = %v", names)
		}
		return items[2].Value.(string), nil
	})
	if err != nil || path != "people.yaml" {
		t.Fatalf("selected %q: %v", path, err)
	}
	_, err = resolveCampaignDeployFile(nil, true, func(string, []tui.PickerItem) (string, error) { return "", ErrUserCancelled })
	if !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestCampaignDeployFileMissingPathGuidance(t *testing.T) {
	t.Chdir(t.TempDir())
	neverPick := func(string, []tui.PickerItem) (string, error) { t.Fatal("unexpected picker"); return "", nil }
	_, err := resolveCampaignDeployFile(nil, true, neverPick)
	if err == nil || !strings.Contains(err.Error(), "no .yaml or .yml files") || !strings.Contains(err.Error(), "wendy data campaign deploy campaign.yaml") {
		t.Fatalf("missing file guidance = %v", err)
	}
	writePickerFiles(t, map[string]string{"compose.yml": "services: {}\n"})
	_, err = resolveCampaignDeployFile(nil, true, neverPick)
	if err == nil || !strings.Contains(err.Error(), "no campaign files") || !strings.Contains(err.Error(), "compose.yml") {
		t.Fatalf("no-campaign guidance = %v", err)
	}
	writePickerFiles(t, map[string]string{"people.yml": fmt.Sprintf(pickerCampaign, "people")})
	_, err = resolveCampaignDeployFile(nil, false, neverPick)
	if err == nil || !strings.Contains(err.Error(), "non-interactive or --json") || !strings.Contains(err.Error(), "people.yml") || strings.Contains(err.Error(), "compose.yml") {
		t.Fatalf("non-interactive guidance = %v", err)
	}
	path, err := resolveCampaignDeployFile([]string{"../explicit.yaml"}, false, neverPick)
	if err != nil || path != "../explicit.yaml" {
		t.Fatalf("explicit path = %q: %v", path, err)
	}
	cmd := newDataCampaignDeployCmd()
	if err := cmd.ValidateArgs(nil); err != nil {
		t.Fatalf("picker blocked by argument validation: %v", err)
	}
	if err := cmd.ValidateArgs([]string{"one.yaml", "two.yml"}); err == nil {
		t.Fatal("accepted multiple paths")
	}
}

func TestCampaignDeployErrorExplainsKindOnOldAgent(t *testing.T) {
	old := status.Error(codes.InvalidArgument, "parsing campaign YAML: yaml: unmarshal errors:\n  line 1: field kind not found in type data.Campaign")
	err := campaignDeployError(old)
	if err == nil || !strings.Contains(err.Error(), "wendy device update") || !strings.Contains(err.Error(), "remove the kind line") {
		t.Fatalf("old-agent error = %v", err)
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("the gRPC status was lost: %v", status.Code(err))
	}
	other := status.Error(codes.InvalidArgument, "campaign must define at least one source")
	if got := campaignDeployError(other); got != other {
		t.Fatalf("an unrelated error was rewritten: %v", got)
	}
	if campaignDeployError(nil) != nil {
		t.Fatal("nil error rewritten")
	}
}
```

Add `"fmt"` to the test file's imports too.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/cli/commands/ -run 'CampaignDeployFile|CampaignDeployError' -v`
Expected: FAIL. The picker offers `compose.yml`, `build.stagefile.yaml`, `empty.yaml` and `front.watch.yaml`; `campaignDeployError` is undefined.

- [ ] **Step 3: Filter the picker by header**

In `resolveCampaignDeployFile`, replace the loop body's tail, from `names = append(names, entry.Name())` through the `if !interactive` block, so the function reads:

```go
func resolveCampaignDeployFile(args []string, interactive bool, pick func(string, []tui.PickerItem) (string, error)) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	const usage = "pass a file path, for example: wendy data campaign deploy campaign.yaml"
	entries, err := os.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("listing campaign files in the current directory: %w; %s", err, usage)
	}
	var items []tui.PickerItem
	var names, skipped []string
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		info, err := os.Stat(entry.Name())
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		// Offer only files whose header is a campaign's, not every YAML file:
		// a project folder also holds compose files, stagefiles and, with data
		// documents, inference and watch files.
		if !isCampaignFile(entry.Name(), info.Size()) {
			skipped = append(skipped, entry.Name())
			continue
		}
		names = append(names, entry.Name())
		items = append(items, tui.PickerItem{Name: entry.Name(), Value: entry.Name()})
	}
	if len(items) == 0 {
		if len(skipped) > 0 {
			return "", fmt.Errorf("no campaign files in the current directory (these YAML files are not campaigns: %s); %s", strings.Join(skipped, ", "), usage)
		}
		return "", fmt.Errorf("no .yaml or .yml files found in the current directory; %s", usage)
	}
	if !interactive {
		return "", fmt.Errorf("a campaign YAML path is required in non-interactive or --json mode; %s\nCampaign files in the current directory: %s", usage, strings.Join(names, ", "))
	}
	return pick("Select a campaign YAML file", items)
}

// maxCampaignFileBytes bounds what the picker reads per file. Campaigns are a
// few kilobytes; anything far larger is not one.
const maxCampaignFileBytes = 1 << 20

func isCampaignFile(name string, size int64) bool {
	if size > maxCampaignFileBytes {
		return false
	}
	contents, err := os.ReadFile(name)
	if err != nil {
		return false
	}
	header, err := data.DocumentHeader(contents)
	return err == nil && header.Kind == datadoc.DefaultKind
}
```

Add `"github.com/wendylabsinc/wendy/go/internal/shared/datadoc"` to `data.go`'s imports.

- [ ] **Step 4: Parse through the library and explain old agents**

In `newDataCampaignDeployCmd`, replace

```go
		plan, err := data.ParseCampaign(contents)
		if err != nil {
			return err
		}
```

with

```go
		document, err := data.ParseDocument(contents)
		if err != nil {
			return err
		}
		plan, ok := document.Body.(data.Campaign)
		if !ok {
			return fmt.Errorf("%s is a %s document, not a campaign", path, document.Header.Kind)
		}
```

(With only `collection` registered, `ok` is always true in this PR; PR 4 changes the message to point at `wendy data apply`.)

Replace

```go
		campaign, err := conn.DataService.CampaignDeploy(cmd.Context(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: contents})
		if err != nil {
			return err
		}
```

with

```go
		campaign, err := conn.DataService.CampaignDeploy(cmd.Context(), &agentpbv2.DataCampaignDeployRequest{CampaignYaml: contents})
		if err != nil {
			return campaignDeployError(err)
		}
```

and add, below `newDataCampaignDeployCmd`:

```go
// campaignDeployError explains the one refusal a new CLI can get from an
// older agent: a campaign file that starts with kind: collection, which the
// agent's strict parser does not know. The gRPC status is kept.
func campaignDeployError(err error) error {
	if err == nil || !strings.Contains(status.Convert(err).Message(), "field kind not found") {
		return err
	}
	return status.Errorf(status.Code(err), "%s\nThis device's agent predates the kind field: update it with `wendy device update`, or remove the kind line from the campaign file.", status.Convert(err).Message())
}
```

Add `"google.golang.org/grpc/status"` to `data.go`'s imports (it is not imported there today).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run 'CampaignDeployFile|CampaignDeployError|DataCampaign' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/data.go go/internal/cli/commands/data_campaign_file_test.go
git commit -m "feat(cli): campaign deploy parses data documents and offers only campaigns

The no-path picker offered every YAML file in the folder, compose files
and stagefiles included. It now offers files whose data document header
is a collection's, and says which YAML files it skipped. A kind line
refused by an older agent comes back with how to fix it.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Verify and open the PR

**Files:** none changed unless a check fails.

- [ ] **Step 1: Run every package that parses campaigns**

Run: `cd go && go test ./internal/shared/datadoc/ ./internal/agent/data/ ./internal/agent/services/ ./internal/cli/commands/ ./internal/cli/mcp/`
Expected: PASS. `internal/cli/mcp` holds the robot gateway, which builds campaigns with `yaml.Marshal(data.Campaign)`; `Kind` is `omitempty`, so its output is unchanged. If a failure appears, check it against `origin/main` in a scratch worktree (no `git stash`).

- [ ] **Step 2: Vet and format**

Run: `cd go && go vet ./internal/shared/datadoc/ ./internal/agent/data/ ./internal/cli/commands/ && gofmt -l .`
Expected: no output from `gofmt -l`.

- [ ] **Step 3: Push and open the PR**

```bash
git push -u origin ed/datadoc
gh pr create --base main --head ed/datadoc \
  --title "feat(datadoc): shared parser for data documents, with campaigns as the collection kind" \
  --body "$(cat <<'EOF'
First code step of the data documents spec (#2257, §5 and §14 row 3).

- **`go/internal/shared/datadoc`**: the header (`kind`, `version`, `name`, `owner`), a registry of kinds and a two-pass parser: header first, then the kind's strict decoder. It knows no kind itself.
- **Campaigns are the `collection` kind.** `data.ParseDocument` uses `ParseCampaign` as the collection decoder. A file without `kind` is a collection, and `ParseCampaign` now also accepts `kind: collection`, never storing or hashing it. The example campaigns keep their revisions from `main` (golden test).
- **`wendy data campaign deploy`** parses through the library. Its no-path picker offers only campaign files instead of every YAML file in the folder, and a `kind` line refused by an older agent comes back with how to fix it.

No proto change. PR 4 registers the `inference` and `watch` kinds and adds `wendy data apply`; `kind` gets documented then.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: a PR URL. Leave it ready for review.
