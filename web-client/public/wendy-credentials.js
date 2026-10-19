/* Origin-scoped credentials. This script runs only in the WASM worker. */
self.wendyCredentials = (() => {
  let opening;
  function open() {
    if (!opening)
      opening = new Promise((resolve, reject) => {
        const request = indexedDB.open("wendy-client", 1);
        request.onupgradeneeded = () =>
          request.result.createObjectStore("credentials");
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => {
          opening = undefined;
          reject(request.error);
        };
        request.onblocked = () => {
          opening = undefined;
          reject(new Error("Browser storage is blocked"));
        };
      });
    return opening;
  }
  async function transaction(mode, operation) {
    const db = await open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction("credentials", mode);
      const request = operation(tx.objectStore("credentials"));
      tx.oncomplete = () => resolve(request.result ?? "");
      tx.onerror = () => reject(tx.error);
      tx.onabort = () =>
        reject(tx.error || new Error("Credential storage was interrupted"));
    });
  }
  const read = () => transaction("readonly", (store) => store.get("session"));
  // WDY-3920, as the console does: the operator key's ML-DSA seed is
  // AES-GCM-encrypted under a NON-extractable WebCrypto key stored next to it
  // as a CryptoKey. Page script cannot export that key, but script running in
  // this origin can still call decrypt, so this protects the seed at rest
  // (readable records, disk), not against XSS. aad is the tenant and operator.
  // A record that is a string comes from before this and holds the key in
  // plaintext; the worker rewraps it on its first restore.
  return {
    load: async () => {
      const record = await read();
      return typeof record === "string" ? record : (record?.session ?? "");
    },
    loadKey: async (aad) => {
      const record = await read();
      if (typeof record?.session !== "string")
        throw new Error("No wrapped operator key");
      return new Uint8Array(
        await crypto.subtle.decrypt(
          {
            name: "AES-GCM",
            iv: record.iv,
            additionalData: new TextEncoder().encode(aad),
          },
          record.wrappingKey,
          record.wrappedSeed,
        ),
      );
    },
    saveWrapped: async (session, seed, aad) => {
      const wrappingKey = await crypto.subtle.generateKey(
        { name: "AES-GCM", length: 256 },
        false,
        ["encrypt", "decrypt"],
      );
      const iv = crypto.getRandomValues(new Uint8Array(12));
      const wrappedSeed = await crypto.subtle.encrypt(
        { name: "AES-GCM", iv, additionalData: new TextEncoder().encode(aad) },
        wrappingKey,
        seed,
      );
      return transaction("readwrite", (store) =>
        store.put({ session, wrappingKey, iv, wrappedSeed }, "session"),
      );
    },
    clear: () => transaction("readwrite", (store) => store.delete("session")),
  };
})();
