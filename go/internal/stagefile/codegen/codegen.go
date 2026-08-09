// Package codegen compiles a validated Stagefile spec into Dockerfile text.
// Generate is a pure function: given a spec and a map of already-resolved
// image digests, it returns the exact Dockerfile bytes, or an error if a
// referenced image has no resolved digest. Every helper in this package
// takes typed fields and returns fixed strings — none of them accept or
// interpolate a user-supplied shell string.
package codegen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// defaultUser is the distroless-style non-root numeric UID used when a
// final stage doesn't declare an explicit user. A numeric UID needs no
// /etc/passwd entry, so it works on any base image.
const defaultUser = "65532"

// Generate compiles f into Dockerfile text. images maps every pinned from:
// value in f to its resolved "sha256:..." digest, and downloads maps every
// download url that declared no sha256 of its own to the one resolved for it
// (see internal/lock). platform, if non-empty (e.g. "linux/arm64"), is
// applied to every FROM via --platform; pass "" to omit it and let the
// builder decide. A stage's own `platform: build` overrides it with
// $BUILDPLATFORM for that stage.
func Generate(f *spec.File, images, downloads map[string]string, platform string) (string, error) {
	var blocks []string
	lastIdx := len(f.Stages) - 1

	for i, s := range f.Stages {
		pinned := s.Pin == nil || *s.Pin
		digest := images[s.From]
		if pinned && digest == "" {
			return "", fmt.Errorf("stage %q: no resolved digest for %q; run `stagefile lock`", s.Name, s.From)
		}
		if !pinned {
			digest = ""
		}

		stagePlatform := platform
		if s.Platform == "build" {
			stagePlatform = "$BUILDPLATFORM"
		}

		lines := []string{fromLine(s.From, digest, s.Name, stagePlatform)}
		lines = append(lines, kvLines("ARG", s.Args)...)
		lines = append(lines, kvLines("ENV", s.Env)...)
		if s.Workdir != "" {
			lines = append(lines, "WORKDIR "+s.Workdir)
		}

		// Fetches come before install: a download is the largest and most
		// stable thing in a stage, and behind the install step a bumped pip
		// package would re-fetch every model. Nothing invalidates an ADD but
		// its own url and checksum.
		fetches, err := downloadFetchLines(s.Download, downloads)
		if err != nil {
			return "", fmt.Errorf("stage %q: %w", s.Name, err)
		}
		lines = append(lines, fetches...)

		if s.Install != nil {
			if s.Install.Apt != nil {
				lines = append(lines, aptInstallLines(s.Install.Apt)...)
			}
			if s.Install.Apk != nil {
				lines = append(lines, apkInstallLines(s.Install.Apk)...)
			}
			if len(s.Install.CMake) > 0 {
				lines = append(lines, cmakeInstallLines(s.Install.CMake, stagePlatform)...)
			}
			if s.Install.Pip != nil {
				lines = append(lines, pipInstallLines(s.Install.Pip)...)
			}
			if s.Install.Npm != nil {
				lines = append(lines, npmInstallLines(s.Install.Npm)...)
			}
			if s.Install.Uv != nil {
				lines = append(lines, uvInstallLines(s.Install.Uv)...)
			}
		}

		// Unpacking comes after install, because it needs a tool in the
		// image and this is the only position where `extract: zip` can rely
		// on unzip having been declared in install.apt.packages.
		lines = append(lines, downloadExtractLines(s.Download)...)

		if len(s.Copy) > 0 {
			lines = append(lines, copyLines(s.Copy)...)
		}

		if s.Build != nil {
			bl, err := buildLines(s.Build)
			if err != nil {
				return "", fmt.Errorf("stage %q: %w", s.Name, err)
			}
			lines = append(lines, bl...)
		}

		if i == lastIdx {
			if s.Healthcheck != nil {
				lines = append(lines, healthcheckLine(s.Healthcheck))
			}
			if s.Entrypoint != nil {
				lines = append(lines, entrypointLine(s.Entrypoint))
			}
			if len(s.Cmd) > 0 {
				lines = append(lines, "CMD "+jsonArgv(s.Cmd))
			}
			user := s.User
			if user == "" {
				user = defaultUser
			}
			lines = append(lines, "USER "+user)
		}

		blocks = append(blocks, strings.Join(lines, "\n"))
	}

	return strings.Join(blocks, "\n\n") + "\n", nil
}

