package spec

import (
	"fmt"
	"strings"
	"unicode"
)

// Validate checks structural rules that don't require any external state
// (no registry lookups, no filesystem access). See install/copy/entrypoint
// validation added in later tasks.
func (f *File) Validate() error {
	if f.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", f.Version)
	}
	if len(f.Stages) == 0 {
		return fmt.Errorf("stages: must declare at least one stage")
	}

	priorNames := map[string]bool{}
	lastIdx := len(f.Stages) - 1
	for i, s := range f.Stages {
		if s.Name == "" {
			return fmt.Errorf("stages[%d]: name is required", i)
		}
		if s.Name == "local" {
			return fmt.Errorf("stages[%d]: %q is a reserved name (means the local source tree in copy.from)", i, s.Name)
		}
		if priorNames[s.Name] {
			return fmt.Errorf("stages[%d]: duplicate stage name %q", i, s.Name)
		}
		if s.From == "" {
			return fmt.Errorf("stage %q: from is required", s.Name)
		}
		if err := validateNoInjection(&s); err != nil {
			return fmt.Errorf("stage %q: %w", s.Name, err)
		}
		if err := validateInstall(s.Install); err != nil {
			return fmt.Errorf("stage %q: %w", s.Name, err)
		}
		if err := validateCopy(s.Copy, priorNames); err != nil {
			return fmt.Errorf("stage %q: %w", s.Name, err)
		}
		if err := validateBuild(s.Build); err != nil {
			return fmt.Errorf("stage %q: %w", s.Name, err)
		}

		priorNames[s.Name] = true

		isFinal := i == lastIdx
		if !isFinal && s.Entrypoint != nil {
			return fmt.Errorf("stage %q: entrypoint is only allowed on the final stage (%q)", s.Name, f.Stages[lastIdx].Name)
		}
		if !isFinal && s.User != "" {
			return fmt.Errorf("stage %q: user is only allowed on the final stage (%q)", s.Name, f.Stages[lastIdx].Name)
		}
	}
	return nil
}

func validateInstall(inst *Install) error {
	if inst == nil {
		return nil
	}
	if inst.Apt == nil && inst.Apk == nil && inst.Pip == nil && inst.Npm == nil {
		return fmt.Errorf("install: at least one of apt, apk, pip, npm must be set")
	}
	if inst.Apt != nil && len(inst.Apt.Packages) == 0 {
		return fmt.Errorf("install.apt: packages must be non-empty")
	}
	if inst.Apk != nil && len(inst.Apk.Packages) == 0 {
		return fmt.Errorf("install.apk: packages must be non-empty")
	}
	if inst.Pip != nil && inst.Pip.Requirements == "" && len(inst.Pip.Packages) == 0 {
		return fmt.Errorf("install.pip: requirements or packages must be set")
	}
	if inst.Npm != nil && inst.Npm.Manager != "" {
		switch inst.Npm.Manager {
		case "npm", "yarn", "pnpm":
		default:
			return fmt.Errorf("install.npm.manager %q is not one of npm, yarn, pnpm", inst.Npm.Manager)
		}
	}
	return nil
}

func validateCopy(entries []CopyEntry, priorNames map[string]bool) error {
	for _, e := range entries {
		if len(e.Paths) == 0 {
			return fmt.Errorf("copy from %q: paths must be non-empty", e.From)
		}
		for _, p := range e.Paths {
			if p == "/" {
				return fmt.Errorf("copy from %q: copying \"/\" is not allowed; list the specific paths you need", e.From)
			}
		}
		if len(e.Paths) > 1 && e.Dest == "" {
			return fmt.Errorf("copy from %q: dest is required when copying more than one path", e.From)
		}
		if e.From != "local" && !priorNames[e.From] {
			return fmt.Errorf("copy from %q: not a prior stage name (and not \"local\")", e.From)
		}
	}
	return nil
}

func validateBuild(b *Build) error {
	if b == nil {
		return nil
	}
	switch b.Lang {
	case "rust", "go", "swift":
	default:
		return fmt.Errorf("build.lang %q is not one of rust, go, swift", b.Lang)
	}
	if b.Profile != "" && b.Profile != "release" && b.Profile != "debug" {
		return fmt.Errorf("build.profile %q is not one of release, debug", b.Profile)
	}
	return nil
}

