package buildspec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

const remoteSwiftManifest = `// swift-tools-version: 6.2
import PackageDescription
let package = Package(
    name: "example",
    dependencies: [
        .package(url: "https://github.com/wendylabsinc/tensorrt-swift.git", from: "0.0.4")
    ],
    targets: [.executableTarget(name: "example")]
)
`

func auditSpec(workdir, product string) string {
	return fmt.Sprintf(`version = 1

[build]
adapter = "swift"
base = "dustynv/tensorrt:8.6-r36.2.0"
workdir = %q
setup = ["apt-get update && apt-get install -y clang"]

[build.env]
Z_LAST = "last"
A_FIRST = "first"

[build.dependencies]
command = ["swift", "package", "resolve"]

[build.compile]
command = ["swift", "build"]

[runtime]
base = "dustynv/tensorrt:8.6-r36.2.0"
workdir = "/app"
entrypoint = ["/app/%s"]

[[runtime.artifacts]]
source = ".build/debug/%s"
destination = "/app/%s"
`, workdir, product, product, product)
}

func TestCompileAuditSwiftProjectsDeterministically(t *testing.T) {
	tests := []struct {
		name    string
		workdir string
		product string
	}{
		{name: "tensorrt hello", workdir: "/repo/samples/swift/tensorrt-hello", product: "tensorrt-hello"},
		{name: "tensorrt llm streaming", workdir: "/repo", product: "tensorrt-llm-streaming"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := fstest.MapFS{"Package.swift": &fstest.MapFile{Data: []byte(remoteSwiftManifest)}}
			var first []byte
			for iteration := 0; iteration < 25; iteration++ {
				result, err := Compile(project, []byte(auditSpec(test.workdir, test.product)))
				if err != nil {
					t.Fatalf("Compile: %v", err)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatalf("Marshal: %v", err)
				}
				if iteration == 0 {
					first = encoded
				} else if string(encoded) != string(first) {
					t.Fatalf("iteration %d produced different output", iteration)
				}
			}

			var result Result
			if err := json.Unmarshal(first, &result); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			assertOrdered(t, result.Dockerfile,
				`COPY ["Package.swift","./"]`,
				`RUN ["swift","package","resolve"]`,
				`COPY . ./`,
				`RUN ["swift","build"]`,
			)
			if !strings.Contains(result.Dockerfile, `COPY --from=build [".build/debug/`+test.product+`","/app/`+test.product+`"]`) {
				t.Fatalf("generated Dockerfile missing runtime artifact:\n%s", result.Dockerfile)
			}
			if len(result.Plan.PlanID) != 64 {
				t.Fatalf("plan ID = %q, want SHA-256", result.Plan.PlanID)
			}
		})
	}
}

func TestCompileDiscoversResolvedManifestAndSortsEnvironment(t *testing.T) {
	project := fstest.MapFS{
		"Package.swift":    &fstest.MapFile{Data: []byte(remoteSwiftManifest)},
		"Package.resolved": &fstest.MapFile{Data: []byte(`{"version":3}`)},
	}
	result, err := Compile(project, []byte(auditSpec("/workspace", "example")))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !strings.Contains(result.Dockerfile, `COPY ["Package.swift","Package.resolved","./"]`) {
		t.Fatalf("resolved manifest not copied:\n%s", result.Dockerfile)
	}
	assertOrdered(t, result.Dockerfile, `ENV A_FIRST="first"`, `ENV Z_LAST="last"`)
}

func TestCompileRejectsLocalSwiftDependency(t *testing.T) {
	project := fstest.MapFS{"Package.swift": &fstest.MapFile{Data: []byte(`.package(path: "../Local")`)}}
	_, err := Compile(project, []byte(auditSpec("/workspace", "example")))
	if err == nil || !strings.Contains(err.Error(), "local path dependencies") {
		t.Fatalf("error = %v, want local path dependency refusal", err)
	}
}

