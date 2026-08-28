package spec

import (
	"reflect"
	"testing"
)

func TestInstallLocalFilesCoversEveryEcosystem(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *Install
		want []string
	}{
		{"nil", nil, nil},
		{"empty", &Install{}, nil},
		{"pip", &Install{Pip: []PipInstall{{Requirements: "requirements.txt"}}}, []string{"requirements.txt"}},
		{"pip without requirements", &Install{Pip: []PipInstall{{Packages: []string{"flask"}}}}, nil},
		{"npm", &Install{Npm: &NpmInstall{}}, []string{"package.json", "package-lock.json"}},
		{"yarn", &Install{Npm: &NpmInstall{Manager: "yarn"}}, []string{"package.json", "yarn.lock"}},
		{"uv", &Install{Uv: &UvInstall{}}, []string{"pyproject.toml", "uv.lock"}},
		{"apt reads nothing local", &Install{Apt: &AptInstall{Packages: []string{"curl"}}}, nil},
		{"cmake reads nothing local", &Install{CMake: []CMakeInstall{{Repository: "r", Commit: "c"}}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.LocalFiles(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("LocalFiles() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The uv stale-dependency bug happened because "which context files does this
// install read" was answered independently in dockerignore and in the CLI's
// native deps hash, and uv reached only one of them. LocalFiles is now the
// single answer — so this guard fails the moment an Install field is added
// without deciding which side of that question it falls on, which is the only
// way the drift can come back.
func TestInstallLocalFilesClassifiesEveryField(t *testing.T) {
	// Every Install field must appear here. Adding an ecosystem means adding
	// it to LocalFiles (if it reads context files) and to this list either way.
	readsContext := map[string]bool{
		"Pip": true, "Npm": true, "Uv": true,
		// These resolve entirely from the network or the Stagefile itself.
		"Apt": false, "Apk": false, "CMake": false,
	}
	ty := reflect.TypeOf(Install{})
	for i := 0; i < ty.NumField(); i++ {
		name := ty.Field(i).Name
		if _, ok := readsContext[name]; !ok {
			t.Fatalf("Install.%s is unclassified: add it to LocalFiles if it reads "+
				"build-context files, and to this test's map either way", name)
		}
	}
	if len(readsContext) != ty.NumField() {
		t.Fatalf("this test lists %d fields but Install has %d", len(readsContext), ty.NumField())
	}
}
