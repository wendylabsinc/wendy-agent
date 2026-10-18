package commands

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// `wendy device keys` manages a device's trusted operator keys (WDY-3882):
// the public keys whose self-issued certificates a device that is not
// cloud-enrolled accepts, each as owner, operator or viewer. The device
// enforces everything (owner-only, last owner irremovable, refused while
// cloud-enrolled); the CLI passes its refusals through verbatim.

func newDeviceKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage the operator keys a locally owned device trusts",
		Long: "Manage the operator keys a device that is not enrolled in Wendy Cloud trusts.\n" +
			"The key of the computer that installed the device is its first owner. Owners can add\n" +
			"further keys as owner, operator (apps, logs, shell, updates) or viewer (reads only),\n" +
			"and remove them; the last owner key cannot be removed. Requires an owner key.\n" +
			"While a device is enrolled in Wendy Cloud its access is managed by cloud and these\n" +
			"commands are refused.",
	}
	cmd.AddCommand(newDeviceKeysListCmd(), newDeviceKeysAddCmd(), newDeviceKeysRemoveCmd())
	return cmd
}

// withTrustedKeysClient connects to the selected device and runs fn against
// its trusted-keys service.
func withTrustedKeysClient(ctx context.Context, fn func(agentpbv2.WendyTrustedKeysServiceClient) error) error {
	conn, err := connectToAgent(ctx, SuppressProvisioningHint())
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(agentpbv2.NewWendyTrustedKeysServiceClient(conn.Conn))
}

func newDeviceKeysListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the device's trusted operator keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withTrustedKeysClient(cmd.Context(), func(c agentpbv2.WendyTrustedKeysServiceClient) error {
				return runDeviceKeysList(cmd.Context(), c, cmd.OutOrStdout(), jsonOutput)
			})
		},
	}
}

func newDeviceKeysAddCmd() *cobra.Command {
	var role, label string
	cmd := &cobra.Command{
		Use:   "add <public-key-file|->",
		Short: "Trust another operator's public key on the device",
		Long: "Trust another operator's public key on the device with the given role.\n" +
			"The file is the output of 'wendy auth local-key show' on the other operator's\n" +
			"computer, or any PEM \"PUBLIC KEY\" block; '-' reads it from stdin.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := parseTrustedKeyRole(role)
			if err != nil {
				return err
			}
			der, err := readPublicKeyArg(args[0], cmd.InOrStdin())
			if err != nil {
				return err
			}
			return withTrustedKeysClient(cmd.Context(), func(c agentpbv2.WendyTrustedKeysServiceClient) error {
				return runDeviceKeysAdd(cmd.Context(), c, cmd.OutOrStdout(), der, r, label, jsonOutput)
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "role of the key: owner, operator or viewer (required)")
	cmd.Flags().StringVar(&label, "label", "", "a name for the key, shown by 'wendy device keys list'")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func newDeviceKeysRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <fingerprint>",
		Short: "Stop trusting a key on the device",
		Long:  "Stop trusting a key on the device. The fingerprint is the one 'wendy device keys list' prints.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTrustedKeysClient(cmd.Context(), func(c agentpbv2.WendyTrustedKeysServiceClient) error {
				return runDeviceKeysRemove(cmd.Context(), c, cmd.OutOrStdout(), args[0], jsonOutput)
			})
		},
	}
}

var trustedKeyRoles = map[string]agentpbv2.TrustedKeyRole{
	"owner":    agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OWNER,
	"operator": agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OPERATOR,
	"viewer":   agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_VIEWER,
}

func parseTrustedKeyRole(s string) (agentpbv2.TrustedKeyRole, error) {
	if r, ok := trustedKeyRoles[strings.ToLower(strings.TrimSpace(s))]; ok {
		return r, nil
	}
	return 0, fmt.Errorf("--role must be owner, operator or viewer, not %q", s)
}

func trustedKeyRoleName(r agentpbv2.TrustedKeyRole) string {
	for name, v := range trustedKeyRoles {
		if v == r {
			return name
		}
	}
	return "unknown"
}

// readPublicKeyArg reads a public key file (or stdin for "-") and returns its
// SubjectPublicKeyInfo DER. The first PEM "PUBLIC KEY" block counts, so the
// full output of `wendy auth local-key show` works as is. A private key is
// refused rather than sent anywhere.
func readPublicKeyArg(arg string, stdin io.Reader) ([]byte, error) {
	var data []byte
	var err error
	if arg == "-" {
		data, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
	} else {
		data, err = os.ReadFile(arg)
	}
	if err != nil {
		return nil, fmt.Errorf("reading public key: %w", err)
	}
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return nil, errors.New("this is a private key; pass the public key ('wendy auth local-key show' prints it)")
		}
		if block.Type == "PUBLIC KEY" {
			if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
				return nil, fmt.Errorf("parsing public key: %w", err)
			}
			return block.Bytes, nil
		}
	}
	return nil, errors.New("no PEM \"PUBLIC KEY\" block found; pass the output of 'wendy auth local-key show'")
}

