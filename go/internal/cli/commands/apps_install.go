package commands

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	appID := deriveAppID(arg)
	if appID == "" {
		return "", appconfig.AppConfig{}, fmt.Errorf("could not derive an app name from %q; pass --name", arg)
	}
	return arg, appconfig.AppConfig{AppID: appID}, nil
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
	host := registryHostFromImage(image)
	if username != "" || password != "" {
		return &agentpb.RegistryAuth{RegistryHost: host, Username: username, Password: password}, nil
	}
	if a, ok := dockerConfigAuth(host); ok {
		return a, nil
	}
	return nil, nil
}

// dockerConfigAuth reads ~/.docker/config.json (honoring DOCKER_CONFIG) and
// returns credentials for host if present. Docker Hub credentials are stored
// under the canonical "https://index.docker.io/v1/" key, so requests for
// "docker.io" also consult that key.
func dockerConfigAuth(host string) (*agentpb.RegistryAuth, bool) {
	base := os.Getenv("DOCKER_CONFIG")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		base = filepath.Join(home, ".docker")
	}
	data, err := os.ReadFile(filepath.Join(base, "config.json"))
	if err != nil {
		return nil, false
	}
	var parsed struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, false
	}

	candidates := []string{host}
	if host == "docker.io" {
		candidates = append(candidates, "https://index.docker.io/v1/", "index.docker.io")
	}
	for _, key := range candidates {
		entry, ok := parsed.Auths[key]
		if !ok {
			continue
		}
		user, pass := entry.Username, entry.Password
		if entry.Auth != "" {
			if dec, derr := base64.StdEncoding.DecodeString(entry.Auth); derr == nil {
				if i := strings.IndexByte(string(dec), ':'); i >= 0 {
					user = string(dec)[:i]
					pass = string(dec)[i+1:]
				}
			}
		}
		if user == "" && pass == "" {
			continue
		}
		return &agentpb.RegistryAuth{RegistryHost: host, Username: user, Password: pass}, true
	}
	return nil, false
}

// looksLikeAuthError reports whether a registry pull error indicates the
// registry rejected the credentials (or required some). The agent wraps pull
// failures as codes.Internal, so the gRPC code is not informative; match on
// the containerd/registry message instead.
func looksLikeAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"401", "403", "unauthorized", "authentication required", "forbidden", "denied"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
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
			"image directly from the registry.\n\n" +
			"Private and internal-registry images can be installed by passing the " +
			"full image reference with credentials, e.g.:\n" +
			"  wendy device apps install registry.example.com/team/app:1.2.3 \\\n" +
			"      --username $USER --password $TOKEN\n\n" +
			"Credentials are also read from ~/.docker/config.json when --username/" +
			"--password are not given.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			arg := ""
			if len(args) > 0 {
				arg = args[0]
			} else {
				picked, err := pickInstallApp()
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
				if looksLikeAuthError(err) {
					return fmt.Errorf("installing %s: %w\n"+
						"the registry may require credentials; pass --username/--password "+
						"or log in to the registry with docker", cfg.AppID, err)
				}
				return fmt.Errorf("installing %s: %w", cfg.AppID, err)
			}

			// Start the app detached under an UNLESS_STOPPED restart policy. We
			// must read from the stream until the agent confirms the container
			// has started: StartContainer starts the container synchronously and
			// only then sends the Started message, so returning early would let
			// the deferred connection close cancel the in-flight start. Once
			// Started arrives the container runs independently of this stream.
			startStream, err := target.Agent.ContainerService.StartContainer(ctx, &agentpb.StartContainerRequest{
				AppName:       cfg.AppID,
				RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
			})
			if err != nil {
				return fmt.Errorf("starting %s: %w", cfg.AppID, err)
			}
			for {
				resp, rerr := startStream.Recv()
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					return fmt.Errorf("starting %s: %w", cfg.AppID, rerr)
				}
				if resp.GetStarted() != nil {
					break
				}
			}

			cliSuccess("Installed and started %s.", cfg.AppID)
			openAppWebUI(&cfg, target.Agent.Host)
			return nil
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Registry username")
	cmd.Flags().StringVar(&password, "password", "", "Registry password or token")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the registry password from stdin")
	cmd.Flags().StringVar(&nameOverride, "name", "", "Override the installed app name")
	return cmd
}

// openAppWebUI opens the app's web UI in the developer's browser when the
// config declares a postStart openURL hook (catalog web-UI apps do). The URL is
// templated against the device host, reusing the same mechanism as `wendy run`.
func openAppWebUI(cfg *appconfig.AppConfig, host string) {
	if cfg.Hooks == nil || cfg.Hooks.PostStart == nil || cfg.Hooks.PostStart.OpenURL == "" || host == "" {
		return
	}
	url := expandHookEnv(cfg.Hooks.PostStart.OpenURL, host, cfg.AppID)
	if err := browserOpen(url); err != nil {
		cliLogln("Could not open browser for %s: %v", url, err)
		return
	}
	cliLogln("Opening %s", url)
}

// catalogPickerItems builds picker rows for the catalog, grouped by category.
// The category is carried in Type (rendered as a column) and SortKey groups
// rows by "<category>_<name>" so related apps appear together.
func catalogPickerItems(entries []catalog.Entry) []tui.PickerItem {
	items := make([]tui.PickerItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, tui.PickerItem{
			Name:        e.Name,
			Description: e.Description,
			Type:        e.Category,
			SortKey:     e.Category + "_" + e.Name,
			Value:       e.Name,
		})
	}
	return items
}

// installPickerColumns renders Category, Name, and Description columns.
func installPickerColumns() []tui.PickerColumn {
	return []tui.PickerColumn{
		{Title: "Category", MinWidth: 12, Required: true, Value: func(it tui.PickerItem) string { return it.Type }},
		{Title: "Name", MinWidth: 14, Required: true, Value: func(it tui.PickerItem) string { return it.Name }},
		{Title: "Description", MinWidth: 20, Value: func(it tui.PickerItem) string { return it.Description }},
	}
}

// pickInstallApp shows an interactive, searchable picker of the curated catalog
// grouped by category and returns the selected app name.
func pickInstallApp() (string, error) {
	entries, err := catalog.Load()
	if err != nil {
		return "", err
	}
	picker := tui.NewPickerWithTitleAndColumns("Select an app to install", installPickerColumns())
	picker.Filterable = true
	p := tea.NewProgram(picker)

	items := catalogPickerItems(entries)
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
