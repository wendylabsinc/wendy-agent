// Command wendyos-sign generates ML-DSA65 signing keys and produces detached
// signatures over release-artifact digests (driver add-ons, OS images) for the
// WendyOS manifest. It is the private-key counterpart to the agent's embedded
// sigverify.Verifier; CI runs `sign` and feeds the base64 output to the
// publisher, keeping one crypto implementation (the publisher stays crypto-free).
//
//	wendyos-sign keygen -pub pinned_signing_key.pem -priv signing-key.pem
//	wendyos-sign sign  -key signing-key.pem -file driver.raw     # -> base64 sig
//	wendyos-sign sign  -key signing-key.pem -sha256 <hex>        # sign a digest
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/sigverify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "sign":
		sign(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: wendyos-sign <keygen|sign> [flags]")
	fmt.Fprintln(os.Stderr, "  keygen -pub PATH -priv PATH")
	fmt.Fprintln(os.Stderr, "  sign -key PRIV.pem (-file PATH | -sha256 HEX)   # prints base64 detached signature")
	os.Exit(2)
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	pubPath := fs.String("pub", "", "output path for the public key PEM (embed as pinned_signing_key.pem)")
	privPath := fs.String("priv", "", "output path for the private signing key PEM (keep secret)")
	_ = fs.Parse(args)
	if *pubPath == "" || *privPath == "" {
		fatal("keygen requires -pub and -priv")
	}
	pubPEM, privPEM, err := sigverify.GenerateKeypair()
	if err != nil {
		fatal(err.Error())
	}
	if err := os.WriteFile(*privPath, privPEM, 0o600); err != nil {
		fatal(err.Error())
	}
	if err := os.WriteFile(*pubPath, pubPEM, 0o644); err != nil {
		fatal(err.Error())
	}
	fmt.Fprintf(os.Stderr, "wrote %s (public, embed to enable verification) and %s (private, keep secret)\n", *pubPath, *privPath)
}

func sign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "", "private signing key PEM (from `keygen -priv`)")
	filePath := fs.String("file", "", "artifact file to sign (its sha256 is signed)")
	shaHex := fs.String("sha256", "", "hex sha256 digest to sign (instead of -file)")
	_ = fs.Parse(args)
	if *keyPath == "" {
		fatal("sign requires -key")
	}
	privPEM, err := os.ReadFile(*keyPath)
	if err != nil {
		fatal(err.Error())
	}
	signer, err := sigverify.NewSignerFromPEM(privPEM)
	if err != nil {
		fatal(err.Error())
	}

	digest, err := resolveDigest(*filePath, *shaHex)
	if err != nil {
		fatal(err.Error())
	}
	sig, err := signer.Sign(digest)
	if err != nil {
		fatal(err.Error())
	}
	// base64 detached signature, exactly what the manifest's `signature` field
	// carries and the CLI base64-decodes into DriverSpec.signature.
	fmt.Println(base64.StdEncoding.EncodeToString(sig))
}

func resolveDigest(filePath, shaHex string) ([]byte, error) {
	switch {
	case filePath != "":
		f, err := os.Open(filePath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return nil, err
		}
		return h.Sum(nil), nil
	case shaHex != "":
		d, err := hex.DecodeString(strings.TrimSpace(shaHex))
		if err != nil || len(d) != sha256.Size {
			return nil, fmt.Errorf("invalid -sha256 hex (want a 64-char sha256)")
		}
		return d, nil
	default:
		return nil, fmt.Errorf("sign requires -file or -sha256")
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "wendyos-sign: "+msg)
	os.Exit(1)
}