// rejectNewline returns an error if value contains a newline or carriage
// return. This applies everywhere, regardless of whether the field is also
// shell-quoted at codegen time (see internal/codegen's shellQuote), because
// a raw newline breaks the Dockerfile's own line-based grammar before any
// shell or builder instruction ever sees the value.
func rejectNewline(value, fieldDesc string) error {
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("%s must not contain a newline (got %q)", fieldDesc, value)
	}
	return nil
}

// rejectLeadingDash returns an error if value starts with "-", which would
// let it be parsed as a flag by the receiving package manager (pip,
// apt-get, apk) once it arrives as its own argv entry. Shell-quoting a
// value (see internal/codegen's shellQuote) stops the *shell* from
// treating it specially, but does nothing to stop the *program* pip/apt/apk
// is invoking from treating a leading "-" as an option rather than a name.
func rejectLeadingDash(value, fieldDesc string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s must not start with \"-\" (got %q); it would be parsed as a flag, not a package name", fieldDesc, value)
	}
	return nil
}

// rejectWhitespace returns an error if value contains any whitespace
// character (space, tab, or any other rune unicode.IsSpace treats as
// whitespace — including \v and \f, both of which BuildKit's Dockerfile
// tokenizer also splits COPY arguments on). This checks the property
// ("is this rune a separator?") rather than an enumerated list of
// characters, because an enumerated list only ever covers the bytes
// someone already thought to test.
func rejectWhitespace(value, fieldDesc string) error {
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return fmt.Errorf("%s must not contain whitespace (got %q)", fieldDesc, value)
	}
	return nil
}

func validateNoInjection(s *Stage) error {
	if err := rejectNewline(s.Name, "name"); err != nil {
		return err
	}
	if err := rejectNewline(s.From, "from"); err != nil {
		return err
	}
	if err := rejectNewline(s.User, "user"); err != nil {
		return err
	}
	if s.Install != nil {
		if s.Install.Apt != nil {
			for _, p := range s.Install.Apt.Packages {
				if err := rejectNewline(p, "install.apt.packages entry"); err != nil {
					return err
				}
				if err := rejectLeadingDash(p, "install.apt.packages entry"); err != nil {
					return err
				}
			}
		}
		if s.Install.Apk != nil {
			for _, p := range s.Install.Apk.Packages {
				if err := rejectNewline(p, "install.apk.packages entry"); err != nil {
					return err
				}
				if err := rejectLeadingDash(p, "install.apk.packages entry"); err != nil {
					return err
				}
			}
		}
		if s.Install.Pip != nil {
			if err := rejectNewline(s.Install.Pip.Requirements, "install.pip.requirements"); err != nil {
				return err
			}
			if err := rejectWhitespace(s.Install.Pip.Requirements, "install.pip.requirements"); err != nil {
				return err
			}
			if err := rejectLeadingDash(s.Install.Pip.Requirements, "install.pip.requirements"); err != nil {
				return err
			}
			for _, p := range s.Install.Pip.Packages {
				if err := rejectNewline(p, "install.pip.packages entry"); err != nil {
					return err
				}
				if err := rejectLeadingDash(p, "install.pip.packages entry"); err != nil {
					return err
				}
			}
		}
	}
	for _, c := range s.Copy {
		if err := rejectNewline(c.From, "copy.from"); err != nil {
			return err
		}
		if err := rejectWhitespace(c.From, "copy.from"); err != nil {
			return err
		}
		if err := rejectLeadingDash(c.From, "copy.from"); err != nil {
			return err
		}
		if err := rejectNewline(c.Dest, "copy.dest"); err != nil {
			return err
		}
		if err := rejectWhitespace(c.Dest, "copy.dest"); err != nil {
			return err
		}
		if err := rejectLeadingDash(c.Dest, "copy.dest"); err != nil {
			return err
		}
		for _, p := range c.Paths {
			if err := rejectNewline(p, "copy.paths entry"); err != nil {
				return err
			}
			if err := rejectWhitespace(p, "copy.paths entry"); err != nil {
				return err
			}
			if err := rejectLeadingDash(p, "copy.paths entry"); err != nil {
				return err
			}
		}
	}
	if s.Entrypoint != nil {
		for _, arg := range s.Entrypoint.Exec {
			if err := rejectNewline(arg, "entrypoint.exec entry"); err != nil {
				return err
			}
		}
	}
	return nil
}