func fromLine(image, digest, name, platform string) string {
	plat := ""
	if platform != "" {
		plat = "--platform=" + platform + " "
	}
	ref := image
	if digest != "" {
		ref = image + "@" + digest
	}
	return fmt.Sprintf("FROM %s%s AS %s", plat, ref, name)
}

// kvLines renders an ARG/ENV block sorted by key, one instruction per key
// so a later value change invalidates only from that line down.
func kvLines(instruction string, m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		v := m[k]
		if instruction == "ARG" && v == "" {
			lines = append(lines, "ARG "+k)
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s=%s", instruction, k, strconv.Quote(v)))
	}
	return lines
}

// jsonArgv renders an argv slice as the JSON array Dockerfile exec forms
// require.
func jsonArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, s := range argv {
		quoted[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func entrypointLine(e *spec.Entrypoint) string {
	argv := e.Exec
	if e.Source != "" {
		// Source-then-exec wrapper: bash sources the named file, then execs
		// the declared argv passed through untouched as "$@" — no Exec
		// argument is ever parsed by the shell. Requires bash in the image.
		inner := "source " + shellQuote(e.Source) + ` && exec "$@"`
		argv = append([]string{"/bin/bash", "-c", inner, "bash"}, e.Exec...)
	}
	return "ENTRYPOINT " + jsonArgv(argv)
}

func healthcheckLine(h *spec.Healthcheck) string {
	parts := []string{"HEALTHCHECK"}
	if h.Interval != "" {
		parts = append(parts, "--interval="+h.Interval)
	}
	if h.Timeout != "" {
		parts = append(parts, "--timeout="+h.Timeout)
	}
	if h.StartPeriod != "" {
		parts = append(parts, "--start-period="+h.StartPeriod)
	}
	if h.Retries > 0 {
		parts = append(parts, fmt.Sprintf("--retries=%d", h.Retries))
	}
	parts = append(parts, "CMD", jsonArgv(h.Exec))
	return strings.Join(parts, " ")
}

// shellQuote wraps s in single quotes for safe interpolation into a
// shell-form RUN command, so shell metacharacters in s — including the
// ">"/"<" in an ordinary pip version specifier like "flask>=2.0" — are
// never given special meaning by /bin/sh. This is strictly more complete
// than a character denylist: it doesn't require enumerating "dangerous"
// characters, several of which (>, <, space) are also legal and necessary
// in real package specifiers.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// cacheRun renders a RUN backed by a BuildKit cache mount at dir.
//
// Every cache mount goes through here so none can be emitted without a sharing
// mode. BuildKit treats an unqualified cache mount as sharing=shared, meaning
// concurrent builds may use the same directory at once — and Wendy builds up to
// four services concurrently, on top of BuildKit's own parallel execution of
// independent stages within a single Dockerfile. Package managers survive that
// (they lock internally), but the waiting then happens invisibly inside cargo
// or npm. sharing=locked moves the queueing to the mount, where BuildKit
// reports it as part of the build graph.
func cacheRun(dir, cmd string) string {
	return cacheRunID("", dir, cmd)
}

// cacheRunID is cacheRun with an explicit BuildKit cache id. Without one a
// mount is scoped by its target path, which is fine for the package-manager
// caches (their contents are self-describing — a wheel names its own platform)
// but not for a build tree, where the same path in two different builds holds
// object files that must never meet. An id lets the caller say exactly what
// may share.
func cacheRunID(id, dir, cmd string) string {
	mount := "type=cache,sharing=locked"
	if id != "" {
		mount += ",id=" + id
	}
	return fmt.Sprintf("RUN --mount=%s,target=%s %s", mount, dir, cmd)
}

// cmakeCacheID scopes a cmake build tree to the project and the architecture
// it is compiled for, and deliberately to nothing else. Commit is excluded so
// bumping a pin recompiles only what changed — the reason the cache exists.
// Everything else the Stagefile can set (build type, prefix, defines) is an
// ordinary CMake cache variable that CMake reconfigures in place; the target
// platform is the one input it cannot detect, because the compiler sits at the
// same path in either rootfs.
func cmakeCacheID(repository, platform string) string {
	sum := sha256.Sum256([]byte(repository + "\x00" + platform))
	return "stagefile-cmake-" + hex.EncodeToString(sum[:])[:16]
}

// aptRepositoryLines emits the declared extra apt sources: ca-certificates
// bootstrap (an https sources.list URL fails apt-get update without it —
// stock ubuntu/debian images don't ship it), the pinned signing key fetched
// by BuildKit itself (ADD --checksum, so the fetch never runs inside the
// container and the key can't drift), and one sources.list.d entry per
// repository.
func aptRepositoryLines(repos []spec.AptRepository) []string {
	if len(repos) == 0 {
		return nil
	}
	lines := []string{
		"RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \\",
		"    && rm -rf /var/lib/apt/lists/*",
	}
	for _, r := range repos {
		ext := ".gpg"
		if r.Key.Format == "armored" {
			ext = ".asc"
		}
		keyring := "/etc/apt/keyrings/" + r.Name + ext
		sha := strings.TrimPrefix(r.Key.SHA256, "sha256:")
		lines = append(lines, fmt.Sprintf("ADD --chmod=0644 --checksum=sha256:%s %s %s", sha, r.Key.URL, keyring))
		var srcLines []string
		for _, suite := range r.Suites {
			srcLines = append(srcLines, shellQuote(fmt.Sprintf("deb [signed-by=%s] %s %s %s",
				keyring, r.URL, suite, strings.Join(r.Components, " "))))
		}
		lines = append(lines, fmt.Sprintf("RUN printf '%%s\\n' %s > /etc/apt/sources.list.d/%s.list",
			strings.Join(srcLines, " "), r.Name))
	}
	return lines
}

func aptInstallLines(a *spec.AptInstall) []string {
	lines := aptRepositoryLines(a.Repositories)
	parts := []string{"apt-get", "update", "&&", "apt-get", "install", "-y"}
	if !a.Recommends {
		parts = append(parts, "--no-install-recommends")
	}
	for _, p := range a.Packages {
		parts = append(parts, shellQuote(p))
	}
	return append(lines,
		"RUN "+strings.Join(parts, " ")+" \\",
		"    && rm -rf /var/lib/apt/lists/*",
	)
}

func apkInstallLines(a *spec.ApkInstall) []string {
	var lines []string
	if len(a.Repositories) > 0 {
		var quoted []string
		for _, r := range a.Repositories {
			quoted = append(quoted, shellQuote(r))
		}
		lines = append(lines, fmt.Sprintf("RUN printf '%%s\\n' %s >> /etc/apk/repositories", strings.Join(quoted, " ")))
	}
	parts := []string{"apk", "add"}
	if !a.Cache {
		parts = append(parts, "--no-cache")
	}
	for _, p := range a.Packages {
		parts = append(parts, shellQuote(p))
	}
	return append(lines, "RUN "+strings.Join(parts, " "))
}

func cmakeInstallLines(installs []spec.CMakeInstall, platform string) []string {
	lines := make([]string, 0, len(installs))
	for i, c := range installs {
		root := fmt.Sprintf("/tmp/stagefile-cmake-%d", i)
		sourceDir := root + "/source"
		buildDir := root + "/build"
		prefix := c.Prefix
		if prefix == "" {
			prefix = "/usr/local"
		}
		buildType := c.BuildType
		if buildType == "" {
			buildType = "Release"
		}

		configure := []string{
			"cmake", "-S", shellQuote(sourceDir), "-B", shellQuote(buildDir),
			shellQuote("-DCMAKE_BUILD_TYPE=" + buildType),
			shellQuote("-DCMAKE_INSTALL_PREFIX=" + prefix),
		}
		keys := make([]string, 0, len(c.Defines))
		for k := range c.Defines {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			configure = append(configure, shellQuote("-D"+k+"="+c.Defines[k]))
		}

		build := "cmake --build " + shellQuote(buildDir)
		if c.Jobs > 0 {
			build += " --parallel " + strconv.Itoa(c.Jobs)
		}
		commands := []string{
			"git init " + shellQuote(sourceDir),
			"git -C " + shellQuote(sourceDir) + " remote add origin " + shellQuote(c.Repository),
			"git -C " + shellQuote(sourceDir) + " fetch --depth 1 origin " + shellQuote(c.Commit),
			"git -C " + shellQuote(sourceDir) + " checkout --detach FETCH_HEAD",
			strings.Join(configure, " "),
			build,
			"cmake --install " + shellQuote(buildDir),
			// Only the source tree is removed. The build tree is a cache mount
			// and never enters the layer, so deleting it would throw away the
			// object files the next commit bump wants — while removing nothing
			// from the image.
			"rm -rf " + shellQuote(sourceDir),
		}
		lines = append(lines, cacheRunID(cmakeCacheID(c.Repository, platform), buildDir,
			strings.Join(commands, " \\\n    && ")))
	}
	return lines
}

func pipInstallLines(p *spec.PipInstall) []string {
	var lines []string
	if p.Requirements != "" {
		lines = append(lines, fmt.Sprintf("COPY %s %s", p.Requirements, p.Requirements))
	}
	// No --no-cache-dir here: the cache mount below already keeps pip's
	// cache out of the image layer, and disabling the cache would force a
	// full wheel re-download every time this layer rebuilds.
	parts := []string{"pip", "install"}
	if p.Index != "" {
		parts = append(parts, "--index-url", shellQuote(p.Index))
	}
	for _, u := range p.ExtraIndex {
		parts = append(parts, "--extra-index-url", shellQuote(u))
	}
	if p.Requirements != "" {
		parts = append(parts, "-r", shellQuote(p.Requirements))
	}
	for _, pkg := range p.Packages {
		parts = append(parts, shellQuote(pkg))
	}
	lines = append(lines, cacheRun("/root/.cache/pip", strings.Join(parts, " ")))
	return lines
}

func npmInstallLines(n *spec.NpmInstall) []string {
	manager := n.Manager
	if manager == "" {
		manager = "npm"
	}
	var cmd, cacheDir string
	switch manager {
	case "yarn":
		cmd, cacheDir = "yarn install --frozen-lockfile", "/root/.cache/yarn"
		if n.Production {
			cmd += " --production"
		}
	case "pnpm":
		cmd, cacheDir = "pnpm install --frozen-lockfile", "/root/.local/share/pnpm/store"
		if n.Production {
			cmd += " --prod"
		}
	default:
		cmd, cacheDir = "npm ci", "/root/.npm"
		if n.Production {
			cmd += " --omit=dev"
		}
	}
	return []string{
		fmt.Sprintf("COPY package.json %s ./", spec.NpmLockfile(n.Manager)),
		cacheRun(cacheDir, cmd),
	}
}

func uvInstallLines(u *spec.UvInstall) []string {
	parts := []string{"uv", "sync", "--frozen"}
	if !u.Dev {
		parts = append(parts, "--no-dev")
	}
	for _, e := range u.Extras {
		parts = append(parts, "--extra", shellQuote(e))
	}
	return []string{
		"COPY " + strings.Join(spec.UvLocalFiles, " ") + " ./",
		cacheRun("/root/.cache/uv", strings.Join(parts, " ")),
	}
}

// downloadStagingPath is where an archive lands before it is unpacked. It is
// keyed to the download's index within its stage, so identical source always
// compiles to identical bytes — nothing here is random or time-derived.
func downloadStagingPath(i int, extract string) string {
	return fmt.Sprintf("/tmp/stagefile-download-%d.%s", i, extract)
}

// downloadChecksum returns the sha256 to pin d with: the one written in the
// Stagefile, or failing that the one resolved into the lockfile. An
// unpinned download is not representable in the output, so a download with
// neither is an error rather than a plain ADD.
func downloadChecksum(d spec.Download, resolved map[string]string) (string, error) {
	digest := d.SHA256
	if digest == "" {
		digest = resolved[d.URL]
	}
	if digest == "" {
		return "", fmt.Errorf("download %q: no resolved sha256; run a build with network access to pin it, or write sha256: in the Stagefile", d.URL)
	}
	return "sha256:" + strings.TrimPrefix(digest, "sha256:"), nil
}

// downloadFetchLines emits one ADD per download. BuildKit performs the fetch
// and verifies the checksum before any layer can read the bytes, so nothing
// here runs a shell inside the container.
func downloadFetchLines(entries []spec.Download, resolved map[string]string) ([]string, error) {
	var lines []string
	for i, d := range entries {
		checksum, err := downloadChecksum(d, resolved)
		if err != nil {
			return nil, err
		}
		flags := ""
		if d.Owner != "" {
			flags += "--chown=" + d.Owner + " "
		}
		if d.Mode != "" {
			flags += "--chmod=" + d.Mode + " "
		}
		dest := d.Dest
		if d.Extract != "" {
			dest = downloadStagingPath(i, d.Extract)
		}
		lines = append(lines, fmt.Sprintf("ADD %s--checksum=%s %s %s", flags, checksum, d.URL, dest))
	}
	return lines, nil
}

// downloadExtractLines emits the unpack step for every download that declared
// one. A remote ADD never auto-extracts the way a local tarball does, so this
// is forced by BuildKit rather than chosen. The command is assembled from
// typed fields through shellQuote — the compiler writes the shell line, never
// the Stagefile's author.
func downloadExtractLines(entries []spec.Download) []string {
	var lines []string
	for i, d := range entries {
		if d.Extract == "" {
			continue
		}
		staged := shellQuote(downloadStagingPath(i, d.Extract))
		dest := shellQuote(d.Dest)
		var unpack string
		switch d.Extract {
		case "zip":
			unpack = fmt.Sprintf("unzip -q %s -d %s", staged, dest)
		default:
			unpack = fmt.Sprintf("tar -xzf %s -C %s", staged, dest)
		}
		// The staged archive is removed in the same RUN that unpacks it:
		// left to a later layer it would still be in this one, and the image
		// would carry both the tarball and its contents.
		lines = append(lines, fmt.Sprintf("RUN mkdir -p %s && %s && rm %s", dest, unpack, staged))
	}
	return lines
}

func copyLines(entries []spec.CopyEntry) []string {
	var lines []string
	for _, e := range entries {
		dest := e.Dest
		if dest == "" {
			dest = e.Paths[0]
		}
		// BuildKit requires a multi-source COPY's destination to end with
		// "/"; a dest without one validates here but hard-fails at docker
		// build with a raw BuildKit error. Multiple sources make the intent
		// (a directory) unambiguous, so append it.
		if len(e.Paths) > 1 && !strings.HasSuffix(dest, "/") {
			dest += "/"
		}
		flags := ""
		if e.From != "local" {
			flags += "--from=" + e.From + " "
		}
		if e.Owner != "" {
			flags += "--chown=" + e.Owner + " "
		}
		if e.Mode != "" {
			flags += "--chmod=" + e.Mode + " "
		}
		lines = append(lines, fmt.Sprintf("COPY %s%s %s", flags, strings.Join(e.Paths, " "), dest))
	}
	return lines
}

func buildLines(b *spec.Build) ([]string, error) {
	profile := b.Profile
	if profile == "" {
		profile = "release"
	}
	switch b.Lang {
	case "rust":
		cmd := "cargo build"
		if profile == "release" {
			cmd += " --release"
		}
		if b.Product != "" {
			cmd += " --bin " + shellQuote(b.Product)
		}
		return []string{cacheRun("/root/.cargo", cmd)}, nil
	case "go":
		if b.Product != "" {
			// -o with a trailing slash writes the package's binary (named
			// after the package) into that directory.
			return []string{cacheRun("/root/.cache/go-build", fmt.Sprintf(
				"go build -o /usr/local/bin/ %s", shellQuote(b.Product)))}, nil
		}
		return []string{cacheRun("/root/.cache/go-build", "go build ./...")}, nil
	case "swift":
		cmd := "swift build"
		if profile == "release" {
			cmd += " -c release"
		}
		if b.Product != "" {
			cmd += " --product " + shellQuote(b.Product)
		}
		return []string{cacheRun("/root/.swiftpm", cmd)}, nil
	case "npm", "yarn", "pnpm":
		script := b.Script
		if script == "" {
			script = "build"
		}
		cacheDir := map[string]string{
			"npm":  "/root/.npm",
			"yarn": "/root/.cache/yarn",
			"pnpm": "/root/.local/share/pnpm/store",
		}[b.Lang]
		return []string{cacheRun(cacheDir, fmt.Sprintf("%s run %s",
			b.Lang, shellQuote(script)))}, nil
	default:
		return nil, fmt.Errorf("unsupported build.lang %q (supported: rust, go, swift, npm, yarn, pnpm)", b.Lang)
	}
}
