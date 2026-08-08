// Package codegen compiles a validated Stagefile spec into Dockerfile text.
// Generate is a pure function: given a spec and a map of already-resolved
// image digests, it returns the exact Dockerfile bytes, or an error if a
// referenced image has no resolved digest. Every helper in this package
// takes typed fields and returns fixed strings — none of them accept or
// interpolate a user-supplied shell string.
package codegen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// defaultUser is the distroless-style non-root numeric UID used when a
// final stage doesn't declare an explicit user. A numeric UID needs no
// /etc/passwd entry, so it works on any base image.
const defaultUser = "65532"

// Generate compiles f into Dockerfile text. images maps every from: value
// in f to its resolved "sha256:..." digest (see internal/lock). platform,
// if non-empty (e.g. "linux/arm64"), is applied to every FROM via
// --platform; pass "" to omit it and let the builder decide.
func Generate(f *spec.File, images map[string]string, platform string) (string, error) {
	var blocks []string
	lastIdx := len(f.Stages) - 1

	for i, s := range f.Stages {
		digest, ok := images[s.From]
		if !ok {
			return "", fmt.Errorf("stage %q: no resolved digest for %q; run `stagefile lock`", s.Name, s.From)
		}

		lines := []string{fromLine(s.From, digest, s.Name, platform)}

		if s.Install != nil {
			if s.Install.Apt != nil {
				lines = append(lines, aptInstallLines(s.Install.Apt)...)
			}
			if s.Install.Apk != nil {
				lines = append(lines, apkInstallLines(s.Install.Apk)...)
			}
			if s.Install.Pip != nil {
				lines = append(lines, pipInstallLines(s.Install.Pip)...)
			}
			if s.Install.Npm != nil {
				lines = append(lines, npmInstallLines(s.Install.Npm)...)
			}
		}

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
			if s.Entrypoint != nil {
				lines = append(lines, entrypointLine(s.Entrypoint))
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
	return fmt.Sprintf("FROM %s%s@%s AS %s", plat, image, digest, name)
}

func entrypointLine(e *spec.Entrypoint) string {
	quoted := make([]string, len(e.Exec))
	for i, s := range e.Exec {
		quoted[i] = strconv.Quote(s)
	}
	return "ENTRYPOINT [" + strings.Join(quoted, ", ") + "]"
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

func aptInstallLines(a *spec.AptInstall) []string {
	parts := []string{"apt-get", "update", "&&", "apt-get", "install", "-y"}
	if !a.Recommends {
		parts = append(parts, "--no-install-recommends")
	}
	for _, p := range a.Packages {
		parts = append(parts, shellQuote(p))
	}
	return []string{
		"RUN " + strings.Join(parts, " ") + " \\",
		"    && rm -rf /var/lib/apt/lists/*",
	}
}

func apkInstallLines(a *spec.AptInstall) []string {
	parts := []string{"apk", "add"}
	if !a.Recommends {
		parts = append(parts, "--no-cache")
	}
	for _, p := range a.Packages {
		parts = append(parts, shellQuote(p))
	}
	return []string{"RUN " + strings.Join(parts, " ")}
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
	if p.Requirements != "" {
		parts = append(parts, "-r", shellQuote(p.Requirements))
	}
	for _, pkg := range p.Packages {
		parts = append(parts, shellQuote(pkg))
	}
	lines = append(lines, "RUN --mount=type=cache,target=/root/.cache/pip "+strings.Join(parts, " "))
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
	case "pnpm":
		cmd, cacheDir = "pnpm install --frozen-lockfile", "/root/.local/share/pnpm/store"
	default:
		cmd, cacheDir = "npm ci", "/root/.npm"
	}
	return []string{
		fmt.Sprintf("COPY package.json %s ./", spec.NpmLockfile(n.Manager)),
		fmt.Sprintf("RUN --mount=type=cache,target=%s %s", cacheDir, cmd),
	}
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
		from := ""
		if e.From != "local" {
			from = "--from=" + e.From + " "
		}
		lines = append(lines, fmt.Sprintf("COPY %s%s %s", from, strings.Join(e.Paths, " "), dest))
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
		return []string{"RUN --mount=type=cache,target=/root/.cargo " + cmd}, nil
	case "go":
		return []string{"RUN --mount=type=cache,target=/root/.cache/go-build go build ./..."}, nil
	case "swift":
		cmd := "swift build"
		if profile == "release" {
			cmd += " -c release"
		}
		return []string{"RUN --mount=type=cache,target=/root/.swiftpm " + cmd}, nil
	default:
		return nil, fmt.Errorf("unsupported build.lang %q (supported: rust, go, swift)", b.Lang)
	}
}
