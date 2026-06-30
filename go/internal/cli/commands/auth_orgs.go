//go:build darwin || linux || windows

package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"

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
			if len(cfg.Auth) == 0 {
				return config.ErrNotLoggedIn
			}

			// Aggregate orgs from every stored auth session and deduplicate by
			// org ID. Any valid session suffices to list orgs; the user should
			// not be prompted to choose one just to view this list.
			seen := make(map[int32]bool)
			var orgs []*cloudpb.Organization
			for i := range cfg.Auth {
				a := &cfg.Auth[i]
				if len(a.Certificates) == 0 {
					continue
				}
				fetched, fetchErr := listOrgsFromCloud(cmd.Context(), a)
				if fetchErr != nil {
					continue // skip failing sessions; others may succeed
				}
				for _, org := range fetched {
					if !seen[org.GetId()] {
						seen[org.GetId()] = true
						orgs = append(orgs, org)
					}
				}
			}
			if len(orgs) == 0 {
				fmt.Println("Your account belongs to no organizations.")
				return nil
			}

			// Always show the picker: this command exists specifically to let
			// the user inspect and change their org selection.
			id, name, err := pickOrgInteractiveFn(orgs, cfg)
			if err != nil {
				return err
			}

			fmt.Printf("Selected organization: %s (ID: %d)\n", name, id)
			return nil
		},
	}
	return cmd
}
