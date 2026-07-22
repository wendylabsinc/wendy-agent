package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// groupNamePattern restricts a device-group name (an Asset tag used as a group):
// start with an alphanumeric, then alphanumerics, '.', '_', or '-'. This keeps
// group names safe to pass as CLI args, embed in tables, and round-trip through
// the cloud filter (which treats commas/spaces specially).
var groupNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// validateGroupName reports whether name is a well-formed device-group name.
func validateGroupName(name string) error {
	if name == "" {
		return fmt.Errorf("group name is required")
	}
	if !groupNamePattern.MatchString(name) {
		return fmt.Errorf("group name %q is invalid: start with a letter or digit, then letters, digits, '.', '_', or '-' (max 63 chars)", name)
	}
	return nil
}

// addTag returns tags with tag appended. changed is false when the tag was
// already present (the returned slice is then equivalent to the input).
func addTag(tags []string, tag string) (result []string, changed bool) {
	for _, t := range tags {
		if t == tag {
			return tags, false
		}
	}
	out := make([]string, 0, len(tags)+1)
	out = append(out, tags...)
	out = append(out, tag)
	return out, true
}

// removeTag returns tags with every occurrence of tag removed. changed is false
// when the tag was not present.
func removeTag(tags []string, tag string) (result []string, changed bool) {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t != tag {
			out = append(out, t)
		}
	}
	return out, len(out) != len(tags)
}

// groupCounts maps each tag (group) to the number of assets that carry it.
func groupCounts(assets []*cloudpb.Asset) map[string]int {
	counts := map[string]int{}
	for _, a := range assets {
		for _, t := range a.GetTags() {
			counts[t]++
		}
	}
	return counts
}

// assetsInGroup returns the assets carrying the given tag, preserving input order.
func assetsInGroup(assets []*cloudpb.Asset, group string) []*cloudpb.Asset {
	var out []*cloudpb.Asset
	for _, a := range assets {
		for _, t := range a.GetTags() {
			if t == group {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// assetsWithAnyTag returns the assets carrying any of the given tags (exact
// match — cloud tags are assigned labels, not globs), preserving input order.
func assetsWithAnyTag(assets []*cloudpb.Asset, tags []string) []*cloudpb.Asset {
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}
	var out []*cloudpb.Asset
	for _, a := range assets {
		for _, t := range a.GetTags() {
			if want[t] {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

func newFleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Manage groups of WendyOS devices",
		Long: "Operate across many devices at once.\n\n" +
			"A device group is a tag on the cloud Asset: a device is in group \"cameras\" when it\n" +
			"carries that tag. Group membership is the targeting primitive that fleet-wide\n" +
			"operations (deploying to a whole group, fleet inventory) build on.",
	}
	cmd.AddCommand(
		newFleetGroupCmd(),
		newFleetAppsCmd(),
		newFleetRunCmd(),
	)
	return cmd
}

func newFleetGroupCmd() *cobra.Command {
	var cloudGRPC string

	cmd := &cobra.Command{
		Use:   "group",
		Short: "Define and inspect device groups",
	}
	cmd.PersistentFlags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (optional when a default session is set via 'wendy auth use')")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "ls",
			Short: "List device groups and their member counts",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runFleetGroupLs(cmd.Context(), cloudGRPC)
			},
		},
		&cobra.Command{
			Use:   "show <group>",
			Short: "List the devices in a group",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFleetGroupShow(cmd.Context(), cloudGRPC, args[0])
			},
		},
		&cobra.Command{
			Use:   "add <group> <device>...",
			Short: "Add one or more devices to a group",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFleetGroupMembership(cmd.Context(), cloudGRPC, args[0], args[1:], true)
			},
		},
		&cobra.Command{
			Use:   "rm <group> <device>...",
			Short: "Remove one or more devices from a group",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFleetGroupMembership(cmd.Context(), cloudGRPC, args[0], args[1:], false)
			},
		},
	)
	return cmd
}

// fleetGroupSummary is the --json shape for `fleet group ls`.
type fleetGroupSummary struct {
	Group   string `json:"group"`
	Devices int    `json:"devices"`
}

