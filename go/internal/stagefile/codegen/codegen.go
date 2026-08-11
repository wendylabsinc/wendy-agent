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
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/gpu"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// defaultUser is the distroless-style non-root numeric UID used when a
// final stage doesn't declare an explicit user. A numeric UID needs no
// /etc/passwd entry, so it works on any base image.
const defaultUser = "65532"

// Option configures a Generate call.
type Option func(*options)

type options struct {
	cacheScope string
}

// WithCacheScope names the project being built, for the cache-mount ids that
// must not be shared between projects — currently the Swift build tree, which
// is the only compiler cache here that holds object files rather than
// self-describing downloaded artifacts.
//
// It is an option rather than a parameter because it is not needed to produce
// a correct Dockerfile, only an efficient one: without it, projects whose
// stages agree on toolchain, platform and profile take turns invalidating one
// build tree. Callers pass something stable and unique per project;
// CompileFile passes the project directory.
func WithCacheScope(scope string) Option {
	return func(o *options) { o.cacheScope = scope }
}

// Generate compiles f into Dockerfile text. images maps every pinned from:
// value in f to its resolved "sha256:..." digest, and downloads maps every
// download url that declared no sha256 of its own to the one resolved for it
// (see internal/lock). platform, if non-empty (e.g. "linux/arm64"), is
// applied to every FROM via --platform; pass "" to omit it and let the
// builder decide. A stage's own `platform: build` overrides it with
// $BUILDPLATFORM for that stage.
//
// cudaProfile is the resolved GPU profile for the architecture being built
// for, and is required if any stage declares cuda:. It is passed in rather
// than looked up here because it is pinned in the lockfile: the compiler must
// emit what was recorded, not what this binary's table says today.
func Generate(f *spec.File, images, downloads map[string]string, platform string, cudaProfile *gpu.Profile, opts ...Option) (string, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
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

		if s.CUDA && cudaProfile == nil {
			return "", fmt.Errorf("stage %q declares cuda: but no GPU target was resolved for this build", s.Name)
		}

		lines := []string{fromLine(s.From, digest, s.Name, stagePlatform)}
		lines = append(lines, kvLines("ARG", s.Args)...)
		lines = append(lines, kvLines("ENV", stageEnv(&s, cudaProfile))...)
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

		// An absent install: is compiled as an empty one rather than skipped,
		// because pipGroupLines is also where a GPU stage's CUDA runtime is
		// spliced in, and that stage needs the runtime whether or not it
		// declares any install of its own — the collection below imports it.
		// With no fields set, nothing else here emits a line.
		install := s.Install
		if install == nil {
			install = &spec.Install{}
		}
		if install.Apt != nil {
			lines = append(lines, aptInstallLines(install.Apt)...)
		}
		if install.Apk != nil {
			lines = append(lines, apkInstallLines(install.Apk)...)
		}
		if len(install.CMake) > 0 {
			lines = append(lines, cmakeInstallLines(install.CMake, stagePlatform)...)
		}
		lines = append(lines, pipGroupLines(install.Pip, s.CUDA, cudaProfile, stagePlatform)...)
		if install.Npm != nil {
			lines = append(lines, npmInstallLines(install.Npm)...)
		}
		if install.Uv != nil {
			lines = append(lines, uvInstallLines(install.Uv, stagePlatform)...)
		}

		// Unpacking comes after install, because it needs a tool in the
		// image and this is the only position where `extract: zip` can rely
		// on unzip having been declared in install.apt.packages.
		lines = append(lines, downloadExtractLines(s.Download)...)

		// Collection runs after every install (it reads what they produced)
		// and before copy: (so editing app source never reruns it).
		if s.CUDA {
			lines = append(lines, cudaCollectLines(*cudaProfile)...)
		}

		if len(s.Copy) > 0 {
			lines = append(lines, copyLines(s.Copy)...)
		}

		if s.Build != nil {
			bl, err := buildLines(s.Build, s.From, stagePlatform, o.cacheScope)
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
				if s.CUDA {
					// CUDA's memory manager opens /dev/nvmap, which is
					// root-only on a Jetson. A GPU stage that took the
					// non-root default would build clean and then fail on the
					// device at the first allocation, so the declaration that
					// the stage uses the GPU is also the declaration that it
					// needs root. An explicit user: still wins.
					user = "root"
				}
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
	return "RUN " + mountFlag(cacheMount{id: id, target: dir}) + " " + cmd
}

// cacheMount is one BuildKit cache mount: a target path, and the id that
// decides which other builds may share it (empty means "scoped by target
// path", BuildKit's default).
type cacheMount struct {
	id     string
	target string
}

// mountFlag renders one cache mount's --mount= flag. Every cache mount in the
// generated Dockerfile is rendered here, so none can be emitted without a
// sharing mode — see cacheRun for why sharing=locked rather than BuildKit's
// default.
func mountFlag(m cacheMount) string {
	mount := "type=cache,sharing=locked"
	if m.id != "" {
		mount += ",id=" + m.id
	}
	return fmt.Sprintf("--mount=%s,target=%s", mount, m.target)
}

// cacheRunMounts is cacheRunID for a step that needs more than one cache
// mount, which a compiled build usually does: one cache for the dependencies
// it downloads and a separate one for the object files it produces, because
// the two are invalidated by completely different things.
func cacheRunMounts(mounts []cacheMount, cmd string) string {
	parts := make([]string, 0, len(mounts)+1)
	for _, m := range mounts {
		parts = append(parts, mountFlag(m))
	}
	return "RUN " + strings.Join(append(parts, cmd), " \\\n    ")
}

// scopedCacheID hashes the parts that decide whether two builds can share a
// cache into an opaque BuildKit mount id. Callers pass every input that changes
// what lands in the cache and nothing else: too narrow a scope makes builds
// contend for no benefit, too wide a scope makes them miss each other's work.
func scopedCacheID(kind string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		io.WriteString(h, p)
		h.Write([]byte{0})
	}
	return "stagefile-" + kind + "-" + hex.EncodeToString(h.Sum(nil))[:16]
}

