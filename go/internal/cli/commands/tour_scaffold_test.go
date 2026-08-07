package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTourCreatePythonProjectStaysInBaseDir pins the fix for sample projects
// leaking into the developer's real ~/Documents: createPythonProject must
// scaffold under the injected projectBaseDir and nowhere else, and must pick a
// non-conflicting "-N" name when the base name already exists rather than
// overwrite it.
func TestTourCreatePythonProjectStaysInBaseDir(t *testing.T) {
	base := t.TempDir()

	m := newTourWizardModel()
	m.projectBaseDir = base
	if err := m.createPythonProject(); err != nil {
		t.Fatalf("createPythonProject: %v", err)
	}

	want := filepath.Join(base, "wendy-hello")
	if m.projectPath != want {
		t.Errorf("projectPath = %q, want %q", m.projectPath, want)
	}
	for _, name := range []string{"wendy.json", "app.py", "Dockerfile", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(want, name)); err != nil {
			t.Errorf("expected scaffolded file %s: %v", name, err)
		}
	}

	// A second run must not overwrite the first; it increments the suffix.
	m2 := newTourWizardModel()
	m2.projectBaseDir = base
	if err := m2.createPythonProject(); err != nil {
		t.Fatalf("createPythonProject (2nd): %v", err)
	}
	if want2 := filepath.Join(base, "wendy-hello-1"); m2.projectPath != want2 {
		t.Errorf("second projectPath = %q, want %q", m2.projectPath, want2)
	}

	// Exactly the two scaffolds should exist under base — nothing escaped it.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("base dir has %d entries, want 2 (wendy-hello, wendy-hello-1)", len(entries))
	}
}
