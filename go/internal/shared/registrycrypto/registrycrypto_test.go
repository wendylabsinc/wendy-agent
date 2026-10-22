package registrycrypto

import (
	"bytes"
	"crypto/hpke"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Vectors: service-protos conformance/registry and conformance/reqsig at the
// commit in testdata/SOURCE.

func load(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type binding struct {
	OrganizationID  string `json:"organization_id"`
	ScopeKind       byte   `json:"scope_kind"`
	ScopeID         string `json:"scope_id"`
	KeyID           string `json:"key_id"`
	RecipientKind   int32  `json:"recipient_kind"`
	Recipient       string `json:"recipient"`
	RecipientKeyID  string `json:"recipient_key_id"`
	DeploymentID    string `json:"deployment_id"`
	ManifestDigest  string `json:"manifest_digest"`
	ContentKeyID    string `json:"content_key_id"`
	DeploymentKeyID string `json:"deployment_key_id"`
}

func (b binding) wrap() Wrap {
	return Wrap{b.OrganizationID, b.ScopeKind, b.ScopeID, b.KeyID, b.RecipientKind, b.Recipient, b.RecipientKeyID}
}

func (b binding) sealed() SealedKey {
	return SealedKey{b.OrganizationID, b.DeploymentID, b.ManifestDigest, b.ContentKeyID, b.DeploymentKeyID}
}

type registryVectors struct {
	PerBlobKey struct {
		ContentKeyHex string `json:"content_key_hex"`
		ContentKeyID  string `json:"content_key_id"`
		Blobs         []struct {
			BlobKeyHex      string `json:"blob_key_hex"`
			PlaintextDigest string `json:"plaintext_digest"`
		} `json:"blobs"`
	} `json:"per_blob_key"`
	HybridKEM struct {
		IKMHex        string `json:"ikm_hex"`
		PublicKeyHex  string `json:"public_key_hex"`
		KeyID         string `json:"key_id"`
		EncHex        string `json:"enc_hex"`
		CiphertextHex string `json:"ciphertext_hex"`
		InfoHex       string `json:"info_hex"`
		AADHex        string `json:"aad_hex"`
		PlaintextHex  string `json:"plaintext_hex"`
	} `json:"hybrid_kem"`
	ChunkAEAD struct {
		ContentKeyHex string `json:"content_key_hex"`
		ChunkSize     int    `json:"chunk_size"`
		Vectors       []struct {
			Name             string `json:"name"`
			Expect           string `json:"expect"`
			PlaintextHex     string `json:"plaintext_hex"`
			PlaintextDigest  string `json:"plaintext_digest"`
			PlaintextSize    uint64 `json:"plaintext_size"`
			CiphertextHex    string `json:"ciphertext_hex"`
			CiphertextDigest string `json:"ciphertext_digest"`
		} `json:"vectors"`
	} `json:"chunk_aead"`
	KeyWrap struct {
		RecipientIKMHex string `json:"recipient_ikm_hex"`
		Vectors         []struct {
			Name          string  `json:"name"`
			Expect        string  `json:"expect"`
			Binding       binding `json:"binding"`
			InfoHex       string  `json:"info_hex"`
			EncHex        string  `json:"enc_hex"`
			CiphertextHex string  `json:"ciphertext_hex"`
			KeyHex        string  `json:"wrapped_key_hex"`
		} `json:"vectors"`
	} `json:"key_wrap"`
	SealedContentKey struct {
		DeploymentKeyHex string `json:"deployment_key_hex"`
		Vectors          []struct {
			Name          string  `json:"name"`
			Expect        string  `json:"expect"`
			Binding       binding `json:"binding"`
			AADHex        string  `json:"aad_hex"`
			NonceHex      string  `json:"nonce_hex"`
			CiphertextHex string  `json:"ciphertext_hex"`
			ContentKeyHex string  `json:"content_key_hex"`
		} `json:"vectors"`
	} `json:"sealed_content_key"`
	Todo []json.RawMessage `json:"todo"`
}

func TestRegistryVectors(t *testing.T) {
	var v registryVectors
	load(t, "registry-vectors-v1.json", &v)

	contentKey := unhex(t, v.PerBlobKey.ContentKeyHex)
	if KeyID(contentKey) != v.PerBlobKey.ContentKeyID {
		t.Fatal("content_key_id")
	}
	for _, b := range v.PerBlobKey.Blobs {
		key, err := BlobKey(contentKey, b.PlaintextDigest)
		if err != nil || hex.EncodeToString(key) != b.BlobKeyHex {
			t.Fatalf("per-blob key for %s", b.PlaintextDigest)
		}
	}

	h := v.HybridKEM
	priv, err := KEM.DeriveKeyPair(unhex(t, h.IKMHex))
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PublicKey().Bytes()
	if hex.EncodeToString(pub) != h.PublicKeyHex || KeyID(pub) != h.KeyID || len(pub) != 1216 {
		t.Fatal("hybrid KEM public key or key_id")
	}
	// The suite check uses labelled info/aad, not the wrap layout, so it goes
	// through crypto/hpke directly with this package's suite.
	recipient, err := hpke.NewRecipient(unhex(t, h.EncHex), priv, kdf, aead, unhex(t, h.InfoHex))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := recipient.Open(unhex(t, h.AADHex), unhex(t, h.CiphertextHex)); err != nil || hex.EncodeToString(pt) != h.PlaintextHex {
		t.Fatal("hybrid KEM vector does not open")
	}

	if len(v.Todo) != 0 {
		t.Fatalf("vendored vectors still list todo entries: %s", v.Todo)
	}
	c := v.ChunkAEAD
	if len(c.Vectors) == 0 || len(v.KeyWrap.Vectors) == 0 || len(v.SealedContentKey.Vectors) == 0 {
		t.Fatal("chunk, wrap or seal vectors missing")
	}
	for _, cv := range c.Vectors {
		want := Blob{PlaintextDigest: cv.PlaintextDigest, CiphertextDigest: cv.CiphertextDigest, PlaintextSize: cv.PlaintextSize, CiphertextSize: uint64(len(cv.CiphertextHex) / 2)}
		var out bytes.Buffer
		err := DecryptBlob(&out, bytes.NewReader(unhex(t, cv.CiphertextHex)), unhex(t, c.ContentKeyHex), want, c.ChunkSize)
		if cv.Expect == "valid" {
			if err != nil || hex.EncodeToString(out.Bytes()) != cv.PlaintextHex {
				t.Fatalf("%s: %v", cv.Name, err)
			}
			// Encryption is deterministic: a producer reproduces the bytes.
			var enc bytes.Buffer
			got, err := EncryptBlob(&enc, bytes.NewReader(unhex(t, cv.PlaintextHex)), unhex(t, c.ContentKeyHex), cv.PlaintextDigest, c.ChunkSize)
			if err != nil || got != want || hex.EncodeToString(enc.Bytes()) != cv.CiphertextHex {
				t.Fatalf("%s: encryption does not reproduce the vector", cv.Name)
			}
		} else if !errors.Is(err, ErrOpen) {
			t.Fatalf("%s: want ErrOpen, got %v", cv.Name, err)
		}
	}

	wrapPriv, err := KEM.DeriveKeyPair(unhex(t, v.KeyWrap.RecipientIKMHex))
	if err != nil {
		t.Fatal(err)
	}
	for _, wv := range v.KeyWrap.Vectors {
		w := wv.Binding.wrap()
		info, err := w.info()
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(info) != wv.InfoHex {
			t.Fatalf("%s: info bytes differ", wv.Name)
		}
		key, err := w.Open(wrapPriv, unhex(t, wv.EncHex), unhex(t, wv.CiphertextHex))
		if wv.Expect == "valid" {
			if err != nil || hex.EncodeToString(key) != wv.KeyHex {
				t.Fatalf("%s: %v", wv.Name, err)
			}
		} else if !errors.Is(err, ErrOpen) {
			t.Fatalf("%s: want ErrOpen, got %v", wv.Name, err)
		}
	}

	for _, sv := range v.SealedContentKey.Vectors {
		s := sv.Binding.sealed()
		aad, err := s.aad()
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(aad) != sv.AADHex {
			t.Fatalf("%s: aad bytes differ", sv.Name)
		}
		key, err := s.Open(unhex(t, v.SealedContentKey.DeploymentKeyHex), unhex(t, sv.NonceHex), unhex(t, sv.CiphertextHex))
		if sv.Expect == "valid" {
			if err != nil || hex.EncodeToString(key) != sv.ContentKeyHex {
				t.Fatalf("%s: %v", sv.Name, err)
			}
		} else if !errors.Is(err, ErrOpen) {
			t.Fatalf("%s: want ErrOpen, got %v", sv.Name, err)
		}
	}
}

func encrypt(t *testing.T, contentKey, plaintext []byte, chunkSize int) ([]byte, Blob) {
	t.Helper()
	var ct bytes.Buffer
	b, err := EncryptBlob(&ct, bytes.NewReader(plaintext), contentKey, sha(plaintext), chunkSize)
	if err != nil {
		t.Fatal(err)
	}
	return ct.Bytes(), b
}

func TestBlobRoundTripAndTamper(t *testing.T) {
	contentKey := NewKey()
	for _, size := range []int{0, 1, 15, 16, 17, 32, 40} {
		pt := bytes.Repeat([]byte{byte(size)}, size)
		ct, b := encrypt(t, contentKey, pt, 16)
		chunks := max(1, (size+15)/16)
		if b.CiphertextSize != uint64(size+16*chunks) || b.PlaintextSize != uint64(size) {
			t.Fatalf("size %d: framing %+v", size, b)
		}
		var out bytes.Buffer
		if err := DecryptBlob(&out, bytes.NewReader(ct), contentKey, b, 16); err != nil || !bytes.Equal(out.Bytes(), pt) {
			t.Fatalf("size %d: round trip: %v", size, err)
		}
	}

	pt := bytes.Repeat([]byte("abcdefgh"), 6) // 48 bytes = 3 full chunks of 16
	ct, b := encrypt(t, contentKey, pt, 16)
	other := []byte("another blob, same release, same key")
	otherCT, otherB := encrypt(t, contentKey, other, 16)
	const c = 32 // ciphertext chunk
	cases := map[string]struct {
		ct   []byte
		want Blob
	}{
		"truncated last chunk": {ct[:2*c], b},
		"reordered chunks":     {append(append(append([]byte{}, ct[c:2*c]...), ct[:c]...), ct[2*c:]...), b},
		"trailing chunk":       {append(append([]byte{}, ct...), ct[2*c:]...), b},
		"flipped bit":          {append(append([]byte{}, ct[:5]...), append([]byte{ct[5] ^ 1}, ct[6:]...)...), b},
		"cross-blob splice":    {append(append([]byte{}, otherCT[:c]...), ct[c:]...), b},
		"other blob's key":     {otherCT, Blob{PlaintextDigest: b.PlaintextDigest, CiphertextDigest: otherB.CiphertextDigest, PlaintextSize: otherB.PlaintextSize, CiphertextSize: otherB.CiphertextSize}},
		"empty ciphertext":     {nil, b},
	}
	for name, tc := range cases {
		if err := DecryptBlob(&bytes.Buffer{}, bytes.NewReader(tc.ct), contentKey, tc.want, 16); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: want ErrOpen, got %v", name, err)
		}
	}

	// Authentic chunks, wrong manifest digests: refused at the end.
	bad := b
	bad.CiphertextDigest = otherB.CiphertextDigest
	if err := DecryptBlob(&bytes.Buffer{}, bytes.NewReader(ct), contentKey, bad, 16); err == nil {
		t.Error("ciphertext digest mismatch accepted")
	}
	bad = b
	bad.PlaintextSize++
	if err := DecryptBlob(&bytes.Buffer{}, bytes.NewReader(ct), contentKey, bad, 16); err == nil {
		t.Error("plaintext size mismatch accepted")
	}
	if _, err := EncryptBlob(&bytes.Buffer{}, bytes.NewReader(pt), contentKey, sha(other), 16); err == nil {
		t.Error("encryption under a digest the plaintext does not have accepted")
	}
}

func TestWrapAndSeal(t *testing.T) {
	priv, err := KEM.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PublicKey().Bytes()
	deploymentKey, contentKey := NewKey(), NewKey()
	w := Wrap{OrganizationID: "2558fd76-afc7-466e-9613-6b715296a526", Scope: ScopeDeployment, ScopeID: "dep-1", KeyID: KeyID(deploymentKey), RecipientKind: 2, Recipient: "spiffe://wendy.sh/tenant/2558fd76-afc7-466e-9613-6b715296a526/operator/u1", RecipientKeyID: KeyID(pub)}
	enc, ct, err := w.Seal(pub, deploymentKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := w.Open(priv, enc, ct); err != nil || !bytes.Equal(got, deploymentKey) {
		t.Fatalf("unwrap: %v", err)
	}
	for name, moved := range map[string]Wrap{
		"other tenant":    {"00000000-0000-4000-8000-000000000000", w.Scope, w.ScopeID, w.KeyID, w.RecipientKind, w.Recipient, w.RecipientKeyID},
		"release scope":   {w.OrganizationID, ScopeRelease, w.ScopeID, w.KeyID, w.RecipientKind, w.Recipient, w.RecipientKeyID},
		"other scope id":  {w.OrganizationID, w.Scope, "dep-2", w.KeyID, w.RecipientKind, w.Recipient, w.RecipientKeyID},
		"other kind":      {w.OrganizationID, w.Scope, w.ScopeID, w.KeyID, 1, w.Recipient, w.RecipientKeyID},
		"other recipient": {w.OrganizationID, w.Scope, w.ScopeID, w.KeyID, w.RecipientKind, w.Recipient + "x", w.RecipientKeyID},
		"other key id":    {w.OrganizationID, w.Scope, w.ScopeID, KeyID(contentKey), w.RecipientKind, w.Recipient, w.RecipientKeyID},
	} {
		if _, err := moved.Open(priv, enc, ct); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: want ErrOpen, got %v", name, err)
		}
	}
	if _, _, err := w.Seal(pub, contentKey); err == nil {
		t.Error("wrapping a key other than key_id accepted")
	}
	stranger, _ := KEM.GenerateKey()
	if _, err := w.Open(stranger, enc, ct); err == nil {
		t.Error("another recipient key opened the wrap")
	}

	s := SealedKey{w.OrganizationID, "dep-1", "sha256:" + KeyID([]byte("manifest")), KeyID(contentKey), KeyID(deploymentKey)}
	nonce, sealed, err := s.Seal(deploymentKey, contentKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Open(deploymentKey, nonce, sealed); err != nil || !bytes.Equal(got, contentKey) {
		t.Fatalf("open sealed key: %v", err)
	}
	for name, moved := range map[string]SealedKey{
		"other tenant":     {"00000000-0000-4000-8000-000000000000", s.DeploymentID, s.ManifestDigest, s.ContentKeyID, s.DeploymentKeyID},
		"other deployment": {s.OrganizationID, "dep-2", s.ManifestDigest, s.ContentKeyID, s.DeploymentKeyID},
		"other manifest":   {s.OrganizationID, s.DeploymentID, "sha256:00", s.ContentKeyID, s.DeploymentKeyID},
		"other content id": {s.OrganizationID, s.DeploymentID, s.ManifestDigest, KeyID(deploymentKey), s.DeploymentKeyID},
	} {
		if _, err := moved.Open(deploymentKey, nonce, sealed); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: want ErrOpen, got %v", name, err)
		}
	}
	if _, err := s.Open(NewKey(), nonce, sealed); err == nil {
		t.Error("another deployment key opened the sealed key")
	}
}

// RecipientKey and RegisterEncryptionKeyRequest are decoded field by field
// until wendycloud/v2/registry.proto is vendored.
func fields(t *testing.T, b []byte) map[protowire.Number][][]byte {
	t.Helper()
	out := map[protowire.Number][][]byte{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatal("bad tag")
		}
		b = b[n:]
		var v []byte
		switch typ {
		case protowire.BytesType:
			v, n = protowire.ConsumeBytes(b)
		case protowire.VarintType:
			var x uint64
			x, n = protowire.ConsumeVarint(b)
			v = protowire.AppendVarint(nil, x)
		default:
			t.Fatalf("unexpected wire type %d", typ)
		}
		if n < 0 {
			t.Fatal("bad field")
		}
		out[num] = append(out[num], v)
		b = b[n:]
	}
	return out
}