// trustedKeysError surfaces the device's refusal text as is: it is the
// device's decision ("managed by cloud", "last owner", "requires an owner
// key"), not something for the CLI to reword. An agent without the service
// predates local access.
func trustedKeysError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	if st.Code() == codes.Unimplemented {
		return errors.New("this device's agent does not support trusted operator keys; update it with 'wendy device update'")
	}
	return errors.New(st.Message())
}

type trustedKeyJSON struct {
	Fingerprint string `json:"fingerprint"`
	Role        string `json:"role"`
	Label       string `json:"label,omitempty"`
	AddedAt     string `json:"addedAt,omitempty"`
	AddedBy     string `json:"addedBy,omitempty"`
	ThisKey     bool   `json:"thisComputer,omitempty"`
}

func trustedKeyView(k *agentpbv2.TrustedKey, mine string) trustedKeyJSON {
	v := trustedKeyJSON{
		Fingerprint: k.GetFingerprint(),
		Role:        trustedKeyRoleName(k.GetRole()),
		Label:       k.GetLabel(),
		AddedBy:     k.GetAddedBy(),
		ThisKey:     mine != "" && k.GetFingerprint() == mine,
	}
	if k.GetAddedAt() > 0 {
		v.AddedAt = time.Unix(k.GetAddedAt(), 0).UTC().Format(time.RFC3339)
	}
	return v
}

// thisComputerFingerprint is the local key's fingerprint, "" when there is
// none yet; listing never creates one.
func thisComputerFingerprint() string {
	path, err := localkey.Path()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	key, err := localkey.LoadOrCreate(path)
	if err != nil {
		return ""
	}
	fp, _ := localkey.Fingerprint(key)
	return fp
}

func runDeviceKeysList(ctx context.Context, c agentpbv2.WendyTrustedKeysServiceClient, out io.Writer, asJSON bool) error {
	resp, err := c.ListTrustedKeys(ctx, &agentpbv2.ListTrustedKeysRequest{})
	if err != nil {
		return trustedKeysError(err)
	}
	mine := thisComputerFingerprint()
	views := make([]trustedKeyJSON, 0, len(resp.GetKeys()))
	for _, k := range resp.GetKeys() {
		views = append(views, trustedKeyView(k, mine))
	}
	if asJSON {
		return writeJSON(out, map[string]any{"keys": views})
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "FINGERPRINT\tROLE\tLABEL\tADDED\tADDED BY")
	for _, v := range views {
		label := v.Label
		if v.ThisKey {
			label = strings.TrimSpace(label + " (this computer)")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.Fingerprint, v.Role, label, v.AddedAt, v.AddedBy)
	}
	return w.Flush()
}

func runDeviceKeysAdd(ctx context.Context, c agentpbv2.WendyTrustedKeysServiceClient, out io.Writer, der []byte, role agentpbv2.TrustedKeyRole, label string, asJSON bool) error {
	resp, err := c.AddTrustedKey(ctx, &agentpbv2.AddTrustedKeyRequest{PublicKey: der, Role: role, Label: label})
	if err != nil {
		return trustedKeysError(err)
	}
	if asJSON {
		return writeJSON(out, trustedKeyView(resp.GetKey(), ""))
	}
	fmt.Fprintf(out, "Trusted %s as %s.\n", resp.GetKey().GetFingerprint(), trustedKeyRoleName(resp.GetKey().GetRole()))
	return nil
}

func runDeviceKeysRemove(ctx context.Context, c agentpbv2.WendyTrustedKeysServiceClient, out io.Writer, fingerprint string, asJSON bool) error {
	fingerprint = strings.TrimSpace(fingerprint)
	if _, err := c.RemoveTrustedKey(ctx, &agentpbv2.RemoveTrustedKeyRequest{Fingerprint: fingerprint}); err != nil {
		return trustedKeysError(err)
	}
	if asJSON {
		return writeJSON(out, map[string]any{"removed": fingerprint})
	}
	fmt.Fprintf(out, "Removed %s.\n", fingerprint)
	return nil
}

// newAuthLocalKeyCmd groups the local operator key: the key this computer
// proves itself with to devices that are not cloud-enrolled.
func newAuthLocalKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local-key",
		Short: "Show this computer's local operator key",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print this computer's local operator public key and fingerprint",
		Long: "Print this computer's local operator public key and its fingerprint. Give the output to an\n" +
			"owner of a device, who trusts it with 'wendy device keys add <file> --role <role>'.\n" +
			"The key is created on first use; its private half never leaves this computer.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthLocalKeyShow(cmd.OutOrStdout(), jsonOutput)
		},
	})
	return cmd
}

func runAuthLocalKeyShow(out io.Writer, asJSON bool) error {
	path, err := localkey.Path()
	if err != nil {
		return err
	}
	key, err := localkey.LoadOrCreate(path)
	if err != nil {
		return err
	}
	der, err := localkey.PublicKeyDER(key)
	if err != nil {
		return err
	}
	fp := certs.SPKIFingerprint(der)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if asJSON {
		return writeJSON(out, map[string]string{"fingerprint": fp, "publicKey": pubPEM, "path": path})
	}
	fmt.Fprintf(out, "Fingerprint: %s\n\n%s", fp, pubPEM)
	return nil
}
