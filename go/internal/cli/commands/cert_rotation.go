package commands

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// maybePromptCertRotation offers to reissue any stored cloud certificate that carries
// its Wendy identity only in the legacy CommonName and lacks the authoritative
// "urn:wendy:org:..." URI SAN. Users enrolled before SAN issuance hold such certs; the
// mTLS identity path now prefers the SAN. Unlike the agent (which rotates automatically),
// the CLI prompts the user first. It is best-effort and only acts on an interactive
// terminal — it never blocks or fails a command.
func maybePromptCertRotation(ctx context.Context) {
	if !isInteractiveTerminal() {
		return
	}
	cfg, err := config.Load()
	if err != nil || len(cfg.Auth) == 0 {
		return
	}

	stale := false
	for _, auth := range cfg.Auth {
		if len(auth.Certificates) > 0 && certLacksIdentitySAN(auth.Certificates[0].PemCertificate) {
			stale = true
			break
		}
	}
	if !stale {
		return
	}

	if !confirmFn("Your Wendy certificate predates identity SANs. Rotate it now?") {
		return
	}

	rotated := 0
	for i := range cfg.Auth {
		if len(cfg.Auth[i].Certificates) == 0 ||
			!certLacksIdentitySAN(cfg.Auth[i].Certificates[0].PemCertificate) {
			continue
		}
		if err := refreshCertsForAuth(ctx, &cfg.Auth[i]); err != nil {
			fmt.Println(tui.ErrorMessage(
				fmt.Sprintf("Failed to rotate certificate for %s: %v", cfg.Auth[i].CloudGRPC, err)))
			continue
		}
		rotated++
	}
	if rotated == 0 {
		return
	}
	if err := config.Save(cfg); err != nil {
		fmt.Println(tui.ErrorMessage(fmt.Sprintf("Failed to save rotated certificates: %v", err)))
		return
	}
	fmt.Println(tui.SuccessMessage("Certificate rotated to include its identity SAN."))
}

// certLacksIdentitySAN reports whether a PEM certificate resolves to a Wendy identity
// but carries no "urn:wendy:org:..." URI SAN — i.e. a legacy CommonName-only identity
// that should be rotated. It returns false for certificates that already carry the SAN,
// carry no Wendy identity at all, or cannot be parsed.
func certLacksIdentitySAN(pemCertificate string) bool {
	if pemCertificate == "" {
		return false
	}
	leafPEM, err := certs.LeafCertificatePEM(pemCertificate)
	if err != nil {
		return false
	}
	block, _ := pem.Decode([]byte(leafPEM))
	if block == nil {
		return false
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if certs.HasWendyIdentitySAN(leaf) {
		return false
	}
	_, ok, err := certs.IdentityFromCert(leaf)
	return ok && err == nil
}
