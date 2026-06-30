//go:build darwin || linux || windows

package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func newAuthListOrgsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-orgs",
		Short: "List and select your Wendy Cloud organizations",
		Long: `Show all organizations your account belongs to and optionally set a default.

Press 'd' on a highlighted organization to mark it as the default for commands
that target a specific org (such as 'wendy os install --pre-enroll' and
'wendy device enroll'). Press 'x' to clear the default. Press 'r' to remove
the stored credentials for the highlighted org. Enter selects an org for this
invocation only and prints its details.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			auth, err := config.ResolveAuth(cfg, "", pickAuthSessionFn)
			if err != nil {
				return err
			}

			orgs, err := listOrgsFromCloud(cmd.Context(), auth)
			if err != nil {
				return fmt.Errorf("fetching organizations: %w", err)
			}
			if len(orgs) == 0 {
				fmt.Println("Your account belongs to no organizations.")
				return nil
			}

			// Always force-show the picker: this command exists specifically
			// to let the user inspect and change their org selection.
			id, name, err := pickOrgInteractiveFn(orgs, cfg.DefaultOrgID)
			if err != nil {
				return err
			}

			fmt.Printf("Selected organization: %s (ID: %d)\n", name, id)
			return nil
		},
	}
	return cmd
}
