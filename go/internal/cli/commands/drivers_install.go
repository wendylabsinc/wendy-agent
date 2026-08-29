package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// resolveDriver finds a driver add-on for the device's OS version + kernel in the
// GCS manifest — the same catalog `wendy os` reads, so no separate registry
// server is needed — and returns its manifest entry plus download URL. pr>0
// resolves against a per-PR build manifest.
func resolveDriver(deviceType, osVersion, kernel, name string, pr int) (extensionEntry, string, error) {
	exts, err := driverExtensionsFor(deviceType, osVersion, pr)
	if err != nil {
		return extensionEntry{}, "", err
	}
	e, ok := selectExtension(exts, name, kernel)
	if !ok {
		return extensionEntry{}, "", fmt.Errorf("no driver %q published for %s on %s (kernel %s)", name, osVersion, deviceType, kernel)
	}
	if e.Path == "" {
		return extensionEntry{}, "", fmt.Errorf("driver %q has no artifact in the manifest", name)
	}
	return e, gcsBaseURL + "/" + e.Path, nil
}

// kernelMatches reports whether an add-on is usable on a device running kernel.
// An entry declaring no kernel is never offered: the agent refuses remote
// installs that do not declare one, so listing it would only promise a failure.
func kernelMatches(e extensionEntry, kernel string) bool {
	return kernel == "" || e.KernelVersion == kernel
}

// selectExtension picks the named extension that is usable on this kernel.
func selectExtension(exts []extensionEntry, name, kernel string) (extensionEntry, bool) {
	for _, e := range exts {
		if e.Name != name {
			continue
		}
		// A .ko is valid only for the exact kernel it was built against.
		if !kernelMatches(e, kernel) {
			continue
		}
		return e, true
	}
	return extensionEntry{}, false
}

// deviceDriverCoords reads the manifest coordinates of the connected device: its
// manifest key (device type) and the driver state (OS version, kernel, installed
// set) in one ListDrivers round-trip.
func deviceDriverCoords(ctx context.Context, target *SelectedDevice) (string, *agentpbv2.ListDriversResponse, error) {
	if target.Agent == nil {
		return "", nil, fmt.Errorf("selected device does not support driver add-ons")
	}
	ver, err := target.Agent.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return "", nil, fmt.Errorf("getting device type: %w", err)
	}
	dl, err := target.Agent.DriverService.ListDrivers(ctx, &agentpbv2.ListDriversRequest{})
	if err != nil {
		return "", nil, fmt.Errorf("getting device OS/kernel: %w", driverServiceErr(err))
	}
	return ver.GetDeviceType(), dl, nil
}

