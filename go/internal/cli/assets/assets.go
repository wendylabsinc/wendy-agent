// Package assets embeds Wendy documentation and AI skill files for offline use.
package assets

import "embed"

// FS contains documentation and skill files embedded at compile time.
// Only plain-Markdown reference documentation and skills are embedded here;
// the docs-site source (Next.js app, MDX guides, components, images) lives in the
// docs tree but is intentionally excluded to keep the CLI binary small.
//
//go:embed docs/Examples docs/apps docs/architecture docs/clients docs/cloud docs/debugging docs/development docs/pki docs/vscode docs/wendy-lite docs/wendy-os-publisher docs/wendyos docs/RELEASES.md docs/entitlements.md docs/roadmap.md skills
var FS embed.FS
