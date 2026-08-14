package stagefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/codegen"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/gpu"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// Every Stagefile shipped in Examples/ must parse and validate. The DSL-gaps
// survey records two conversions that parsed and validated cleanly and then
// died at docker build; this cannot catch that class, but it does catch the
// cheaper one — a shipped example that was never run through the compiler at
// all after a schema change.
func TestShippedExamplesParseAndValidate(t *testing.T) {
	root := filepath.Join("..", "..", "..", "Examples")
	var found int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Base(path) != "build.stagefile.yaml" {
			return nil
		}
		found++
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// spec.Parse validates as well as parses.
			f, err := spec.Parse(data)
			if err != nil {
				t.Fatalf("%s does not compile: %v", path, err)
			}
			// Validation only covers the shape. Code generation is where a
			// stage's declarations are actually turned into instructions, and
			// where an example that survived a schema change but no longer
			// lowers would show up.
			images := map[string]string{}
			needsGPU := false
			for _, s := range f.Stages {
				images[s.From] = "sha256:" + strings.Repeat("a", 64)
				needsGPU = needsGPU || s.CUDA
			}
			var profile *gpu.Profile
			if needsGPU {
				// Any real architecture will do — this asserts that the
				// example lowers, not which board it is destined for.
				p, err := gpu.ProfileFor(gpu.KnownArches()[0])
				if err != nil {
					t.Fatal(err)
				}
				profile = &p
			}
			if _, err := codegen.Generate(f, images, nil, "", profile); err != nil {
				t.Fatalf("%s does not generate: %v", path, err)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no Stagefiles found under Examples/ — the walk root is wrong")
	}
}
