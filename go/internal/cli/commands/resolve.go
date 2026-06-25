package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/resolution"
)

func newResolveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "resolve <target>",
		Short:  "Resolve a device target and print the candidate address set",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResolve(cmd.Context(), args[0])
		},
	}
	return cmd
}

func runResolve(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	candidates, sourceResults, err := resolution.Resolve(ctx, target)

	fmt.Fprintf(os.Stderr, "Target: %s\n\n", target)
	fmt.Fprintf(os.Stderr, "Strategy results:\n")
	for _, src := range []resolution.Source{resolution.SourceLiteralIP, resolution.SourceMDNS, resolution.SourceDNS, resolution.SourceCache} {
		if detail, ok := sourceResults[src]; ok {
			fmt.Fprintf(os.Stderr, "  %-14s %s\n", string(src)+":", detail)
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		return err
	}

	fmt.Fprintf(os.Stderr, "\nCandidates (%d):\n", len(candidates))
	for i, c := range candidates {
		zone := ""
		if c.Zone != "" {
			zone = " zone=" + c.Zone
		}
		iface := ""
		if c.Interface != "" {
			iface = " iface=" + c.Interface
		}
		fmt.Fprintf(os.Stderr, "  [%d] %s  source=%s%s%s\n", i+1, c.Addr(), c.Source, zone, iface)
	}
	return nil
}
