import Foundation
import Security

/// Minimal wrapper over the macOS Keychain (`kSecClassGenericPassword`) for
/// storing opaque secret blobs. Deliberately key-agnostic so future secrets
/// (e.g. an ML-KEM private key that cannot be Secure-Enclave-backed) reuse it.
struct KeychainStore {
    let service: String

    enum KeychainError: Error, CustomStringConvertible {
        case unexpectedStatus(OSStatus)

        var description: String {
            switch self {
            case .unexpectedStatus(let s): return "Keychain error: OSStatus \(s)"
            }
        }
    }

    init(service: String = "sh.wendy.agent") {
        self.service = service
    }

    private func baseQuery(account: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: self.service,
            kSecAttrAccount as String: account,
        ]
    }

    func set(_ data: Data, account: String) throws {
        // Upsert: delete any existing item, then add with the fixed accessibility.
        try self.remove(account: account)
        var attrs = self.baseQuery(account: account)
        attrs[kSecValueData as String] = data
        attrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(attrs as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError.unexpectedStatus(status) }
    }

    func get(account: String) throws -> Data? {
        var query = self.baseQuery(account: account)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &out)
        switch status {
        case errSecSuccess: return out as? Data
        case errSecItemNotFound: return nil
        default: throw KeychainError.unexpectedStatus(status)
        }
    }

    func remove(account: String) throws {
        let status = SecItemDelete(self.baseQuery(account: account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainError.unexpectedStatus(status)
        }
    }
}
