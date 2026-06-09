package commands

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/assets"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

// installSkillsForAllTools extracts embedded skill files into each detected AI tool.
// Errors per-tool are collected and returned so the caller can report them.
func installSkillsForAllTools() []mcpSetupResult {
	var results []mcpSetupResult

	if r := installClaudeCodeSkills(); r != nil {
		results = append(results, *r)
	}
	if r := installCodexSkills(); r != nil {
		results = append(results, *r)
	}
	if r := installOpencodeSkills(); r != nil {
		results = append(results, *r)
	}

	return results
}

// skillNames lists every first-level directory under assets/skills that holds a SKILL.md.
func wendySkillNames() []string {
	entries, err := assets.FS.ReadDir("skills")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := assets.FS.Open("skills/" + e.Name() + "/SKILL.md"); err == nil {
			names = append(names, e.Name())
		}
	}
	return names
}

// ---- Claude Code ----------------------------------------------------------------

func installClaudeCodeSkills() *mcpSetupResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if _, err := os.Stat(pluginsDir); err != nil {
		// Claude Code not present.
		return nil
	}

	const marketplace = "wendy-skills"
	ver := sanitizeVersion(version.Version)

	skillNames := wendySkillNames()
	if len(skillNames) == 0 {
		return &mcpSetupResult{tool: "Claude Code skills", err: fmt.Errorf("no embedded skills found")}
	}

	for _, name := range skillNames {
		dst := filepath.Join(pluginsDir, "cache", marketplace, name, ver)
		if err := extractSkillDir(name, dst); err != nil {
			return &mcpSetupResult{tool: "Claude Code skills", err: fmt.Errorf("extracting %s: %w", name, err)}
		}
		if err := updateInstalledPlugins(pluginsDir, name, marketplace, ver, dst); err != nil {
			return &mcpSetupResult{tool: "Claude Code skills", err: err}
		}
	}

	return &mcpSetupResult{tool: "Claude Code skills", path: filepath.Join(pluginsDir, "cache", marketplace)}
}

// extractSkillDir copies assets/skills/<name>/** into dstDir/skills/<name>/.
func extractSkillDir(skillName, dstDir string) error {
	return fs.WalkDir(assets.FS, "skills/"+skillName, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "skills/"+skillName+"/")
		dst := filepath.Join(dstDir, "skills", skillName, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		data, err := assets.FS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
}

type installedPluginsFile struct {
	Version int                      `json:"version"`
	Plugins map[string][]pluginEntry `json:"plugins"`
}

type pluginEntry struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
	Version     string `json:"version"`
	InstalledAt string `json:"installedAt,omitempty"`
	LastUpdated string `json:"lastUpdated"`
}

func updateInstalledPlugins(pluginsDir, name, marketplace, ver, installPath string) error {
	jsonPath := filepath.Join(pluginsDir, "installed_plugins.json")
	var ipf installedPluginsFile
	data, err := os.ReadFile(jsonPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading installed_plugins.json: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &ipf); err != nil {
			return fmt.Errorf("parsing installed_plugins.json: %w", err)
		}
	}
	if ipf.Version == 0 {
		ipf.Version = 2
	}
	if ipf.Plugins == nil {
		ipf.Plugins = map[string][]pluginEntry{}
	}

	key := name + "@" + marketplace
	now := time.Now().UTC().Format(time.RFC3339)
	entry := pluginEntry{
		Scope:       "user",
		InstallPath: installPath,
		Version:     ver,
		LastUpdated: now,
	}

	existing := ipf.Plugins[key]
	if len(existing) == 0 {
		entry.InstalledAt = now
		ipf.Plugins[key] = []pluginEntry{entry}
	} else {
		entry.InstalledAt = existing[0].InstalledAt
		ipf.Plugins[key] = []pluginEntry{entry}
	}

	out, err := json.MarshalIndent(ipf, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(jsonPath, out, 0o644)
}

// sanitizeVersion replaces characters not safe in directory names.
func sanitizeVersion(v string) string {
	r := strings.NewReplacer("/", "-", ":", "-", " ", "-")
	return r.Replace(v)
}

// ---- Codex ----------------------------------------------------------------------

func installCodexSkills() *mcpSetupResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	codexDir := filepath.Join(home, ".codex")
	if _, err := os.Stat(codexDir); err != nil {
		if _, err2 := exec.LookPath("codex"); err2 != nil {
			return nil
		}
		if err := os.MkdirAll(codexDir, 0o755); err != nil {
			return &mcpSetupResult{tool: "Codex skills", err: err}
		}
	}

	target := filepath.Join(codexDir, "wendy-skills.md")
	if err := writeSkillsMarkdown(target); err != nil {
		return &mcpSetupResult{tool: "Codex skills", err: err}
	}
	return &mcpSetupResult{tool: "Codex skills", path: target}
}

// ---- Opencode -------------------------------------------------------------------

func installOpencodeSkills() *mcpSetupResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// Detect opencode via binary or config directory.
	configDir := filepath.Join(home, ".config", "opencode")
	if _, err := os.Stat(configDir); err != nil {
		if _, err2 := exec.LookPath("opencode"); err2 != nil {
			return nil
		}
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			return &mcpSetupResult{tool: "Opencode skills", err: err}
		}
	}

	target := filepath.Join(configDir, "wendy-skills.md")
	if err := writeSkillsMarkdown(target); err != nil {
		return &mcpSetupResult{tool: "Opencode skills", err: err}
	}
	return &mcpSetupResult{tool: "Opencode skills", path: target}
}

func writeSkillsMarkdown(path string) error {
	var sb strings.Builder
	sb.WriteString("# Wendy Skills\n\n")
	sb.WriteString("Auto-generated by `wendy mcp setup`. Do not edit — re-run the command to update.\n\n")

	for _, name := range []string{"wendy", "wendy-lite", "wendy-contributing", "wendy-swift"} {
		data, err := assets.FS.ReadFile("skills/" + name + "/SKILL.md")
		if err != nil {
			continue
		}
		sb.WriteString("---\n\n")
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}
