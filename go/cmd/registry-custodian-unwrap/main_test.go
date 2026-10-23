package main

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/sha3"
	"encoding/hex"
	"testing"
)

// Vector custodian-release-wrap-valid and its recipient key (hybrid_kem
// private_key_hex), service-protos conformance/registry/registry-vectors-v1.json.
const (
	vecPrivateKey = "7c18af3a49f494ebaf03735137125e5e3b3c98ddbb44d01f40524c41e8616f62"
	vecEnc        = "ab095d4e53482be63ef6784dd3a9f1c1ecd9c9f29825cd09c4f3fe5c3ee32ea48e43ff5c85833cac35b6bf41df046de1aa04afab40afd08ed9695f1a6826c2b80fd1de4c8185762725dbb411fd76f6cdfec650d5740105c1b2d54eef5e48ac1e3402966e75fe7b607b2ca5bd6e32acdc66bad38a5b82037cdaddae006918be4bdfde66fa0f5e6bb60e6a4498e9ea4458053a86f948411665a3c8ce5a195f579e08125f7002ac4451d7bec5f92e4c36704ee017ba4f1bc0f27740fa84bd0cf5d4e1d5e0c133ee134ea1d4f5011f5809edbafffe3ba2874b260ffb58b6bf5a707c5ce49ea360f8fc4333e6ce096f1498d520c2f3a263d34a02df7a5b9ad4e13c9b02a6b2f625bc85bb6dbf62b4b37646d554a3946e37940ff4e8fcd1c4e34be049bf5c2c0b40fcfc07e4e72f0540042bad16e86149c2a4a69998a68af6e7fde4a6268ab4e1d81aa172d70049f191c9ed28ca5038875094362538dce8260ea96be109aa551b3c092657faaffdf096856725fc2c5d88befcbb10972a01028537dedaed6b862035ff173773784fb7d1260a9d58726ef83360e3bb7f4d3bb28d9f4a2e02ae502d64b49069bda87e57cc8bfef2fc84580c67c30e4d909e37485afe7506e3697159137df2ee2a2dd48de23b974b80d5a7429df7a4d932b56478e360389386b13087827c64e99b8bfcfbed42fd465f9f2a9f298b734e24d000beae41b0031dcfa84b9fc052d6a5acf493007c9298f7d59188bd7ce33d18fb824ef3b79880b6177bd3cca8b7da48e7c24d2c0e48268575f9f6b270f57ff44818bce06d78a157fcb6148c0592262d6ac2e75b34f85d541943a7ab1e04569601d310ea7bb0c9ece6fa05830d10bd8a999a412b7ef8e59b0fe754e6e0532cc9ac6a9e22bd2dd5359856493c6495d9e7560a68f9db41190bd47a22c7ccdce63da26fbdadb2a7003394486f265321717a7169376c78f48d3e2842cdb1c4ed26325c5db6359ba50bfacb5c8cdbd1178c87438b0445c4a6255c24bc2e859e4d12f8e9d3ceb6ac2156214e60ac792097d5c7eeba534f29202d76fed7595ee5c9f36317ef89dfa4489ed01b54d2134f411d79ab713a9e369a349fdd89a6612a807941e5afbb16ca164d14d6fe2a8e95de3b75757d886cd73d1cdfc1cab007511578fa6949edd3b65652db418f73aa220261c1a5ba351a845039a6e2952d7b666384272b3013a10d9b5552e3425299de0f4f243c9ca979d5d26ba23f9a6d5216b059ea1138429f59edf60ace3a8ec0784eeae4f29c7b9810dba25376066df058787258ff36f2e4749019b86fcc94e2bcf1601f2890f98e2562b84753b238c91dac4eca3b6867e503a4239dc0da611fcd055122ba5d06e0755c0c305bf911e134e3344c68fc9ae319062399b6e9fa4b12c91beb610d18f6dddd85e45f93e4a88a642fe7f1a6f42623b43b413bd0215eff76a4b91899a4a9848c947795362af042dbef02d4e10e245b7181e6502ebcf61f4658cb681d5dbfe60d67172fa17c6ea866a7397e494ff4699c5afc45cb02d6e54e523f156004dff2d47525f1e8752b3f5769947dd8e11dc27275"
	vecCiphertext = "d79f6884356da23d633c1123fa5dca63bc10a4086fa220dfea5975807edfd6e1dd7f1dc9f99bda7ede324afade1ea581"
	vecWrappedKey = "d82ca60ac5250e6ece7b68f4dfe5f401704bbd0f8a7fe0acac6e0436791133c7"
)

func TestUnwrapCustodianVector(t *testing.T) {
	var w wrap
	w.OrganizationID = "2558fd76-afc7-466e-9613-6b715296a526"
	w.Scope.AppReleaseID = "9e1f3a5c-7b2d-4c6e-8f0a-1b3d5e7f9a2c"
	w.Scope.KeyID = "378273d51fdfcec6a29937f1df93e443a2cf2c80111b2ef7d3a356cb7387cf79"
	w.RecipientKeyID = "bd31a0c8ebc7539c1b361f379f52b59f2200c1332f850430ae1a2c535447f796"
	w.Enc, _ = hex.DecodeString(vecEnc)
	w.Ciphertext, _ = hex.DecodeString(vecCiphertext)

	// Local X-Wing decapsulation standing in for KMS (draft-connolly-cfrg-xwing-kem).
	seed, _ := hex.DecodeString(vecPrivateKey)
	expanded := sha3.SumSHAKE256(seed, mlkem.SeedSize+32)
	seedPQ, seedT := expanded[:mlkem.SeedSize], expanded[mlkem.SeedSize:]
	pq, err := mlkem.NewDecapsulationKey768(seedPQ)
	if err != nil {
		t.Fatal(err)
	}
	x, err := ecdh.X25519().NewPrivateKey(seedT)
	if err != nil {
		t.Fatal(err)
	}
	pub := append(pq.EncapsulationKey().Bytes(), x.PublicKey().Bytes()...)
	decap := func(enc []byte) ([]byte, error) {
		ctPQ, ctT := enc[:mlkem.CiphertextSize768], enc[mlkem.CiphertextSize768:]
		ssPQ, err := pq.Decapsulate(ctPQ)
		if err != nil {
			return nil, err
		}
		eph, err := ecdh.X25519().NewPublicKey(ctT)
		if err != nil {
			return nil, err
		}
		ssT, err := x.ECDH(eph)
		if err != nil {
			return nil, err
		}
		h := sha3.New256()
		for _, b := range [][]byte{ssPQ, ssT, ctT, x.PublicKey().Bytes(), []byte(`\./` + `/^\`)} {
			h.Write(b)
		}
		return h.Sum(nil), nil
	}

	key, err := unwrap(w, pub, decap)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(key); got != vecWrappedKey {
		t.Fatalf("key = %s, want %s", got, vecWrappedKey)
	}

	w.Ciphertext[0] ^= 1
	if _, err := unwrap(w, pub, decap); err == nil {
		t.Fatal("tampered wrap opened")
	}
	if _, err := unwrap(w, pub[1:], decap); err == nil {
		t.Fatal("wrap opened under another recipient key")
	}
}
