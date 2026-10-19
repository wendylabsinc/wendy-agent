package unenrollproof

import (
	"crypto"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestResetOperatorPreservesBothMLDSAProfiles(t *testing.T) {
	for _, profile := range []struct {
		name   string
		params mldsa.Parameters
	}{{"ML-DSA-65", mldsa.MLDSA65()}, {"ML-DSA-87", mldsa.MLDSA87()}} {
		t.Run(profile.name, func(t *testing.T) {
			key, err := mldsa.GenerateKey(profile.params)
			if err != nil {
				t.Fatal(err)
			}
			leaf := &x509.Certificate{PublicKey: key.Public(), Raw: []byte("validated-peer-leaf")}
			request := []byte{1, 2, 3}
			hash := sha256.Sum256(request)
			thumb := sha256.Sum256(leaf.Raw)
			at := time.Now()
			descriptor := map[string]any{"aud": "https://cloud.wendy.sh/broker", "body_sha256": base64.RawURLEncoding.EncodeToString(hash[:]), "correlation_id": "operator-test", "expiry": at.Unix() + 29, "iat": at.Unix() - 1, "nonce": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "operation": "wendycloud.v2.AssetService/DeleteAsset", "target": map[string]any{"tenant": "tenant", "resource": "asset/asset"}}
			body, err := json.Marshal(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			token := func(algorithm string) string {
				header, err := json.Marshal(map[string]string{"alg": algorithm, "kid": base64.RawURLEncoding.EncodeToString(thumb[:])})
				if err != nil {
					t.Fatal(err)
				}
				input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
				sig, err := key.Sign(rand.Reader, []byte(input), crypto.Hash(0))
				if err != nil {
					t.Fatal(err)
				}
				return input + "." + base64.RawURLEncoding.EncodeToString(sig)
			}
			if err := verifyOperatorReset(token(profile.name), request, "asset", "tenant", "operator-test", leaf, at, at); err != nil {
				t.Fatal(err)
			}
			for _, wrong := range []string{"ES256", "ML-DSA-44", "ML-DSA-65", "ML-DSA-87"} {
				if wrong != profile.name && verifyOperatorReset(token(wrong), request, "asset", "tenant", "operator-test", leaf, at, at) == nil {
					t.Fatalf("algorithm confusion accepted: %s", wrong)
				}
			}
		})
	}
}
