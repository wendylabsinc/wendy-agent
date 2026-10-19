//go:build js && wasm

package main

import (
	"bytes"
	"os"
	"strings"
	"syscall/js"
	"testing"
)

// The real worker script against Node's WebCrypto and a Map-backed IndexedDB
// that structured-clones what it stores, the way the browser does.
func loadCredentialScript(t *testing.T) js.Value {
	t.Helper()
	src, err := os.ReadFile("../../../web-client/public/wendy-credentials.js")
	if err != nil {
		t.Fatal(err)
	}
	js.Global().Call("eval", `
globalThis.self = globalThis;
globalThis.fakeIDB = new Map();
globalThis.indexedDB = { open() {
  const request = {};
  setTimeout(() => {
    const store = {
      get: (k) => ({ result: structuredClone(fakeIDB.get(k)) }),
      put: (v, k) => { fakeIDB.set(k, structuredClone(v)); return { result: k }; },
      delete: (k) => { fakeIDB.delete(k); return { result: undefined }; },
    };
    request.result = { transaction() { const tx = { objectStore: () => store }; setTimeout(() => tx.oncomplete()); return tx; } };
    request.onsuccess();
  });
  return request;
} };`)
	js.Global().Call("eval", string(src))
	return js.Global().Get("fakeIDB")
}

func TestCredentialStoreWrapsTheSeed(t *testing.T) {
	idb := loadCredentialScript(t)
	st := browserCredentialStore{}
	seed := bytes.Repeat([]byte{0x5a}, 32)
	const aad = "operator-key:tenant:operator"
	if err := st.SaveWrapped([]byte(`{"version":2}`), seed, aad); err != nil {
		t.Fatal(err)
	}
	record := idb.Call("get", "session")
	key := record.Get("wrappingKey")
	if record.Get("session").String() != `{"version":2}` || key.Get("extractable").Bool() || key.Get("algorithm").Get("name").String() != "AES-GCM" || record.Get("iv").Length() != 12 {
		t.Fatal("record is not a session plus a non-extractable AES-GCM wrapped seed")
	}
	wrapped := make([]byte, record.Get("wrappedSeed").Get("byteLength").Int())
	js.CopyBytesToGo(wrapped, js.Global().Get("Uint8Array").New(record.Get("wrappedSeed")))
	if bytes.Contains(wrapped, seed[:16]) {
		t.Fatal("seed stored in plaintext")
	}
	if got, err := st.LoadKey(aad); err != nil || !bytes.Equal(got, seed) {
		t.Fatalf("unwrap: %x, %v", got, err)
	}
	if _, err := st.LoadKey("operator-key:tenant:another"); err == nil {
		t.Fatal("seed unwrapped for another operator")
	}
	if raw, err := st.Load(); err != nil || string(raw) != `{"version":2}` {
		t.Fatalf("load: %q, %v", raw, err)
	}
	if err := st.Save([]byte(`{"privateKey":"x"}`)); err == nil {
		t.Fatal("saved a session that carries its key")
	}
	if err := st.Clear(); err != nil || idb.Call("has", "session").Bool() {
		t.Fatal("sign-out kept the record")
	}
}

func TestCredentialStoreReplacesAPlaintextRecord(t *testing.T) {
	idb := loadCredentialScript(t)
	st := browserCredentialStore{}
	legacy := `{"version":1,"privateKey":"-----BEGIN PRIVATE KEY-----\ntest-only\n-----END PRIVATE KEY-----\n"}`
	idb.Call("set", "session", legacy)
	if raw, err := st.Load(); err != nil || string(raw) != legacy {
		t.Fatalf("legacy record not readable for migration: %q, %v", raw, err)
	}
	if _, err := st.LoadKey("operator-key:tenant:operator"); err == nil {
		t.Fatal("a plaintext record has no wrapped key")
	}
	if err := st.SaveWrapped([]byte(`{"version":2}`), bytes.Repeat([]byte{1}, 32), "operator-key:tenant:operator"); err != nil {
		t.Fatal(err)
	}
	if record := idb.Call("get", "session"); record.Type() == js.TypeString || strings.Contains(record.Get("session").String(), "PRIVATE KEY") {
		t.Fatal("plaintext record survived the rewrap")
	}
}