func runFleetGroupLs(ctx context.Context, cloudGRPC string) error {
	auth, err := pickAuthEntry(cloudGRPC)
	if err != nil {
		return err
	}
	assets, err := fetchCloudAssetsFiltered(ctx, auth, false)
	if err != nil {
		return err
	}

	counts := groupCounts(assets)
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)

	if jsonOutput || !isInteractiveTerminal() {
		out := make([]fleetGroupSummary, 0, len(names))
		for _, name := range names {
			out = append(out, fleetGroupSummary{Group: name, Devices: counts[name]})
		}
		return printJSON(out)
	}

	if len(names) == 0 {
		fmt.Println("No device groups yet. Create one with 'wendy fleet group add <group> <device>...'.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "GROUP\tDEVICES")
	for _, name := range names {
		fmt.Fprintf(tw, "%s\t%d\n", name, counts[name])
	}
	return tw.Flush()
}

func runFleetGroupShow(ctx context.Context, cloudGRPC, group string) error {
	if err := validateGroupName(group); err != nil {
		return err
	}
	auth, err := pickAuthEntry(cloudGRPC)
	if err != nil {
		return err
	}
	assets, err := fetchCloudAssetsFiltered(ctx, auth, false)
	if err != nil {
		return err
	}
	members := assetsInGroup(assets, group)

	if jsonOutput || !isInteractiveTerminal() {
		infos := make([]discoverDeviceInfo, 0, len(members))
		for _, a := range members {
			infos = append(infos, cloudDeviceInfoFromAsset(a, nil))
		}
		return printJSON(infos)
	}

	if len(members) == 0 {
		fmt.Printf("Group %q has no devices (or does not exist).\n", group)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tTYPE\tADDRESS")
	for _, a := range members {
		info := cloudDeviceInfoFromAsset(a, nil)
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", info.ID, info.Name, info.Type, info.Address)
	}
	return tw.Flush()
}

// fleetMembershipResult is the per-device outcome of an add/rm, used for --json.
type fleetMembershipResult struct {
	Device  string `json:"device"`
	AssetID int32  `json:"assetId,omitempty"`
	Changed bool   `json:"changed"`
	Error   string `json:"error,omitempty"`
}

func runFleetGroupMembership(ctx context.Context, cloudGRPC, group string, devices []string, add bool) error {
	if err := validateGroupName(group); err != nil {
		return err
	}
	auth, err := pickAuthEntry(cloudGRPC)
	if err != nil {
		return err
	}
	assets, err := fetchCloudAssetsFiltered(ctx, auth, false)
	if err != nil {
		return err
	}

	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := cloudpb.NewAssetServiceClient(conn)

	results := make([]fleetMembershipResult, 0, len(devices))
	failures := 0
	for _, dev := range devices {
		res := fleetMembershipResult{Device: dev}
		asset, rErr := resolveCloudAsset(assets, dev)
		if rErr != nil {
			res.Error = rErr.Error()
			failures++
			results = append(results, res)
			continue
		}
		res.AssetID = asset.GetId()

		var newTags []string
		var changed bool
		if add {
			newTags, changed = addTag(asset.GetTags(), group)
		} else {
			newTags, changed = removeTag(asset.GetTags(), group)
		}
		res.Changed = changed
		if !changed {
			// Already in the desired state — skip the round-trip.
			results = append(results, res)
			continue
		}

		// UpdateAsset replaces the tags list with the value sent; other (optional)
		// fields left unset are not modified.
		if _, uErr := client.UpdateAsset(cloudContext(ctx, auth), &cloudpb.UpdateAssetRequest{
			Id:   asset.GetId(),
			Tags: newTags,
		}); uErr != nil {
			res.Changed = false
			res.Error = uErr.Error()
			failures++
		}
		results = append(results, res)
	}

	if jsonOutput || !isInteractiveTerminal() {
		if err := printJSON(results); err != nil {
			return err
		}
	} else {
		verb := "Added"
		prep := "to"
		if !add {
			verb = "Removed"
			prep = "from"
		}
		for _, r := range results {
			switch {
			case r.Error != "":
				fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", r.Device, r.Error)
			case r.Changed:
				fmt.Printf("  ✓ %s %s %s %q\n", verb, r.Device, prep, group)
			default:
				state := "already in"
				if !add {
					state = "not in"
				}
				fmt.Printf("  • %s %s group %q\n", r.Device, state, group)
			}
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d of %d device(s) failed", failures, len(devices))
	}
	return nil
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
