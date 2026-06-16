package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/catalog"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// deriveAppID returns a container-safe app id from an image reference: the
// repository basename, without registry, tag, or digest.
func deriveAppID(image string) string {
	ref := image
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	// Strip the tag: a ':' that appears after the last '/'.
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		if colon := strings.LastIndexByte(ref[slash:], ':'); colon >= 0 {
			ref = ref[:slash+colon]
		}
	} else if colon := strings.LastIndexByte(ref, ':'); colon >= 0 {
		ref = ref[:colon]
	}
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		ref = ref[slash+1:]
	}
	return ref
}

// registryHostFromImage returns the registry host of an image ref, defaulting
// to docker.io for short names (no host component).
func registryHostFromImage(image string) string {
	first := image
	i := strings.IndexByte(first, '/')
	if i < 0 {
		return "docker.io"
	}
	first = first[:i]
	// A host component contains a '.' or ':' or is exactly "localhost".
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return "docker.io"
}

// resolveInstallSource maps a CLI argument to an image reference and a default
// app config. A catalog name uses the curated entry; anything else is treated
// as a raw image reference with a minimal config.
func resolveInstallSource(arg string) (string, appconfig.AppConfig, error) {
	if e, ok := catalog.Lookup(arg); ok {
		return e.Image, e.DefaultConfig, nil
	}
	if arg == "" {
		return "", appconfig.AppConfig{}, fmt.Errorf("no app name or image specified")
	}
	return arg, appconfig.AppConfig{AppID: deriveAppID(arg)}, nil
}

// resolveRegistryAuth returns RegistryAuth for the pull, or nil for an
// anonymous pull. Resolution order: explicit flags, then ~/.docker/config.json
// for the image's registry host, then anonymous.
func resolveRegistryAuth(image, username, password string, passwordStdin bool) (*agentpb.RegistryAuth, error) {
	if passwordStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading password from stdin: %w", err)
		}
		password = strings.TrimRight(string(data), "\r\n")
	}
	if username != "" || password != "" {
		return &agentpb.RegistryAuth{
			RegistryHost: registryHostFromImage(image),
			Username:     username,
			Password:     password,
		}, nil
	}
	if a, ok := dockerConfigAuth(registryHostFromImage(image)); ok {
		return a, nil
	}
	return nil, nil
}

func newAppsInstallCmd() *cobra.Command {
	var username, password string
	var passwordStdin bool
	var nameOverride string

	cmd := &cobra.Command{
		Use:   "install [name|image]",
		Short: "Install a common app or container image onto the device",
		Long: "Install an app from the curated catalog (e.g. redis, postgres, " +
			"homeassistant) or any container image reference. The device pulls the " +
			"image directly from the registry. Private registries can be accessed " +
			"with --username/--password or via your local ~/.docker/config.json.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			arg := ""
			if len(args) > 0 {
				arg = args[0]
			} else {
				picked, err := pickInstallApp(ctx)
				if err != nil {
					return err
				}
				arg = picked
			}

			image, cfg, err := resolveInstallSource(arg)
			if err != nil {
				return err
			}
			if nameOverride != "" {
				cfg.AppID = nameOverride
			}

			auth, err := resolveRegistryAuth(image, username, password, passwordStdin)
			if err != nil {
				return err
			}

			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("installing apps is supported on agent-connected devices only")
			}

			cfgBytes, err := json.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("encoding app config: %w", err)
			}

			cliLogln("Installing %s (image %s) on the device…", cfg.AppID, image)
			if _, err := target.Agent.ContainerService.CreateContainer(ctx, &agentpb.CreateContainerRequest{
				ImageName:    image,
				AppName:      cfg.AppID,
				AppConfig:    cfgBytes,
				RegistryAuth: auth,
			}); err != nil {
				return fmt.Errorf("installing %s: %w", cfg.AppID, err)
			}

			// Start detached (UNLESS_STOPPED). The returned stream is not consumed:
			// installed catalog apps run in the background, like `apps start -d`.
			if _, err := target.Agent.ContainerService.StartContainer(ctx, &agentpb.StartContainerRequest{
				AppName:       cfg.AppID,
				RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
			}); err != nil {
				return fmt.Errorf("starting %s: %w", cfg.AppID, err)
			}

			cliSuccess("Installed and started %s.", cfg.AppID)
			return nil
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Registry username")
	cmd.Flags().StringVar(&password, "password", "", "Registry password or token")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the registry password from stdin")
	cmd.Flags().StringVar(&nameOverride, "name", "", "Override the installed app name")
	return cmd
}

// pickInstallApp shows an interactive picker of installable apps: the curated
// catalog plus the org's app releases (best-effort) when logged in.
func pickInstallApp(ctx context.Context) (string, error) {
	entries, err := catalog.Load()
	if err != nil {
		return "", err
	}
	var items []tui.PickerItem
	for _, e := range entries {
		items = append(items, tui.PickerItem{Name: e.Name, Description: e.Description, Value: e.Name})
	}
	if orgApps, oerr := listOrgApps(ctx); oerr != nil {
		cliNotice("Could not load org apps: %v", oerr)
	} else {
		for _, a := range orgApps {
			items = append(items, tui.PickerItem{Name: a.Name, Description: "your org", Value: a.Name})
		}
	}
	return runInstallPicker("Select an app to install", items)
}

// runInstallPicker runs an interactive picker over the given items and returns
// the selected item's Value as a string.
func runInstallPicker(title string, items []tui.PickerItem) (string, error) {
	picker := tui.NewPickerWithTitle(title)
	p := tea.NewProgram(picker)

	go func() {
		p.Send(tui.PickerAddMsg{Items: items})
		p.Send(tui.PickerDoneMsg{})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("install picker: %w", err)
	}
	pm := finalModel.(tui.PickerModel)
	if pm.Cancelled() {
		return "", ErrUserCancelled
	}
	sel := pm.Selected()
	if sel == nil {
		return "", fmt.Errorf("no app selected")
	}
	name, ok := sel.Value.(string)
	if !ok {
		return "", fmt.Errorf("invalid selection")
	}
	return name, nil
}
