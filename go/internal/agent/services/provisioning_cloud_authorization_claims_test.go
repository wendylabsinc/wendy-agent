package services

import (
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/unenrollproof"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func TestCloudResetCorrectSignatureCannotSubstituteClaims(t *testing.T) {
	for _, name := range []string{"purpose", "issuer", "audience", "expired", "future", "lifetime", "jti", "fingerprint", "operator", "correlation", "extra-claim", "caller-key-url"} {
		t.Run(name, func(t *testing.T) {
			svc, req, ctx := cloudResetFixture(t)
			key := renewFixtureAuthorization(t, svc, req, ctx, time.Now())
			unchanged := checkResetStateUnchanged(t, svc)
			parts := strings.Split(string(req.CloudResetAuthorization), ".")
			var header, body map[string]any
			h, _ := base64.RawURLEncoding.DecodeString(parts[0])
			b, _ := base64.RawURLEncoding.DecodeString(parts[1])
			if json.Unmarshal(h, &header) != nil || json.Unmarshal(b, &body) != nil {
				t.Fatal("fixture")
			}
			switch name {
			case "purpose":
				header["typ"] = "tunnel-session-grant+jws"
			case "issuer":
				body["iss"] = "https://evil.example"
			case "audience":
				body["aud"] = "different-agent"
			case "expired":
				body["iat"] = time.Now().Unix() - 60
				body["exp"] = time.Now().Unix() - 30
			case "future":
				body["iat"] = time.Now().Unix() + 60
				body["exp"] = time.Now().Unix() + 90
			case "lifetime":
				body["exp"] = int64(body["iat"].(float64)) + 31
			case "jti":
				body["jti"] = "reused-noncanonical-identifier"
			case "fingerprint":
				body["request_body"] = []byte{1, 2, 3}
			case "operator":
				body["request_jws"] = "forged.operator.signature"
			case "correlation":
				body["correlation_id"] = "different-operation"
			case "extra-claim":
				body["admin"] = true
			case "caller-key-url":
				header["jku"] = "https://evil.example/keys"
			}
			req.CloudResetAuthorization = fixtureJWS(t, key, header, body)
			if _, err := NewProvisioningServiceV2(svc).Unprovision(ctx, req); err == nil {
				t.Fatal("substituted authority accepted")
			}
			unchanged()
		})
	}
}

func TestCloudResetVerifiedPendingRecoversWithoutNetworkOrNewFile(t *testing.T) {
	for _, name := range []string{"before-erasure", "partial-erasure", "readable-file", "writable-directory", "symlink"} {
		t.Run(name, func(t *testing.T) {
			svc, req, ctx := cloudResetFixture(t)
			p, _ := peer.FromContext(ctx)
			operator := p.AuthInfo.(credentials.TLSInfo).State.PeerCertificates[0]
			at := time.Now()
			cloudKey, err := svc.verifyCloudReset(ctx, req.CloudResetAuthorization, svc.cloudHost, svc.principalURI, req.ExpectedCertificateSha256, req.CloudDeletion, operator, at, at)
			if err != nil {
				t.Fatal(err)
			}
			leaves, _ := certs.ParseCertsFromPEM([]byte(svc.certPEM))
			pair, err := certs.TLSKeyPair(svc.certPEM, svc.chainPEM, string(svc.keyPEM))
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := unenrollproof.SignCompletion(unenrollproof.Completion{
				Principal: svc.principalURI, Cloud: svc.cloudHost, AssetID: "33333333-3333-4333-8333-333333333333", Fingerprint: req.ExpectedCertificateSha256,
				AuthorizedAt: at.Unix(), AuthorizedAtNanos: at.UnixNano(), Certificate: leaves[0].Raw, Chain: svc.chainPEM, Revocation: req.RevocationProof,
				CloudDeletion: req.CloudDeletion, CloudAuthorization: req.CloudResetAuthorization, CloudVerificationKey: cloudKey, ResetOperator: operator.Raw,
			}, pair.PrivateKey.(crypto.Signer))
			if err != nil {
				t.Fatal(err)
			}
			state, err := svc.readProvisioningState()
			if err != nil {
				t.Fatal(err)
			}
			state.Unenrollment = &unenrollmentState{Status: unenrollmentPending, Receipt: receipt}
			state.KeyPEM = ""
			if err = svc.saveState(state); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "partial-erasure":
				if err = os.Remove(filepath.Join(svc.configPath, "device-key.pem")); err != nil {
					t.Fatal(err)
				}
			case "readable-file":
				if err = os.Chmod(svc.statePath(), 0644); err != nil {
					t.Fatal(err)
				}
			case "writable-directory":
				if err = os.Chmod(svc.configPath, 0777); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err = os.Rename(svc.statePath(), svc.statePath()+".moved"); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(svc.statePath()+".moved", svc.statePath()); err != nil {
					t.Fatal(err)
				}
			}
			unchanged := func() {}
			if name == "readable-file" || name == "writable-directory" || name == "symlink" {
				unchanged = checkResetStateUnchanged(t, svc)
			}
			// No time provider, Cloud dialer or HTTPS client is supplied on restart.
			recovered := NewProvisioningService(zap.NewNop(), svc.configPath)
			if name == "readable-file" || name == "writable-directory" || name == "symlink" {
				if recovered.resetRecoveryErr == nil {
					t.Fatal("unprotected saved key authorized erasure")
				}
				unchanged()
				return
			}
			if recovered.resetRecoveryErr != nil {
				t.Fatal(recovered.resetRecoveryErr)
			}
			got, err := recovered.cloudCompletion()
			if err != nil || string(got) != string(receipt) {
				t.Fatal("lost exact receipt", err)
			}
			entries, err := os.ReadDir(svc.configPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "provisioning.json" {
				t.Fatalf("extra recovery/credential files: %v", entries)
			}
			record, _, err := unenrollproof.ReadCompletion(got)
			if err != nil || record.Version != 2 || !record.AuthorizationTime().Equal(at) {
				t.Fatal("lost authenticated timestamp", err)
			}
			if _, err = x509.ParseCertificate(record.ResetOperator); err != nil {
				t.Fatal(err)
			}
		})
	}
}