func TestRecipientKeyVectors(t *testing.T) {
	var v struct {
		Root    string `json:"root_cert_der_b64"`
		Vectors []struct {
			Name          string `json:"name"`
			Expect        string `json:"expect"`
			Organization  string `json:"organization_id"`
			Principal     string `json:"principal"`
			RecipientKey  string `json:"recipient_key_b64"`
			ExpectedKeyID string `json:"expected_key_id"`
		} `json:"recipient_key_vectors"`
	}
	load(t, "device-signed-request-vectors-v1.json", &v)
	root, err := x509.ParseCertificate(unb64(t, v.Root))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Vectors) != 6 {
		t.Fatalf("want 6 recipient key vectors, got %d", len(v.Vectors))
	}
	for _, rv := range v.Vectors {
		f := fields(t, unb64(t, rv.RecipientKey))
		var reg cloudpbv2.SignedRequest
		if len(f[2]) != 1 || proto.Unmarshal(f[2][0], &reg) != nil {
			t.Fatalf("%s: registration", rv.Name)
		}
		payload, err := VerifyRegistration(&reg, f[3], root, rv.Principal, rv.Organization)
		if err == nil {
			// The caller's remaining checks: organization_id and key_id.
			p := fields(t, payload)
			if len(p[1]) != 1 || string(p[1][0]) != rv.Organization || len(p[3]) != 1 {
				err = errors.New("payload does not bind the organization")
			} else if rv.Expect == "valid" && KeyID(p[3][0]) != rv.ExpectedKeyID {
				t.Fatalf("%s: key_id %s", rv.Name, KeyID(p[3][0]))
			}
		}
		if (err == nil) != (rv.Expect == "valid") {
			t.Errorf("%s: expect %s, got %v", rv.Name, rv.Expect, err)
		}
	}
}

