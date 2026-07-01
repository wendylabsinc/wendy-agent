package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

// TestDueCLIUpdateCheckSkipsDevBuilds asserts that development builds — both the
// literal "dev" default and CI branch builds carrying a "-dev" suffix — never
// trigger the periodic CLI update check (WDY-1770).
func TestDueCLIUpdateCheckSkipsDevBuilds(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })

	for _, ver := range []string{"dev", "2026.06.30-133859-dev"} {
		t.Run(ver, func(t *testing.T) {
			version.Version = ver
			// Empty LastCLIUpdateCheck would otherwise mark the check as due.
			if dueCLIUpdateCheck(&config.Config{}) {
				t.Errorf("dueCLIUpdateCheck for dev build %q = true, want false", ver)
			}
		})
	}
}