// pipCacheID scopes pip's wheel cache to the index set it can download from and
// the platform whose wheels it stores.
//
// Both matter because the mount stays sharing=locked: without an id, BuildKit
// scopes by target path, so every pip install in every concurrently-built
// service queues on ONE lock — including installs that could never share a
// wheel because they pull from different indexes or build for different
// architectures. Scoping removes that false contention while keeping the real
// sharing: same index, same platform still means one mount, so the second build
// gets the first one's downloads instead of re-fetching them.
// index is the group's effective index, which for a cuda: group comes from the
// resolved GPU profile rather than the Stagefile — scoping on p.Index would put
// every GPU group in the one unnamed-index cache alongside PyPI wheels, which
// is exactly the mixing this ID exists to prevent.
func pipCacheID(p *spec.PipInstall, index, platform string) string {
	parts := append([]string{platform, index}, p.ExtraIndex...)
	return scopedCacheID("pip", parts...)
}

// cmakeCacheID scopes a cmake build tree to the project and the architecture
// it is compiled for, and deliberately to nothing else. Commit is excluded so
// bumping a pin recompiles only what changed — the reason the cache exists.
// Everything else the Stagefile can set (build type, prefix, defines) is an
// ordinary CMake cache variable that CMake reconfigures in place; the target
// platform is the one input it cannot detect, because the compiler sits at the
// same path in either rootfs.
func cmakeCacheID(repository, platform string) string {
	return scopedCacheID("cmake", repository, platform)
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

// pipInstallLines emits one pip group. A group marked cuda: takes its index
// from cudaProfile instead of the Stagefile, which is what lets the same
// source resolve GPU wheels for whichever board it is being built for.
func pipInstallLines(p *spec.PipInstall, cudaProfile *gpu.Profile, platform string) []string {
	var lines []string
	if p.Requirements != "" {
		lines = append(lines, fmt.Sprintf("COPY %s %s", p.Requirements, p.Requirements))
	}
	// No --no-cache-dir here: the cache mount below already keeps pip's
	// cache out of the image layer, and disabling the cache would force a
	// full wheel re-download every time this layer rebuilds.
	parts := []string{"pip", "install"}
	index := p.Index
	if p.CUDA && cudaProfile != nil {
		index = cudaProfile.Index
	}
	if index != "" {
		parts = append(parts, "--index-url", shellQuote(index))
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
	lines = append(lines, cacheRunID(pipCacheID(p, index, platform), "/root/.cache/pip", strings.Join(parts, " ")))
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

func uvInstallLines(u *spec.UvInstall, platform string) []string {
	parts := []string{"uv", "sync", "--frozen"}
	if !u.Dev {
		parts = append(parts, "--no-dev")
	}
	for _, e := range u.Extras {
		parts = append(parts, "--extra", shellQuote(e))
	}
	return []string{
		"COPY " + strings.Join(spec.UvLocalFiles, " ") + " ./",
		cacheRunID(scopedCacheID("uv", platform), "/root/.cache/uv", strings.Join(parts, " ")),
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

// cudaConfPath is where a CUDA stage registers its collected library
// directory with the dynamic loader. The 000- prefix puts it first among
// ld.so.conf.d entries.
const cudaConfPath = "/etc/ld.so.conf.d/000-stagefile-cuda.conf"

// cudaCollectLines symlinks every shared object the CUDA runtime wheels
// installed into the profile's LibDir, registers that directory with the
// dynamic loader, and refreshes the cache.
//
// This is the step nobody would think to write. On a JetPack-7 device the
// container runtime injects the host's CUDA 13 via CDI; the wheels installed
// above are CUDA 12. Their sonames differ where that is lucky
// (libcudart.so.12 vs .13) and collide where it is not (libcudnn.so.9 exists
// in both). Without this the loader satisfies some of a framework's
// dependencies from the wheel and others from the host, and the failure
// surfaces on the device as a missing symbol — nowhere near the build.
//
// The wheel directory is located by asking Python where it put the package
// rather than by naming a dist-packages path, because that path carries the
// base image's Python version and would silently find nothing after a base
// image bump. `find -exec ln -sf` is the compiler's own command assembled
// from typed fields, the same posture as aptRepositoryLines' sources.list
// write — no Stagefile string reaches the shell.
func cudaCollectLines(p gpu.Profile) []string {
	dir := shellQuote(p.LibDir)
	commands := []string{
		"mkdir -p " + dir,
		// python3 -c is the compiler's literal, and importing the package pip
		// just installed is also the check that it is there: a GPU stage whose
		// runtime failed to install fails here, not on the device.
		`NVIDIA_DIR="$(python3 -c 'import nvidia, os; print(os.path.dirname(nvidia.__file__))')"`,
		// -exec ln -sf {} dir/ ';' — the trailing slash makes ln treat the
		// destination as a directory, so each link keeps the library's own
		// name rather than overwriting a single file.
		fmt.Sprintf(`find "$NVIDIA_DIR" -name '*.so*' -exec ln -sf '{}' %s ';'`, shellQuote(p.LibDir+"/")),
		fmt.Sprintf("printf '%%s\\n' %s > %s", dir, shellQuote(cudaConfPath)),
		"ldconfig",
	}
	return []string{"RUN " + strings.Join(commands, " \\\n    && ")}
}

// stageEnv returns the ENV map to emit for s: the Stagefile's own, plus the
// loader path a CUDA stage needs.
//
// ld.so.conf alone is not enough. A Jetson's CDI injection puts the host's
// CUDA directories on the container's loader path in a position that beats
// ld.so.conf.d, so the collected directory has to be on LD_LIBRARY_PATH to
// win. validateCUDA refuses a Stagefile that sets the variable itself, so
// there is nothing here to merge with.
func stageEnv(s *spec.Stage, cudaProfile *gpu.Profile) map[string]string {
	if !s.CUDA || cudaProfile == nil {
		return s.Env
	}
	env := make(map[string]string, len(s.Env)+1)
	for k, v := range s.Env {
		env[k] = v
	}
	env[spec.LDLibraryPath] = cudaProfile.LibDir
	return env
}

// pipGroupLines emits the stage's pip groups, splicing in the CUDA runtime
// group a GPU stage needs.
//
// The runtime lands directly after the last group that asked for the GPU
// index, and before any ordinary PyPI group. That position is not cosmetic:
// the runtime changes only when the profile does, while app dependencies
// change constantly, and a later group's edit must not invalidate the layer
// holding several hundred megabytes of CUDA libraries.
func pipGroupLines(groups []spec.PipInstall, stageCUDA bool, cudaProfile *gpu.Profile, platform string) []string {
	runtimeAfter := -1
	for i, g := range groups {
		if g.CUDA {
			runtimeAfter = i
		}
	}

	var lines []string
	emitRuntime := func() {
		if !stageCUDA || cudaProfile == nil {
			return
		}
		// A separate invocation from the wheels above, with no --index-url,
		// so it resolves from PyPI. Folding the two together would make the
		// vendor index primary and PyPI an extra index, and pip would then be
		// free to satisfy either package from either source — resolving torch
		// from PyPI, which is the wrong-architecture wheel this whole feature
		// exists to avoid.
		lines = append(lines, pipInstallLines(&spec.PipInstall{Packages: cudaProfile.Runtime}, nil, platform)...)
	}

	for i := range groups {
		lines = append(lines, pipInstallLines(&groups[i], cudaProfile, platform)...)
		if i == runtimeAfter {
			emitRuntime()
		}
	}
	if runtimeAfter == -1 {
		// A GPU stage that installs no GPU wheels of its own still gets the
		// runtime — it may be loading CUDA through something apt or cmake
		// installed.
		emitRuntime()
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

// buildLines compiles a stage's build: step. from, platform and scope are not
// used by every language — they are the inputs that scope a compiler's cache
// mounts, and only a language whose object files are cached needs them.
// swiftScratchPath and swiftCachePath are where a Swift stage's build tree and
// SwiftPM's shared cache live while the stage builds. Both are cache mounts,
// and neither is the package's own .build — deliberately.
//
// Mounting over .build would be the obvious thing and is wrong: a cache mount
// is not part of any layer, so the product binary would vanish the moment the
// RUN finished, and every Swift entrypoint naming .build/release/<product>
// (which is where SwiftPM puts it, and what a hand-written Swift Dockerfile
// therefore copies from) would point at nothing. Building into a scratch path
// off to the side leaves .build an ordinary directory that the binary can be
// installed into, so the cache is invisible to the rest of the Stagefile.
const (
	swiftScratchPath = "/var/cache/stagefile/swift/scratch"
	swiftCachePath   = "/var/cache/stagefile/swift/pm"
)

// swiftScratchID scopes a Swift build tree to everything that decides which
// object files belong in it: the toolchain image, the target platform, the
// compile profile, and the project itself.
//
// The project is in the scope even though sharing a tree across projects would
// not produce a wrong binary — SwiftPM rebuilds what it does not recognise.
// It would produce the one thing this mount exists to prevent: two projects
// alternately invalidating each other's tree, and, because the mount stays
// sharing=locked, queueing to do it. scope is empty when the caller did not
// name a project, which falls back to sharing per (toolchain, platform,
// profile) — no worse than having no id at all.
func swiftScratchID(scope, from, platform, profile string) string {
	return scopedCacheID("swift-scratch", scope, from, platform, profile)
}

// swiftCacheID scopes SwiftPM's shared cache to the toolchain and platform,
// and deliberately not to the project. It holds bare dependency clones keyed
// by repository URL and compiled manifests keyed by their own contents, so two
// projects depending on swift-nio genuinely share one clone instead of each
// fetching it — the same reason the pip cache is scoped by index rather than
// by project.
func swiftCacheID(from, platform string) string {
	return scopedCacheID("swiftpm", from, platform)
}

// swiftBuildLines compiles a Swift build stage.
//
// Two things are cached, because two different things are slow and they are
// invalidated by different events. The scratch tree holds every object file in
// the build including all of its dependencies', and is the entire reason a
// second build is incremental: without it a one-line edit to app code
// recompiles SwiftNIO from source. The shared cache holds SwiftPM's dependency
// clones and compiled manifests, so a rebuild also stops re-cloning every
// package in the graph over the network before it can start compiling.
//
// The binary is then installed from the scratch tree into .build/<profile>/,
// which is what SwiftPM would have written had it built in place. That keeps
// the cache an implementation detail: an entrypoint written against the
// conventional path keeps working, and nothing in the Stagefile has to know
// the build tree moved.
func swiftBuildLines(b *spec.Build, profile, from, platform, scope string) []string {
	// Always spell the configuration out. Bare `swift build` already means
	// debug, so an implicit debug build is indistinguishable from someone
	// having forgotten the flag — both in the generated Dockerfile and to the
	// optimizer check that scans for it.
	flags := "--scratch-path " + swiftScratchPath +
		" --cache-path " + swiftCachePath +
		" -c " + profile
	build := "swift build " + flags
	if b.Product != "" {
		build += " --product " + shellQuote(b.Product)
	}

	dest := ".build/" + profile
	commands := []string{
		build,
		// The bin path is asked of SwiftPM rather than assembled from profile:
		// the real directory carries the target triple, so composing it here
		// would mean hardcoding a per-architecture guess that breaks quietly
		// the first time it is wrong. --product is omitted because the path is
		// the same for every product in the package.
		`BIN="$(swift build ` + flags + ` --show-bin-path)"`,
		"mkdir -p " + dest,
		// Only the top level, and only files and resource bundles. The
		// per-target *.build/ directories sitting beside them hold the object
		// files, which belong in the cache mount and would otherwise be copied
		// into the image — several hundred megabytes of build intermediates in
		// a layer, for nothing.
		`find "$BIN" -mindepth 1 -maxdepth 1 \( -type f -o -name '*.bundle' \) ` +
			`-exec cp -a '{}' ` + dest + `/ ';'`,
	}
	mounts := []cacheMount{
		{id: swiftScratchID(scope, from, platform, profile), target: swiftScratchPath},
		{id: swiftCacheID(from, platform), target: swiftCachePath},
	}
	return []string{cacheRunMounts(mounts, strings.Join(commands, " \\\n    && "))}
}

func buildLines(b *spec.Build, from, platform, scope string) ([]string, error) {
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
		return swiftBuildLines(b, profile, from, platform, scope), nil
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
