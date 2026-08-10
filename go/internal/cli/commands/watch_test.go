package commands

import (
	"path/filepath"
	"testing"
)

func TestWatchShouldIgnore(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		rel    string
		ignore bool
	}{
		// Artifacts the deploy pipeline writes into the watched root on every
		// build — reacting to them would cancel each deploy from inside itself.
		{generatedDockerfileName, true},
		{generatedDockerignoreName, true},
		{"build.stagefile.lock.yaml", true},
		{"api/" + generatedDockerfileName, true},
		// ...including the artifacts of every Stagefile variant.
		{"Dockerfile.generated.prod", true},
		{"Dockerfile.generated.prod.dockerignore", true},
		{"prod.stagefile.lock.yaml", true},
		// Real sources must keep triggering redeploys.
		{stagefileSourceName, false},
		{"prod.stagefile.yaml", false},
		{"main.py", false},
		{"Dockerfile", false},
		{"Dockerfile.prod", false},
		// Editor droppings and ignored dirs.
		{"main.py~", true},
		{".git/HEAD", true},
		{"node_modules/pkg/index.js", true},
	}
	for _, c := range cases {
		if got := watchShouldIgnore(filepath.Join(root, filepath.FromSlash(c.rel)), root); got != c.ignore {
			t.Errorf("watchShouldIgnore(%q) = %v, want %v", c.rel, got, c.ignore)
		}
	}
}
