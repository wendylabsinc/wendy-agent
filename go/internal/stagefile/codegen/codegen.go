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
	priorStages := make(map[string]bool, len(f.Stages))

	for i, s := range f.Stages {
		fromPriorStage := priorStages[s.From]
		pinned := !fromPriorStage && (s.Pin == nil || *s.Pin)
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

		// Treat pip as a linked filesystem overlay rather than a linear child of
		// the app's OS-package layer. Both branches now depend directly on the
		// pinned base, so editing APT cannot invalidate or re-layer pip and
		// editing pip cannot rebuild APT. pip installs under a compiler-owned
		// root; COPY --link later promotes only the resulting /usr/local tree.
		pipDependencyStage := ""
		if s.Install != nil && len(s.Install.Pip) > 0 {
			pipDependencyStage = generatedStageName("stagefile-pip-deps", i, f.Stages)
		}

		// CUDA runtime wheels are by far the largest dependency layer in a GPU
		// image. Build them from the pinned base in an independent stage so an
		// unrelated edit to the app stage's APT/CMake dependencies cannot evict
		// several gigabytes of otherwise-identical CUDA content. The final
		// COPY --link below rebases this content onto the app stage without
		// coupling its cache key to the app stage's parent filesystem.
		cudaRuntimeStage := ""
		if s.CUDA {
			cudaRuntimeStage = cudaRuntimeStageName(i, f.Stages)
			blocks = append(blocks, strings.Join(cudaRuntimeStageLines(
				s, digest, pinned, stagePlatform, cudaRuntimeStage, *cudaProfile,
			), "\n"))
		}
		if pipDependencyStage != "" {
			blocks = append(blocks, strings.Join(pipDependencyStageLines(
				s, digest, pinned, stagePlatform, pipDependencyStage, cudaProfile,
			), "\n"))
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
		// because a GPU stage still needs its generated runtime even when it
		// declares no install of its own — the collection below imports it.
		// With no fields set, nothing else here emits a line.
		install := s.Install
		if install == nil {
			install = &spec.Install{}
		}
		if install.Apt != nil {
			// Include the resolved base-image digest in the APT cache scope. Two
			// Stagefiles using the same pinned base can safely share indexes and
			// .debs, while a moved tag (or a different distro) gets a fresh cache.
			// An unpinned stage has no immutable base identity, so leave aptBase
			// empty to disable persistent APT caches for that stage.
			aptBase := ""
			if pinned {
				aptBase = s.From + "@" + digest
			}
			lines = append(lines, aptInstallLines(install.Apt, aptBase, stagePlatform)...)
		}
		if install.Apk != nil {
			lines = append(lines, apkInstallLines(install.Apk)...)
		}
		if len(install.CMake) > 0 {
			lines = append(lines, cmakeInstallLines(install.CMake, stagePlatform)...)
		}
		if cudaRuntimeStage != "" {
			lines = append(lines, fmt.Sprintf("COPY --link --from=%s %s %s",
				cudaRuntimeStage, cudaPythonRoot, cudaPythonRoot))
		}
		if pipDependencyStage != "" {
			lines = append(lines, fmt.Sprintf("COPY --link --from=%s %s/ /",
				pipDependencyStage, pipOverlayRoot))
		}
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
		priorStages[s.Name] = true
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

// pipCacheID scopes pip's wheel cache to the index set it can download from,
// the platform whose wheels it stores, and the build-only OS packages that can
// affect locally compiled wheel contents.
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
	parts = append(parts, p.BuildPackages...)
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
func aptRepositoryLines(repos []spec.AptRepository, base, platform string) []string {
	if len(repos) == 0 {
		return nil
	}
	// The bootstrap can only use the base image's repositories; the declared
	// repositories are added after ca-certificates and their pinned keys exist.
	lines := []string{aptRun(base, platform, nil,
		"apt-get update && apt-get install -y --no-install-recommends ca-certificates")}
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

// aptCacheScope returns a stable description of every input that controls the
// contents of APT's indexes and package archive. Package names are deliberately
// excluded: compatible Stagefiles should share downloads even when they install
// different subsets. Repository order is preserved because it can affect APT
// priority when otherwise-equivalent sources are declared more than once.
func aptCacheScope(base, platform string, repos []spec.AptRepository) []string {
	parts := []string{base, platform}
	for _, r := range repos {
		parts = append(parts, r.Name, r.URL, strings.Join(r.Suites, "\x1f"), strings.Join(r.Components, "\x1f"), r.Key.URL, r.Key.SHA256, r.Key.Format)
	}
	return parts
}

// aptRun gives APT two persistent BuildKit caches: package indexes (so update
// can use conditional requests instead of downloading every index afresh) and
// downloaded .debs. Explicit IDs make those caches reusable across separately
// compiled Stagefiles. sharing=locked is required because APT itself takes
// exclusive locks in both directories.
func aptRun(base, platform string, repos []spec.AptRepository, command string) string {
	// pin: false deliberately leaves the base image unresolved. Without an
	// immutable base identity, a persistent cache could carry indexes or .debs
	// across incompatible images after a tag moves. Keep the uncached layer
	// small by removing its package indexes after each APT invocation.
	if base == "" {
		return "RUN " + command + " \\\n" +
			"    && rm -rf /var/lib/apt/lists/*"
	}
	scope := aptCacheScope(base, platform, repos)
	mounts := []cacheMount{
		{id: scopedCacheID("apt-lists", scope...), target: "/var/lib/apt/lists"},
		{id: scopedCacheID("apt-archives", scope...), target: "/var/cache/apt"},
	}
	// Debian/Ubuntu container images normally install docker-clean, whose APT
	// hooks delete every downloaded .deb after the command. Removing that hook
	// is what lets the archive mount actually retain packages for sibling builds;
	// the mount itself remains outside the resulting image layer.
	return cacheRunMounts(mounts, "rm -f /etc/apt/apt.conf.d/docker-clean && "+command)
}

func aptInstallLines(a *spec.AptInstall, base, platform string) []string {
	lines := aptRepositoryLines(a.Repositories, base, platform)
	parts := []string{"apt-get", "update", "&&", "apt-get", "install", "-y"}
	if !a.Recommends {
		parts = append(parts, "--no-install-recommends")
	}
	for _, p := range a.Packages {
		parts = append(parts, shellQuote(p))
	}
	return append(lines, aptRun(base, platform, a.Repositories, strings.Join(parts, " ")))
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
// source resolve GPU wheels for whichever board it is being built for. root,
// when non-empty, turns the install into a filesystem overlay whose contents
// can be promoted onto a sibling stage with COPY --link.
func pipInstallLines(p *spec.PipInstall, cudaProfile *gpu.Profile, platform, root string) []string {
	var lines []string
	if p.Requirements != "" {
		lines = append(lines, fmt.Sprintf("COPY %s %s", p.Requirements, p.Requirements))
	}
	// No --no-cache-dir here: the cache mount below already keeps pip's
	// cache out of the image layer, and disabling the cache would force a
	// full wheel re-download every time this layer rebuilds.
	parts := []string{"pip", "install"}
	if root != "" {
		// Let pip select the base image's default installation scheme underneath
		// the overlay root. Passing --prefix /usr/local is not portable: Debian's
		// patched sysconfig scheme expands it to /usr/local/local, leaving copied
		// packages outside Python's import path in Debian and ROS images.
		parts = append(parts, "--root", shellQuote(root))
	}
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

// cudaPythonRoot is a dedicated, compiler-owned Python import root for the
// generated CUDA runtime stage. Keeping these packages out of the base
// interpreter's site-packages makes the directory safe to promote wholesale
// with COPY --link, independently of the app stage's APT/CMake history.
const cudaPythonRoot = "/opt/stagefile/cuda/python"

// pipOverlayRoot is the staging root for user-declared pip dependencies. pip
// recreates the absolute /usr/local hierarchy underneath it; copying the
// root's contents to / therefore places packages and scripts at their normal
// runtime paths without copying pip's build-only OS packages.
const pipOverlayRoot = "/opt/stagefile/pip/root"

// generatedStageName returns a deterministic compiler-owned stage name that
// cannot collide with a user-declared stage.
func generatedStageName(prefix string, stageIndex int, stages []spec.Stage) string {
	used := make(map[string]bool, len(stages))
	for _, s := range stages {
		used[s.Name] = true
	}
	base := fmt.Sprintf("%s-%d", prefix, stageIndex)
	name := base
	for suffix := 2; used[name]; suffix++ {
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
	return name
}

// cudaRuntimeStageName returns a generated stage name that cannot collide with
// a user-declared stage. Stage names are references rather than image-visible
// state, so the stable stage index is sufficient to keep generated output
// deterministic.
func cudaRuntimeStageName(stageIndex int, stages []spec.Stage) string {
	return generatedStageName("stagefile-cuda-runtime", stageIndex, stages)
}

// pipDependencyStageLines builds all user pip groups as a sibling of the app
// stage. BuildPackages are deliberately installed only here: headers and
// compilers can build wheels, but cannot leak into the runtime root copied to
// the app stage.
func pipDependencyStageLines(s spec.Stage, digest string, pinned bool, platform, name string, cudaProfile *gpu.Profile) []string {
	lines := []string{fromLine(s.From, digest, name, platform)}
	lines = append(lines, kvLines("ARG", s.Args)...)
	lines = append(lines, kvLines("ENV", s.Env)...)
	if s.Workdir != "" {
		lines = append(lines, "WORKDIR "+s.Workdir)
	}

	aptBase := ""
	if pinned {
		aptBase = s.From + "@" + digest
	}
	lines = append(lines, pipBuildPackageLines(s.Install, aptBase, platform)...)
	lines = append(lines, pipGroupLines(s.Install.Pip, cudaProfile, platform, pipOverlayRoot)...)
	return lines
}

// pipBuildPackageLines bootstraps pip when the base does not provide it and
// installs the de-duplicated build-only package set. A declared apk manager
// selects apk; apt is the default because Debian/Ubuntu bases are the common
// case and existing pip-only Stagefiles need no additional syntax.
func pipBuildPackageLines(inst *spec.Install, aptBase, platform string) []string {
	var packages []string
	seen := map[string]bool{}
	for _, group := range inst.Pip {
		for _, pkg := range group.BuildPackages {
			if !seen[pkg] {
				seen[pkg] = true
				packages = append(packages, pkg)
			}
		}
	}

	if inst.Apk != nil && inst.Apt == nil {
		var lines []string
		if len(inst.Apk.Repositories) > 0 && len(packages) > 0 {
			var quoted []string
			for _, repo := range inst.Apk.Repositories {
				quoted = append(quoted, shellQuote(repo))
			}
			lines = append(lines, fmt.Sprintf("RUN printf '%%s\\n' %s >> /etc/apk/repositories", strings.Join(quoted, " ")))
		}
		withPip := append([]string{"py3-pip"}, packages...)
		if len(packages) == 0 {
			return append(lines, "RUN command -v pip >/dev/null 2>&1 || apk add --no-cache 'py3-pip'")
		}
		command := fmt.Sprintf("if command -v pip >/dev/null 2>&1; then apk add --no-cache %s; else apk add --no-cache %s; fi",
			shellQuoteList(packages), shellQuoteList(withPip))
		return append(lines, "RUN "+command)
	}

	var repos []spec.AptRepository
	if inst.Apt != nil && len(packages) > 0 {
		repos = inst.Apt.Repositories
	}
	lines := aptRepositoryLines(repos, aptBase, platform)
	if len(packages) == 0 {
		bootstrap := "command -v pip >/dev/null 2>&1 || " +
			"(apt-get update && apt-get install -y --no-install-recommends 'python3-pip')"
		return append(lines, aptRun(aptBase, platform, repos, bootstrap))
	}
	withPip := append([]string{"python3-pip"}, packages...)
	command := fmt.Sprintf("if command -v pip >/dev/null 2>&1; then apt-get update && apt-get install -y --no-install-recommends %s; else apt-get update && apt-get install -y --no-install-recommends %s; fi",
		shellQuoteList(packages), shellQuoteList(withPip))
	return append(lines, aptRun(aptBase, platform, repos, command))
}

func shellQuoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, shellQuote(value))
	}
	return strings.Join(quoted, " ")
}

// cudaRuntimeStageLines builds the target profile's large, stable runtime in
// an independent stage. pip is bootstrapped only when the pinned base does not
// already provide it; the APT caches are scoped exactly like a normal
// install.apt block, but user-declared packages and repositories deliberately
// do not participate in this stage's cache key.
func cudaRuntimeStageLines(s spec.Stage, digest string, pinned bool, platform, name string, profile gpu.Profile) []string {
	lines := []string{fromLine(s.From, digest, name, platform)}
	aptBase := ""
	if pinned {
		aptBase = s.From + "@" + digest
	}
	bootstrap := "command -v pip >/dev/null 2>&1 || " +
		"(apt-get update && apt-get install -y --no-install-recommends 'python3-pip')"
	lines = append(lines, aptRun(aptBase, platform, nil, bootstrap))

	p := spec.PipInstall{Packages: profile.Runtime}
	parts := []string{"pip", "install", "--target", shellQuote(cudaPythonRoot)}
	for _, pkg := range p.Packages {
		parts = append(parts, shellQuote(pkg))
	}
	lines = append(lines, cacheRunID(
		pipCacheID(&p, "", platform),
		"/root/.cache/pip",
		strings.Join(parts, " "),
	))
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
	env := make(map[string]string, len(s.Env)+2)
	for k, v := range s.Env {
		env[k] = v
	}
	env[spec.LDLibraryPath] = cudaProfile.LibDir
	if existing := env["PYTHONPATH"]; existing != "" {
		env["PYTHONPATH"] = cudaPythonRoot + ":" + existing
	} else {
		env["PYTHONPATH"] = cudaPythonRoot
	}
	return env
}

// pipGroupLines emits user-declared pip groups into root. These groups live in
// an independent generated stage and are promoted onto the app stage as one
// linked filesystem overlay.
func pipGroupLines(groups []spec.PipInstall, cudaProfile *gpu.Profile, platform, root string) []string {
	var lines []string
	for i := range groups {
		lines = append(lines, pipInstallLines(&groups[i], cudaProfile, platform, root)...)
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
