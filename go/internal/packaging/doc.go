// Package packaging holds tests that validate the OS packaging install
// scriptlets without performing a full package build. The tests drive the real
// shipped scripts — the deb/rpm post-install (wendy-agent-postinstall.sh) and
// the Arch .install (wendy-agent.install) — so the migration logic cannot drift
// from what is delivered to devices. The agent.sh installer shares the same
// migration block but is an interactive $SUDO installer and is not unit-driven
// here; the in-agent migration (services.MigrateLegacyConfigDir) is tested
// separately under internal/agent/services.
package packaging
