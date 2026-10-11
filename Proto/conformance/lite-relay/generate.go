//go:build ignore

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"os"
)

func main() {
	scheme := mldsa65.Scheme()
	seed := make([]byte, scheme.SeedSize())
	for i := range seed {
		seed[i] = byte(i)
	}
	pk, sk := scheme.DeriveKey(seed)
	pub, _ := pk.MarshalBinary()
	device := "spiffe://wendy.sh/tenant/00000000-0000-4000-8000-000000000001/device/lite-test"
	operator := "spiffe://wendy.sh/tenant/00000000-0000-4000-8000-000000000001/operator/00000000-0000-4000-8000-000000000002"
	kid := "conformance-cloud-key"
	r := append([]byte("WLG1"), byte(len(kid)))
	r = append(r, []byte(kid)...)
	r = binary.BigEndian.AppendUint64(r, 1700000000)
	r = binary.BigEndian.AppendUint64(r, 1700001800)
	session := make([]byte, 16)
	session[15] = 3
	asset := make([]byte, 16)
	asset[15] = 4
	r = append(r, session...)
	r = append(r, asset...)
	for _, s := range []string{device, operator, "conformance principal descriptor"} {
		h := sha256.Sum256([]byte(s))
		r = append(r, h[:]...)
	}
	r = append(r, scheme.Sign(sk, r, nil)...)
	hash := sha256.Sum256(r)
	root := os.Args[1] + "/"
	os.MkdirAll(root, 0755)
	os.WriteFile(root+"grant.bin", r, 0644)
	os.WriteFile(root+"cloud-key.bin", pub, 0644)
	v := map[string]any{"key_id": kid, "public_key_hex": hex.EncodeToString(pub), "record_hex": hex.EncodeToString(r), "grant_hash_hex": hex.EncodeToString(hash[:]), "device_uri": device, "operator_uri": operator, "issued_at": 1700000000, "expires_at": 1700001800, "session_id_hex": hex.EncodeToString(session), "asset_id_hex": hex.EncodeToString(asset), "private_seed_hex": hex.EncodeToString(seed)}
	b, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(root+"vector.json", append(b, '\n'), 0644)
}
