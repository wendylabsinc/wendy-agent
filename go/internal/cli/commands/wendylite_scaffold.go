package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"gopkg.in/yaml.v3"
)

// shouldOfferWendyLiteESPIDFScaffold reports whether wendy run should offer to
// scaffold Wendy Lite ESP-IDF components + a wendy.json for the current
// project. True only when wendy.json is missing, the project directory looks
// like an ESP-IDF project ("esp-idf" projectType, per detectProjectType), and
// the already-resolved run target is a USB-connected wendy-lite provider
// device.
func shouldOfferWendyLiteESPIDFScaffold(cfgMissing bool, projectType string, target *SelectedDevice) bool {
	if !cfgMissing || projectType != "esp-idf" || target == nil {
		return false
	}
	if target.External == nil || target.Provider == nil {
		return false
	}
	if target.Provider.Key() != "wendy-lite" {
		return false
	}
	return target.External.ConnectionType() == "USB"
}

const wendyLiteRepoURL = "https://github.com/wendylabsinc/wendy-lite.git"

// mergeIdfComponentDependencies adds or updates one ESP-IDF Component Manager
// git dependency per name in components, pointing at the matching
// subdirectory of the wendy-lite repo, in the idf_component.yml at
// manifestPath. Existing unrelated content is preserved; re-running with the
// same components is a no-op.
func mergeIdfComponentDependencies(manifestPath string, components []string) error {
	var doc yaml.Node
	data, err := os.ReadFile(manifestPath)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", manifestPath, err)
		}
	case os.IsNotExist(err):
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}
	default:
		return fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	if len(doc.Content) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a YAML mapping at the document root", manifestPath)
	}

	deps := findOrAppendMappingKey(root, "dependencies")
	if deps.Kind == yaml.ScalarNode && deps.Tag == "!!null" {
		// "dependencies:" with nothing under it is a normal, common manifest
		// shape (produced by IDF templates and hand-written stubs) that
		// parses as a null scalar rather than an empty mapping. Upgrade it
		// in place instead of treating it as malformed.
		*deps = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if deps.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: \"dependencies\" is not a mapping", manifestPath)
	}

	for _, name := range components {
		dep := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		dep.Content = append(dep.Content,
			scalarNode("git"), scalarNode(wendyLiteRepoURL),
			scalarNode("path"), scalarNode("components/"+name),
		)
		setMappingKey(deps, name, dep)
	}

	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(manifestPath), err)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", manifestPath, err)
	}
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", manifestPath, err)
	}
	return nil
}

func scalarNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// findOrAppendMappingKey returns the value node for key in mapping, creating
// an empty mapping node under that key if it doesn't already exist.
func findOrAppendMappingKey(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, scalarNode(key), value)
	return value
}

// setMappingKey sets key to value in mapping, overwriting any existing entry
// with the same key in place (preserving the order of unrelated keys).
func setMappingKey(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, scalarNode(key), value)
}

// newWendyLiteESPIDFAppConfig builds the minimal wendy.json contents for a
// scaffolded native ESP-IDF wendy-lite project.
func newWendyLiteESPIDFAppConfig(appID string) *appconfig.AppConfig {
	return &appconfig.AppConfig{
		AppID:    appID,
		Version:  "0.1.0",
		Platform: appconfig.PlatformWendyLite,
		Language: "c",
	}
}

// writeWendyLiteESPIDFAppConfig writes a scaffolded wendy.json into cwd,
// deriving the app ID from the directory name (matching ensureAppConfig's
// existing convention in helpers.go).
func writeWendyLiteESPIDFAppConfig(cwd string) error {
	cfgPath := filepath.Join(cwd, "wendy.json")
	dirName := filepath.Base(cwd)

	data, err := json.MarshalIndent(newWendyLiteESPIDFAppConfig(dirName), "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		return fmt.Errorf("writing wendy.json: %w", err)
	}
	fmt.Printf("Created wendy.json for %s\n", dirName)
	return nil
}

// wendyLiteESPIDFComponents lists the non-WASM Wendy Lite ESP-IDF components
// offered by the scaffold checklist. WASM-only pieces (wendy_wasm,
// wendy_hal_export, wendy_wasi_shim, wendy_safety, wendy_callback) are
// intentionally excluded: this flow is for native ESP-IDF firmware, not WASM
// apps.
var wendyLiteESPIDFComponents = []string{
	"wendy_hal",
	"wendy_usb",
	"wendy_wifi",
	"wendy_ble_prov",
	"wendy_cloud_prov",
	"wendy_storage",
	"wendy_uart",
	"wendy_spi",
	"wendy_sys",
	"wendy_otel",
	"wendy_ble",
	"wendy_net",
	"wendy_app_usb",
}

// wendyLiteComponentChecklistFn runs the component-selection checklist. It is
// a package var so tests can stub it (mirrors confirmFn's testability
// pattern in helpers.go).
var wendyLiteComponentChecklistFn = func(items []tui.ChecklistItem) ([]tui.ChecklistItem, error) {
	return tui.RunChecklist("Which Wendy Lite components do you want to add?", items)
}

// promptAndScaffoldWendyLiteESPIDF offers to add wendy-lite ESP-IDF
// components to the project at cwd and, if accepted, writes a wendy.json.
// Returns scaffolded=true iff wendy.json was written.
func promptAndScaffoldWendyLiteESPIDF(cwd string) (bool, error) {
	if !confirmFn("This looks like an ESP-IDF project without a wendy.json, and a USB-connected ESP32 was detected. Add Wendy Lite components and set up 'wendy run' for this project?") {
		return false, nil
	}

	items := make([]tui.ChecklistItem, len(wendyLiteESPIDFComponents))
	for i, name := range wendyLiteESPIDFComponents {
		items[i] = tui.ChecklistItem{Label: name, Value: name, Selected: true}
	}
	selected, err := wendyLiteComponentChecklistFn(items)
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return false, ErrUserCancelled
		}
		return false, err
	}

	names := make([]string, len(selected))
	for i, item := range selected {
		names[i] = item.Value
	}

	if len(names) > 0 {
		manifestPath := filepath.Join(cwd, "main", "idf_component.yml")
		if err := mergeIdfComponentDependencies(manifestPath, names); err != nil {
			return false, fmt.Errorf("adding wendy-lite components: %w", err)
		}
		fmt.Printf("Added %d Wendy Lite component(s) to main/idf_component.yml\n", len(names))
	} else {
		fmt.Println("No Wendy Lite components were added. You can add idf_component.yml dependencies manually later.")
	}

	if err := writeWendyLiteESPIDFAppConfig(cwd); err != nil {
		return false, err
	}
	return true, nil
}
