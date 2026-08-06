package spec

// File is a parsed, not-yet-validated Stagefile source.
type File struct {
	Version int     `yaml:"version"`
	Stages  []Stage `yaml:"stages"`
}

// Stage is one named build stage. Every stage has exactly one base image
// (From) plus whichever optional operations it performs.
type Stage struct {
	Name       string      `yaml:"name"`
	From       string      `yaml:"from"`
	Install    *Install    `yaml:"install,omitempty"`
	Build      *Build      `yaml:"build,omitempty"`
	Copy       []CopyEntry `yaml:"copy,omitempty"`
	Entrypoint *Entrypoint `yaml:"entrypoint,omitempty"`
	User       string      `yaml:"user,omitempty"`
}

// Install is the set of declarative, per-ecosystem dependency installs for
// one stage. Any subset of the fields may be set; each compiles to exactly
// one RUN with the compiler's own correct flags baked in — there is no field
// that accepts a raw install command.
type Install struct {
	Apt *AptInstall `yaml:"apt,omitempty"`
	Apk *AptInstall `yaml:"apk,omitempty"`
	Pip *PipInstall `yaml:"pip,omitempty"`
	Npm *NpmInstall `yaml:"npm,omitempty"`
}

// AptInstall covers both apt (Debian/Ubuntu) and apk (Alpine); the
// generated command differs, but the declared shape is identical.
type AptInstall struct {
	Packages   []string `yaml:"packages"`
	Recommends bool     `yaml:"recommends,omitempty"`
}

// PipInstall installs Python dependencies from a requirements file, an
// explicit package list, or both.
type PipInstall struct {
	Requirements string   `yaml:"requirements,omitempty"`
	Packages     []string `yaml:"packages,omitempty"`
}

// NpmInstall always compiles to the ecosystem's frozen-lockfile install
// form. Manager selects npm (default), yarn, or pnpm.
type NpmInstall struct {
	Manager string `yaml:"manager,omitempty"`
}

// NpmLockfile returns the lockfile filename a manager value expects
// alongside package.json (empty string defaults to "npm"). Both codegen
// (to COPY and RUN against the right file) and dockerignore (to allowlist
// it) call this so the two packages can't drift out of sync on which file
// each manager uses.
func NpmLockfile(manager string) string {
	switch manager {
	case "yarn":
		return "yarn.lock"
	case "pnpm":
		return "pnpm-lock.yaml"
	default:
		return "package-lock.json"
	}
}

// Build is a per-language compile step. Profile defaults to "release";
// "debug" is an explicit, visible deviation.
type Build struct {
	Lang    string `yaml:"lang"`
	Profile string `yaml:"profile,omitempty"`
}

// CopyEntry promotes files from a prior stage (or the reserved name
// "local", meaning the source tree next to the Stagefile) into this stage.
// Paths must be non-empty and none may be the literal path "/". Dest is
// required when Paths has more than one entry; otherwise it defaults to
// the single path itself.
type CopyEntry struct {
	From  string   `yaml:"from"`
	Paths []string `yaml:"paths"`
	Dest  string   `yaml:"dest,omitempty"`
}

// Entrypoint is always an argv list — there is no shell-string form.
type Entrypoint struct {
	Exec []string `yaml:"exec"`
}