// sha256File hashes a file without reading it wholly into memory.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func newDriversInstallCmd() *cobra.Command {
	var filePath string
	var signatureB64 string
	var pr int
	var modules []string

	cmd := &cobra.Command{
		Use:   "install <name>",
		Short: "Install a driver add-on from the registry, or a local .raw with --file",
		Long: "With --file, streams a local .raw driver add-on to the device. Without it, resolves\n" +
			"<name> against the release manifest for the device's OS + kernel. Either way the add-on\n" +
			"is verified (sha256 + signature) before it is applied. <name> is the extension name and\n" +
			"must match the .raw's embedded extension-release, or the device refuses to merge it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			// An explicitly-passed empty --file (e.g. an unset shell variable)
			// must not silently fall through to the registry path.
			if cmd.Flags().Changed("file") && filePath == "" {
				return fmt.Errorf("--file requires a path to a .raw add-on")
			}
			// A registry install takes its module list from the manifest; a
			// --module override there would be silently discarded.
			if len(modules) > 0 && filePath == "" {
				return fmt.Errorf("--module is only valid with --file (registry installs use the driver's own module list)")
			}
			// A registry install carries the manifest's signature; overriding it
			// would let a caller pair one add-on's bytes with another's signature.
			if signatureB64 != "" && filePath == "" {
				return fmt.Errorf("--signature is only valid with --file (registry installs use the published signature)")
			}

			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("selected device does not support driver add-ons")
			}

			var spec *agentpbv2.DriverSpec

			if filePath != "" {
				// Local .raw: hash it here so the agent can verify what it received.
				sum, err := sha256File(filePath)
				if err != nil {
					return fmt.Errorf("reading %q: %w", filePath, err)
				}
				// Optional only while no signing key is embedded; once one is, the
				// agent refuses an unsigned add-on and this is the way to supply one.
				var sig []byte
				if signatureB64 != "" {
					sig, err = base64.StdEncoding.DecodeString(signatureB64)
					if err != nil {
						return fmt.Errorf("decoding --signature: %w", err)
					}
				}
				spec = &agentpbv2.DriverSpec{
					Name:        name,
					Sha256:      sum,
					Signature:   sig,
					ModulesLoad: modules,
				}
			} else {
				// Manifest: resolve for the device's OS + kernel, pin the resolved
				// digest + signature, and let the agent fetch the .raw from the URL.
				deviceType, dl, err := deviceDriverCoords(ctx, target)
				if err != nil {
					return err
				}
				e, downloadURL, err := resolveDriver(deviceType, dl.GetBaseVersion(), dl.GetKernelVersion(), name, pr)
				if err != nil {
					return err
				}
				sig, err := base64.StdEncoding.DecodeString(e.Signature)
				if err != nil {
					return fmt.Errorf("decoding driver signature: %w", err)
				}
				spec = &agentpbv2.DriverSpec{
					Name:          e.Name,
					Version:       e.Version,
					KernelVersion: e.KernelVersion,
					Sha256:        e.SHA256,
					Signature:     sig,
					ArtifactUrl:   downloadURL,
					ModulesLoad:   e.ModulesLoad,
				}
				cliLogln("Resolved %s %s for kernel %s", e.Name, e.Version, e.KernelVersion)
			}

			stream, err := target.Agent.DriverService.InstallDriver(ctx)
			if err != nil {
				return fmt.Errorf("starting driver install: %w", driverServiceErr(err))
			}
			// The spec is always the first message on the stream.
			if err := stream.Send(&agentpbv2.InstallDriverRequest{
				RequestType: &agentpbv2.InstallDriverRequest_Spec{Spec: spec},
			}); err != nil {
				return fmt.Errorf("sending driver spec: %w", driverServiceErr(err))
			}
			// Only a local install streams bytes; a manifest install is fetched
			// agent-side from artifact_url, so no chunks follow.
			if filePath != "" {
				if err := streamDriverChunks(stream, filePath); err != nil {
					return err
				}
			}
			if err := stream.CloseSend(); err != nil {
				return fmt.Errorf("closing driver upload: %w", err)
			}
			return consumeDriverApply(stream, name, "installed")
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "Install from a local .raw add-on instead of the registry")
	cmd.Flags().StringVar(&signatureB64, "signature", "", "Base64 detached signature over the .raw's sha256 (--file only; required once a signing key is embedded)")
	cmd.Flags().IntVar(&pr, "pr", 0, "Resolve against a per-PR build manifest instead of releases")
	cmd.Flags().StringArrayVar(&modules, "module", nil, "Override the add-on's built-in module list (repeatable; --file only; normally the driver declares its own)")
	return cmd
}

const driverChunkSize = 1 << 20 // 1 MiB per chunk

func streamDriverChunks(stream agentpbv2.WendyDriverService_InstallDriverClient, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %q: %w", path, err)
	}
	defer f.Close()

	buf := make([]byte, driverChunkSize)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if serr := stream.Send(&agentpbv2.InstallDriverRequest{
				RequestType: &agentpbv2.InstallDriverRequest_Chunk_{
					Chunk: &agentpbv2.InstallDriverRequest_Chunk{Data: buf[:n]},
				},
			}); serr != nil {
				// A send error (typically io.EOF) means the agent closed the
				// stream early; the real cause arrives via Recv, so stop
				// sending and let consumeDriverApply surface it.
				return nil
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("reading %q: %w", path, rerr)
		}
	}
}