func TestDeviceRegistrationVector(t *testing.T) {
	var v struct {
		Root     string `json:"root_cert_der_b64"`
		DeviceCA string `json:"device_ca_cert_der_b64"`
		Vectors  []struct {
			Name          string `json:"name"`
			Tenant        string `json:"expected_tenant"`
			SAN           string `json:"spiffe_san"`
			SignedRequest string `json:"signed_request_b64"`
		} `json:"vectors"`
	}
	load(t, "device-signed-request-vectors-v1.json", &v)
	root, _ := x509.ParseCertificate(unb64(t, v.Root))
	for _, dv := range v.Vectors {
		if dv.Name != "mldsa65-register-device-encryption-key-valid" {
			continue
		}
		var reg cloudpbv2.SignedRequest
		if err := proto.Unmarshal(unb64(t, dv.SignedRequest), &reg); err != nil {
			t.Fatal(err)
		}
		chain := [][]byte{unb64(t, v.DeviceCA)}
		if _, err := VerifyRegistration(&reg, chain, root, dv.SAN, dv.Tenant); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyRegistration(&reg, chain, root, dv.SAN, "00000000-0000-4000-8000-000000000000"); err == nil {
			t.Fatal("registration verified for another organization")
		}
		return
	}
	t.Fatal("device registration vector missing")
}
