package stagefile

import (
	"os"
	"path/filepath"
	"testing"

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
			if _, err := spec.Parse(data); err != nil {
				t.Fatalf("%s does not compile: %v", path, err)
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