func TestCompileRejectsUnknownField(t *testing.T) {
	project := fstest.MapFS{"Package.swift": &fstest.MapFile{Data: []byte(remoteSwiftManifest)}}
	spec := auditSpec("/workspace", "example") + "\n[build.mystery]\nvalue = 1\n"
	_, err := Compile(project, []byte(spec))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

func TestCompileRejectsMissingExplicitInput(t *testing.T) {
	project := fstest.MapFS{"Package.swift": &fstest.MapFile{Data: []byte(remoteSwiftManifest)}}
	spec := strings.Replace(auditSpec("/workspace", "example"),
		`command = ["swift", "package", "resolve"]`,
		"command = [\"swift\", \"package\", \"resolve\"]\ninputs = [\"Package.swift\", \"Package.resolved\"]", 1)
	_, err := Compile(project, []byte(spec))
	if err == nil || !strings.Contains(err.Error(), "Package.resolved") {
		t.Fatalf("error = %v, want missing explicit input", err)
	}
}

func TestCompileWarnsForUnpinnedImages(t *testing.T) {
	project := fstest.MapFS{"Package.swift": &fstest.MapFile{Data: []byte(remoteSwiftManifest)}}
	result, err := Compile(project, []byte(auditSpec("/workspace", "example")))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := []string{"build.base is not digest-pinned", "runtime.base is not digest-pinned"}
	if fmt.Sprint(result.Plan.Warnings) != fmt.Sprint(want) {
		t.Fatalf("warnings = %v, want %v", result.Plan.Warnings, want)
	}
}

func TestBuiltInAdaptersProduceManifestFirstPlans(t *testing.T) {
	tests := []struct {
		name        string
		adapter     string
		files       fstest.MapFS
		wantCopy    string
		wantRun     string
		wantCompile string
	}{
		{
			name: "node npm", adapter: "node",
			files: fstest.MapFS{
				"package.json":      &fstest.MapFile{Data: []byte(`{"name":"example"}`)},
				"package-lock.json": &fstest.MapFile{Data: []byte(`{"lockfileVersion":3}`)},
			},
			wantCopy: `COPY ["package.json","package-lock.json","./"]`,
			wantRun:  `RUN ["npm","ci"]`, wantCompile: `RUN ["npm","run","build"]`,
		},
		{
			name: "python uv", adapter: "python",
			files: fstest.MapFS{
				"pyproject.toml": &fstest.MapFile{Data: []byte(`[project]`)},
				"uv.lock":        &fstest.MapFile{Data: []byte(`version = 1`)},
			},
			wantCopy: `COPY ["pyproject.toml","uv.lock","./"]`,
			wantRun:  `RUN ["uv","sync","--frozen","--no-install-project"]`,
		},
		{
			name: "go", adapter: "go",
			files: fstest.MapFS{
				"go.mod": &fstest.MapFile{Data: []byte("module example.com/app\n")},
				"go.sum": &fstest.MapFile{Data: []byte("sum\n")},
			},
			wantCopy: `COPY ["go.mod","go.sum","./"]`,
			wantRun:  `RUN ["go","mod","download"]`, wantCompile: `RUN ["go","build","-o","/out/app","."]`,
		},
		{
			name: "rust", adapter: "rust",
			files: fstest.MapFS{
				"Cargo.toml": &fstest.MapFile{Data: []byte("[package]\nname = \"example\"\n")},
				"Cargo.lock": &fstest.MapFile{Data: []byte("version = 4\n")},
			},
			wantCopy: `COPY ["Cargo.toml","Cargo.lock","./"]`,
			wantRun:  `RUN ["cargo","fetch","--locked"]`, wantCompile: `RUN ["cargo","build","--release","--locked"]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Compile(test.files, []byte(minimalAdapterSpec(test.adapter)))
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			assertOrdered(t, result.Dockerfile, test.wantCopy, test.wantRun, `COPY . ./`)
			if test.wantCompile != "" && !strings.Contains(result.Dockerfile, test.wantCompile) {
				t.Fatalf("missing compile command %q:\n%s", test.wantCompile, result.Dockerfile)
			}
			if test.adapter == "python" && strings.Contains(result.Dockerfile, "build.compile.run") {
				t.Fatalf("Python default unexpectedly added compile command:\n%s", result.Dockerfile)
			}
		})
	}
}

func TestCustomAdapterRequiresExplicitDependencyContract(t *testing.T) {
	project := fstest.MapFS{
		"deps.lock": &fstest.MapFile{Data: []byte("locked")},
	}
	spec := minimalAdapterSpec("custom") + `

[build.dependencies]
inputs = ["deps.lock"]
command = ["tool", "resolve"]

[build.compile]
command = ["tool", "build"]
`
	result, err := Compile(project, []byte(spec))
	if err != nil {
		t.Fatalf("Compile custom: %v", err)
	}
	assertOrdered(t, result.Dockerfile, `COPY ["deps.lock","./"]`, `RUN ["tool","resolve"]`, `COPY . ./`, `RUN ["tool","build"]`)
}

func TestAdaptersRefuseIncompleteLocalDependencyClosures(t *testing.T) {
	tests := []struct {
		name    string
		adapter string
		files   fstest.MapFS
		want    string
	}{
		{
			name: "node workspace", adapter: "node", want: "workspaces",
			files: fstest.MapFS{
				"package.json":      &fstest.MapFile{Data: []byte(`{"workspaces":["packages/*"]}`)},
				"package-lock.json": &fstest.MapFile{Data: []byte(`{}`)},
			},
		},
		{
			name: "python nested requirement", adapter: "python", want: "nested input",
			files: fstest.MapFS{"requirements.txt": &fstest.MapFile{Data: []byte("-r base.txt\n")}},
		},
		{
			name: "go local replace", adapter: "go", want: "local replace",
			files: fstest.MapFS{"go.mod": &fstest.MapFile{Data: []byte("module app\nreplace local => ../local\n")}},
		},
		{
			name: "rust workspace", adapter: "rust", want: "workspaces",
			files: fstest.MapFS{"Cargo.toml": &fstest.MapFile{Data: []byte("[workspace]\nmembers = []\n")}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.files, []byte(minimalAdapterSpec(test.adapter)))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func minimalAdapterSpec(adapter string) string {
	return fmt.Sprintf(`version = 1

[build]
adapter = %q
base = "builder:latest"

[runtime]
base = "runtime:latest"
entrypoint = ["/app/example"]

[[runtime.artifacts]]
source = "/out/app"
destination = "/app/example"
`, adapter)
}

func assertOrdered(t *testing.T, value string, needles ...string) {
	t.Helper()
	position := -1
	for _, needle := range needles {
		next := strings.Index(value, needle)
		if next < 0 {
			t.Fatalf("missing %q in:\n%s", needle, value)
		}
		if next <= position {
			t.Fatalf("%q appears out of order in:\n%s", needle, value)
		}
		position = next
	}
}
