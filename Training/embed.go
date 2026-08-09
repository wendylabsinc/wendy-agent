// Package trainingassets embeds the training templates and the wendytrain
// library so `wendy fleet train` can stage a build context from a released
// binary, with no checkout of this repository present.
//
// The embedded copy is the release-pinned one: a named template
// (`--template es-fleet`) always stages these files, so the library a device
// runs matches the Command Line Interface that deployed it. A template given
// as a path stages from disk instead, which is the escape hatch for local
// iteration.
//
// __init__.py is named explicitly because the default rules drop entries
// beginning with an underscore, and that file re-exports the library's public
// interface: without it a staged context ships a package that cannot be
// imported. An explicitly named pattern overrides the exclusion, whereas the
// all: prefix would also sweep in every local __pycache__ (roughly 400 kilobytes
// of compiled bytecode that is gitignored, so the binary would differ between a
// developer's machine and a clean checkout).
package trainingassets

import "embed"

// Assets holds templates/ and the pip-installable wendytrain project.
//
//go:embed templates
//go:embed wendytrain/pyproject.toml
//go:embed wendytrain/wendytrain
//go:embed wendytrain/wendytrain/__init__.py
var Assets embed.FS
