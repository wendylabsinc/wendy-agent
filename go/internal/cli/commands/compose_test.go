package commands

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func writeComposeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseComposeFile_PrefersDockerCompose(t *testing.T) {
	dir := t.TempDir()
	writeComposeFile(t, dir, "compose.yml", "services:\n  reporter:\n    image: alpine\n")
	writeComposeFile(t, dir, "docker-compose.yml", "services:\n  greeter:\n    image: alpine\n")

	cfg, name, err := parseComposeFile(dir)
	if err != nil {
		t.Fatalf("parseComposeFile: %v", err)
	}
	if name != "docker-compose.yml" {
		t.Fatalf("expected docker-compose.yml, got %q", name)
	}
	if _, ok := cfg.Services["greeter"]; !ok {
		t.Fatalf("expected service 'greeter', got %#v", cfg.Services)
	}
}

func TestParseComposeFile_NotFound(t *testing.T) {
	if _, _, err := parseComposeFile(t.TempDir()); err == nil {
		t.Fatal("expected error when no compose file is present")
	}
}

func TestComposeBuildContext(t *testing.T) {
	parse := func(t *testing.T, body string) composeService {
		t.Helper()
		var cfg composeConfig
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.Services["svc"]
	}

	t.Run("scalar build path", func(t *testing.T) {
		proj := t.TempDir()
		if err := os.MkdirAll(filepath.Join(proj, "api"), 0o755); err != nil {
			t.Fatal(err)
		}
		svc := parse(t, "services:\n  svc:\n    build: ./api\n")
		ctxDir, df, args, err := composeBuildContext(svc, proj)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if ctxDir != filepath.Join(proj, "api") || df != "Dockerfile" || args != nil {
			t.Fatalf("got (%q,%q,%v)", ctxDir, df, args)
		}
	})

	t.Run("mapping with custom dockerfile and args", func(t *testing.T) {
		proj := t.TempDir()
		svcDir := filepath.Join(proj, "svc")
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(svcDir, "Dockerfile.dev"), []byte("FROM alpine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		svc := parse(t, "services:\n  svc:\n    build:\n      context: ./svc\n      dockerfile: Dockerfile.dev\n      args:\n        FOO: bar\n")
		ctxDir, df, args, err := composeBuildContext(svc, proj)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if ctxDir != svcDir || df != "Dockerfile.dev" || args["FOO"] != "bar" {
			t.Fatalf("got (%q,%q,%v)", ctxDir, df, args)
		}
	})

	t.Run("missing build returns empty", func(t *testing.T) {
		proj := t.TempDir()
		svc := parse(t, "services:\n  svc:\n    image: alpine\n")
		ctxDir, df, _, err := composeBuildContext(svc, proj)
		if err != nil || ctxDir != "" || df != "" {
			t.Fatalf("got (%q,%q,err=%v)", ctxDir, df, err)
		}
	})

	t.Run("unsupported build kind errors", func(t *testing.T) {
		proj := t.TempDir()
		svc := parse(t, "services:\n  svc:\n    build: [./a, ./b]\n")
		if _, _, _, err := composeBuildContext(svc, proj); err == nil {
			t.Fatal("expected error for sequence build kind")
		}
	})

	t.Run("dockerfile path traversal rejected", func(t *testing.T) {
		proj := t.TempDir()
		if err := os.MkdirAll(filepath.Join(proj, "svc"), 0o755); err != nil {
			t.Fatal(err)
		}
		svc := parse(t, "services:\n  svc:\n    build:\n      context: ./svc\n      dockerfile: ../../Dockerfile\n")
		if _, _, _, err := composeBuildContext(svc, proj); err == nil {
			t.Fatal("expected error for traversal dockerfile path")
		}
	})
}

func TestComposeEnv(t *testing.T) {
	parse := func(t *testing.T, body string) composeService {
		t.Helper()
		var cfg composeConfig
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.Services["svc"]
	}

	t.Run("mapping with mixed types", func(t *testing.T) {
		svc := parse(t, "services:\n  svc:\n    environment:\n      STR: hello\n      NUM: 42\n      BOOL: true\n")
		got := composeEnv(svc)
		sort.Strings(got)
		want := []string{"BOOL=true", "NUM=42", "STR=hello"}
		if len(got) != len(want) {
			t.Fatalf("got %v want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v want %v", got, want)
			}
		}
	})

	t.Run("mapping null inherits from process env", func(t *testing.T) {
		t.Setenv("WENDY_TEST_INHERIT", "from-process")
		svc := parse(t, "services:\n  svc:\n    environment:\n      WENDY_TEST_INHERIT: ~\n")
		got := composeEnv(svc)
		if len(got) != 1 || got[0] != "WENDY_TEST_INHERIT=from-process" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("list KEY entries inherit", func(t *testing.T) {
		t.Setenv("WENDY_TEST_LIST", "from-list")
		svc := parse(t, "services:\n  svc:\n    environment:\n      - WENDY_TEST_LIST\n      - EXPLICIT=value\n")
		got := composeEnv(svc)
		sort.Strings(got)
		want := []string{"EXPLICIT=value", "WENDY_TEST_LIST=from-list"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %v want %v", got, want)
		}
	})

	t.Run("list KEY without process env is dropped", func(t *testing.T) {
		os.Unsetenv("WENDY_TEST_MISSING")
		svc := parse(t, "services:\n  svc:\n    environment:\n      - WENDY_TEST_MISSING\n")
		if got := composeEnv(svc); len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})
}

func TestServiceOrder(t *testing.T) {
	parseConfig := func(t *testing.T, body string) *composeConfig {
		t.Helper()
		var cfg composeConfig
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		return &cfg
	}

	t.Run("respects depends_on list form", func(t *testing.T) {
		cfg := parseConfig(t, "services:\n  api:\n    image: a\n    depends_on:\n      - db\n  db:\n    image: b\n")
		ordered, err := serviceOrder(cfg)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// db must precede api.
		dbIdx, apiIdx := -1, -1
		for i, n := range ordered {
			if n == "db" {
				dbIdx = i
			}
			if n == "api" {
				apiIdx = i
			}
		}
		if dbIdx == -1 || apiIdx == -1 || dbIdx > apiIdx {
			t.Fatalf("expected db before api, got %v", ordered)
		}
	})

	t.Run("respects depends_on map form", func(t *testing.T) {
		cfg := parseConfig(t, "services:\n  api:\n    image: a\n    depends_on:\n      db:\n        condition: service_started\n  db:\n    image: b\n")
		ordered, err := serviceOrder(cfg)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(ordered) != 2 {
			t.Fatalf("got %v", ordered)
		}
	})

	t.Run("rejects unknown dependency", func(t *testing.T) {
		cfg := parseConfig(t, "services:\n  api:\n    image: a\n    depends_on:\n      - nope\n")
		if _, err := serviceOrder(cfg); err == nil {
			t.Fatal("expected error for unknown dependency")
		}
	})
}

func TestParseComposeVolume(t *testing.T) {
	cases := []struct {
		in       string
		src, tgt string
	}{
		{"data:/var/lib", "data", "/var/lib"},
		{"data:/var/lib:ro", "data", "/var/lib"},
		{"./host:/in/container", "./host", "/in/container"},
		{"/abs/host:/in/container", "/abs/host", "/in/container"},
		{"anonymous", "", "anonymous"},
		{"C:\\Users\\foo:/data", "C:\\Users\\foo", "/data"},
	}
	for _, c := range cases {
		src, tgt, _ := parseComposeVolume(c.in)
		if src != c.src || tgt != c.tgt {
			t.Errorf("parseComposeVolume(%q) = (%q,%q); want (%q,%q)", c.in, src, tgt, c.src, c.tgt)
		}
	}
}

func TestComposeAppConfig(t *testing.T) {
	parse := func(t *testing.T, body string) composeService {
		t.Helper()
		var cfg composeConfig
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.Services["svc"]
	}

	t.Run("ports synthesise network entitlement", func(t *testing.T) {
		svc := parse(t, "services:\n  svc:\n    ports:\n      - \"8080:80\"\n      - \"9000\"\n")
		cfg := composeAppConfig("proj", "svc", svc, 1)
		if cfg.AppID != "proj-svc" {
			t.Fatalf("appID: %s", cfg.AppID)
		}
		var ports []appconfig.PortMapping
		for _, e := range cfg.Entitlements {
			if e.Type == appconfig.EntitlementNetwork {
				ports = e.Ports
			}
		}
		if len(ports) != 2 || ports[0].Host != 8080 || ports[0].Container != 80 || ports[1].Host != 9000 || ports[1].Container != 9000 {
			t.Fatalf("unexpected ports: %+v", ports)
		}
	})

	t.Run("network_mode host overrides ports", func(t *testing.T) {
		svc := parse(t, "services:\n  svc:\n    network_mode: host\n    ports:\n      - \"80:80\"\n")
		cfg := composeAppConfig("proj", "svc", svc, 1)
		var found bool
		for _, e := range cfg.Entitlements {
			if e.Type == appconfig.EntitlementNetwork && e.Mode == "host" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected host-mode network entitlement, got %+v", cfg.Entitlements)
		}
	})

	t.Run("named volumes become persist entitlements; bind mounts skipped", func(t *testing.T) {
		svc := parse(t, "services:\n  svc:\n    volumes:\n      - data:/var/lib\n      - ./host:/in/container\n      - /abs/host:/in/container\n      - cache:/cache:ro\n")
		cfg := composeAppConfig("proj", "svc", svc, 1)
		var persists []appconfig.Entitlement
		for _, e := range cfg.Entitlements {
			if e.Type == appconfig.EntitlementPersist {
				persists = append(persists, e)
			}
		}
		if len(persists) != 2 {
			t.Fatalf("want 2 persist entitlements, got %d: %+v", len(persists), persists)
		}
		names := map[string]string{persists[0].Name: persists[0].Path, persists[1].Name: persists[1].Path}
		if names["data"] != "/var/lib" || names["cache"] != "/cache" {
			t.Fatalf("unexpected persist mapping: %+v", names)
		}
	})

	t.Run("multi-service groups under projectName without companion", func(t *testing.T) {
		emptySvc := parse(t, "services:\n  api:\n    image: nginx\n")
		api := composeAppConfig("myapp", "api", emptySvc, 2)
		if api.AppID != "myapp" {
			t.Fatalf("multi-service appID: want %q, got %q", "myapp", api.AppID)
		}
		if api.ServiceName != "api" {
			t.Fatalf("multi-service ServiceName: want %q, got %q", "api", api.ServiceName)
		}
		if api.ContainerName() != "myapp_api" {
			t.Fatalf("multi-service ContainerName: want %q, got %q", "myapp_api", api.ContainerName())
		}
	})

	t.Run("single-service keeps legacy appID without companion", func(t *testing.T) {
		emptySvc := parse(t, "services:\n  web:\n    image: nginx\n")
		cfg := composeAppConfig("myapp", "web", emptySvc, 1)
		if cfg.AppID != "myapp-web" {
			t.Fatalf("single-service appID: want %q, got %q", "myapp-web", cfg.AppID)
		}
		if cfg.ServiceName != "" {
			t.Fatalf("single-service ServiceName: want empty, got %q", cfg.ServiceName)
		}
	})
}

func TestNormalizeImageRef(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Bare name → docker.io/library/<name>:latest.
		{"python", "docker.io/library/python:latest"},
		// Name + tag.
		{"python:3.11-slim", "docker.io/library/python:3.11-slim"},
		// Two-segment ref → docker.io/<org>/<name>.
		{"library/nginx:1.27", "docker.io/library/nginx:1.27"},
		{"bitnami/redis:7", "docker.io/bitnami/redis:7"},
		// Custom registry passes through.
		{"gcr.io/google-containers/pause:3.9", "gcr.io/google-containers/pause:3.9"},
		{"localhost:5000/foo:bar", "localhost:5000/foo:bar"},
		{"registry.example.com:5000/team/app:1.2.3", "registry.example.com:5000/team/app:1.2.3"},
		// Digest references.
		{"python@sha256:0000000000000000000000000000000000000000000000000000000000000000", "docker.io/library/python@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
		// Whitespace is trimmed.
		{"  python:3.11-slim  ", "docker.io/library/python:3.11-slim"},
		// Malformed → original input.
		{"this is not a ref", "this is not a ref"},
	}
	for _, c := range cases {
		if got := normalizeImageRef(c.in); got != c.want {
			t.Errorf("normalizeImageRef(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestComposeArgv_PreservesMultiLineScript(t *testing.T) {
	body := "services:\n  svc:\n    command:\n      - python3\n      - -c\n      - |\n        import sys\n        print('hello')\n"
	var cfg composeConfig
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	cmd, extra := composeArgv(cfg.Services["svc"])
	if cmd != "python3" {
		t.Fatalf("cmd = %q; want python3", cmd)
	}
	if len(extra) != 2 || extra[0] != "-c" {
		t.Fatalf("extra = %v", extra)
	}
	// The script body must survive intact, with its embedded newlines, so
	// the agent's strings.Fields(cmd) split can't word-split it.
	if !strings.Contains(extra[1], "import sys") || !strings.Contains(extra[1], "print('hello')") {
		t.Fatalf("script body lost; got %q", extra[1])
	}
}

func TestComposeArgv_ScalarShellSplit(t *testing.T) {
	body := "services:\n  svc:\n    command: \"python3 -m pip install -r requirements.txt\"\n"
	var cfg composeConfig
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	cmd, extra := composeArgv(cfg.Services["svc"])
	want := []string{"-m", "pip", "install", "-r", "requirements.txt"}
	if cmd != "python3" || !equalStrings(extra, want) {
		t.Fatalf("cmd=%q extra=%v; want python3 + %v", cmd, extra, want)
	}
}

func TestComposeArgv_Empty(t *testing.T) {
	body := "services:\n  svc:\n    image: alpine\n"
	var cfg composeConfig
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	cmd, extra := composeArgv(cfg.Services["svc"])
	if cmd != "" || extra != nil {
		t.Fatalf("expected empty argv, got cmd=%q extra=%v", cmd, extra)
	}
}

func TestShellSplit(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a b c", []string{"a", "b", "c"}},
		{"  spaces   between  ", []string{"spaces", "between"}},
		{`echo "hello world"`, []string{"echo", "hello world"}},
		{`echo 'single quotes work too'`, []string{"echo", "single quotes work too"}},
		{`mix "double" 'single' bare`, []string{"mix", "double", "single", "bare"}},
		{"", nil},
	}
	for _, c := range cases {
		got := shellSplit(c.in)
		if !equalStrings(got, c.want) {
			t.Errorf("shellSplit(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}

func TestComposeCompanionWarnings(t *testing.T) {
	cfg := &composeConfig{
		Services: map[string]composeService{
			"camera":   {},
			"detector": {},
		},
	}

	t.Run("nil companion produces no warnings", func(t *testing.T) {
		if w := composeCompanionWarnings(nil, cfg); len(w) != 0 {
			t.Fatalf("want no warnings, got %v", w)
		}
	})

	t.Run("all services matched", func(t *testing.T) {
		companion := &appconfig.AppConfig{
			AppID: "com.example.robot",
			Services: map[string]*appconfig.ServiceConfig{
				"camera":   {Entitlements: []appconfig.Entitlement{{Type: "camera"}}},
				"detector": {Entitlements: []appconfig.Entitlement{{Type: "gpu"}}},
			},
		}
		if w := composeCompanionWarnings(companion, cfg); len(w) != 0 {
			t.Fatalf("want no warnings for matched services, got %v", w)
		}
	})

	t.Run("unmatched service name warns", func(t *testing.T) {
		companion := &appconfig.AppConfig{
			AppID: "com.example.robot",
			Services: map[string]*appconfig.ServiceConfig{
				"camera":  {Entitlements: []appconfig.Entitlement{{Type: "camera"}}},
				"missing": {Entitlements: []appconfig.Entitlement{{Type: "gpu"}}},
			},
		}
		w := composeCompanionWarnings(companion, cfg)
		if len(w) != 1 {
			t.Fatalf("want 1 warning, got %v", w)
		}
		if !strings.Contains(w[0], "missing") {
			t.Errorf("warning should mention the unknown service name, got %q", w[0])
		}
	})

	t.Run("empty services map produces no warnings", func(t *testing.T) {
		companion := &appconfig.AppConfig{AppID: "com.example.robot"}
		if w := composeCompanionWarnings(companion, cfg); len(w) != 0 {
			t.Fatalf("want no warnings for empty services, got %v", w)
		}
	})
}

func TestApplyComposeCompanion(t *testing.T) {
	baseAppCfg := func() *appconfig.AppConfig {
		return &appconfig.AppConfig{
			AppID: "proj-camera",
			Entitlements: []appconfig.Entitlement{
				{Type: appconfig.EntitlementNetwork},
			},
		}
	}

	t.Run("nil companion is a no-op", func(t *testing.T) {
		got := baseAppCfg()
		applyComposeCompanion(got, nil, "camera")
		if len(got.Entitlements) != 1 || got.Isolation != "" || got.Frameworks != nil {
			t.Errorf("nil companion should not change AppConfig: %+v", got)
		}
	})

	t.Run("sets appId, serviceName, isolation and group frameworks", func(t *testing.T) {
		domainZero := 0
		companion := &appconfig.AppConfig{
			AppID:      "com.example.robot",
			Isolation:  "shared-ipc",
			Frameworks: &appconfig.FrameworksConfig{ROS2: &appconfig.ROS2Config{DomainID: &domainZero}},
		}
		got := baseAppCfg()
		applyComposeCompanion(got, companion, "camera")
		if got.AppID != "com.example.robot" {
			t.Errorf("AppID = %q, want %q", got.AppID, "com.example.robot")
		}
		if got.ServiceName != "camera" {
			t.Errorf("ServiceName = %q, want %q", got.ServiceName, "camera")
		}
		if got.Isolation != "shared-ipc" {
			t.Errorf("Isolation = %q, want %q", got.Isolation, "shared-ipc")
		}
		if got.Frameworks == nil || got.Frameworks.ROS2 == nil {
			t.Error("Frameworks.ROS2 should be set")
		}
	})

	t.Run("appends shared then per-service entitlements", func(t *testing.T) {
		companion := &appconfig.AppConfig{
			AppID:        "com.example.robot",
			Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementBluetooth}},
			Services: map[string]*appconfig.ServiceConfig{
				"camera": {
					Entitlements: []appconfig.Entitlement{
						{Type: appconfig.EntitlementCamera},
						{Type: appconfig.EntitlementGPU},
					},
				},
			},
		}
		got := baseAppCfg()
		applyComposeCompanion(got, companion, "camera")
		// 1 compose + 1 shared + 2 per-service = 4
		if len(got.Entitlements) != 4 {
			t.Fatalf("want 4 entitlements (1 compose + 1 shared + 2 per-service), got %d: %+v", len(got.Entitlements), got.Entitlements)
		}
	})

	t.Run("per-service frameworks override group frameworks", func(t *testing.T) {
		groupID, svcID := 0, 42
		groupROS2 := &appconfig.ROS2Config{DomainID: &groupID}
		svcROS2 := &appconfig.ROS2Config{DomainID: &svcID}
		companion := &appconfig.AppConfig{
			AppID:      "com.example.robot",
			Frameworks: &appconfig.FrameworksConfig{ROS2: groupROS2},
			Services: map[string]*appconfig.ServiceConfig{
				"camera": {
					Frameworks: &appconfig.FrameworksConfig{ROS2: svcROS2},
				},
			},
		}
		got := baseAppCfg()
		applyComposeCompanion(got, companion, "camera")
		if got.Frameworks == nil || got.Frameworks.ROS2 == nil {
			t.Fatal("Frameworks.ROS2 should be set")
		}
		if got.Frameworks.ROS2.DomainID == nil || *got.Frameworks.ROS2.DomainID != 42 {
			t.Errorf("DomainID = %v, want 42 (per-service override)", got.Frameworks.ROS2.DomainID)
		}
	})

	t.Run("group frameworks apply when service has no frameworks", func(t *testing.T) {
		domainFive := 5
		groupROS2 := &appconfig.ROS2Config{DomainID: &domainFive}
		companion := &appconfig.AppConfig{
			AppID:      "com.example.robot",
			Frameworks: &appconfig.FrameworksConfig{ROS2: groupROS2},
			Services: map[string]*appconfig.ServiceConfig{
				"camera": {Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}},
			},
		}
		got := baseAppCfg()
		applyComposeCompanion(got, companion, "camera")
		if got.Frameworks == nil || got.Frameworks.ROS2 == nil || got.Frameworks.ROS2.DomainID == nil || *got.Frameworks.ROS2.DomainID != 5 {
			t.Errorf("expected group-level ROS2 DomainID=5, got %+v", got.Frameworks)
		}
	})

	t.Run("unknown service uses only group config", func(t *testing.T) {
		companion := &appconfig.AppConfig{
			AppID:     "com.example.robot",
			Isolation: "shared-ipc",
			Services: map[string]*appconfig.ServiceConfig{
				"camera": {Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}},
			},
		}
		got := baseAppCfg()
		applyComposeCompanion(got, companion, "detector") // not in services map
		if got.Isolation != "shared-ipc" {
			t.Errorf("Isolation = %q, want %q", got.Isolation, "shared-ipc")
		}
		// No per-service entitlements should be added.
		if len(got.Entitlements) != 1 {
			t.Errorf("want 1 entitlement (no per-service addition), got %d", len(got.Entitlements))
		}
	})
}

func TestDeduplicateEntitlements(t *testing.T) {
	gpu := appconfig.Entitlement{Type: appconfig.EntitlementGPU}
	cam := appconfig.Entitlement{Type: appconfig.EntitlementCamera}
	net := appconfig.Entitlement{Type: appconfig.EntitlementNetwork, Mode: "host"}
	persist1 := appconfig.Entitlement{Type: appconfig.EntitlementPersist, Name: "data"}
	persist2 := appconfig.Entitlement{Type: appconfig.EntitlementPersist, Name: "logs"}

	t.Run("removes exact duplicates", func(t *testing.T) {
		got := deduplicateEntitlements([]appconfig.Entitlement{gpu, cam, gpu})
		if len(got) != 2 {
			t.Fatalf("want 2, got %d: %+v", len(got), got)
		}
	})

	t.Run("preserves order, first occurrence wins", func(t *testing.T) {
		got := deduplicateEntitlements([]appconfig.Entitlement{gpu, cam, gpu, net})
		if len(got) != 3 || got[0].Type != appconfig.EntitlementGPU || got[1].Type != appconfig.EntitlementCamera {
			t.Fatalf("unexpected order: %+v", got)
		}
	})

	t.Run("distinct persist names are kept", func(t *testing.T) {
		got := deduplicateEntitlements([]appconfig.Entitlement{persist1, persist2, persist1})
		if len(got) != 2 {
			t.Fatalf("want 2, got %d: %+v", len(got), got)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := deduplicateEntitlements(nil); len(got) != 0 {
			t.Fatalf("want empty, got %+v", got)
		}
	})
}

func TestComposeRestartPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want agentpb.RestartPolicyMode
	}{
		{"always", agentpb.RestartPolicyMode_UNLESS_STOPPED},
		{"unless-stopped", agentpb.RestartPolicyMode_UNLESS_STOPPED},
		{"on-failure", agentpb.RestartPolicyMode_ON_FAILURE},
		{"no", agentpb.RestartPolicyMode_NO},
		{"", agentpb.RestartPolicyMode_NO},
		{"weird", agentpb.RestartPolicyMode_DEFAULT},
	}
	for _, c := range cases {
		got := composeRestartPolicy(c.in).GetMode()
		if got != c.want {
			t.Errorf("composeRestartPolicy(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}

func TestComposeWarnUnsupportedFields(t *testing.T) {
	raw := `
services:
  api:
    image: python:3.11
    devices:
      - /dev/video0
    privileged: true
    ipc: host
`
	var cfg composeConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}

	warnings := unsupportedComposeWarnings(cfg.Services["api"])

	contains := func(field string) bool {
		for _, w := range warnings {
			if w == field {
				return true
			}
		}
		return false
	}
	if !contains("devices") {
		t.Errorf("expected 'devices' in warnings, got %v", warnings)
	}
	if !contains("privileged") {
		t.Errorf("expected 'privileged' in warnings, got %v", warnings)
	}
	if !contains("ipc") {
		t.Errorf("expected 'ipc' in warnings, got %v", warnings)
	}
}

func TestComposeWarnUnsupportedFields_NoneWhenClean(t *testing.T) {
	raw := `
services:
  api:
    image: python:3.11
    environment:
      - FOO=bar
    restart: unless-stopped
`
	var cfg composeConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}

	warnings := unsupportedComposeWarnings(cfg.Services["api"])
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for clean service, got %v", warnings)
	}
}
