import Foundation
import Testing

@testable import WendyAgentCore

@Suite("ProvisioningStore")
struct ProvisioningStoreTests {
    private func tempDir() -> URL {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("wendy-prov-\(UUID().uuidString)", isDirectory: true)
        return dir
    }

    @Test("save then load round-trips and never writes the key into provisioning.json")
    func roundTrip() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = ProvisioningStore(configPath: dir)

        try store.save(
            cloudHost: "cloud.example:50051",
            orgID: 7,
            assetID: 42,
            keyPEM: "KEYPEM",
            certPEM: "CERTPEM",
            chainPEM: "CHAINPEM"
        )

        let loaded = try #require(store.load())
        #expect(loaded.enrolled)
        #expect(loaded.cloudHost == "cloud.example:50051")
        #expect(loaded.orgID == 7)
        #expect(loaded.assetID == 42)
        #expect(loaded.keyPEM == "KEYPEM")
        #expect(loaded.certPEM == "CERTPEM")
        #expect(loaded.chainPEM == "CHAINPEM")

        // Key must NOT be in provisioning.json.
        let json = try String(
            contentsOf: dir.appendingPathComponent("provisioning.json"),
            encoding: .utf8
        )
        #expect(!json.contains("KEYPEM"))
        // Individual PEM files exist.
        #expect(
            FileManager.default.fileExists(
                atPath: dir.appendingPathComponent("device-key.pem").path
            )
        )
        #expect(
            FileManager.default.fileExists(atPath: dir.appendingPathComponent("device.pem").path)
        )
        #expect(FileManager.default.fileExists(atPath: dir.appendingPathComponent("ca.pem").path))
        #expect(
            FileManager.default.fileExists(atPath: dir.appendingPathComponent(".provisioned").path)
        )
    }

    @Test("legacy keyPem in provisioning.json migrates into device-key.pem")
    func legacyMigration() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let legacy = """
            {"enrolled":true,"cloudHost":"c:50051","orgId":1,"assetId":2,\
            "keyPem":"LEGACYKEY","certPem":"C","chainPem":"CH"}
            """
        try legacy.write(
            to: dir.appendingPathComponent("provisioning.json"),
            atomically: true,
            encoding: .utf8
        )

        let store = ProvisioningStore(configPath: dir)
        let loaded = try #require(store.load())
        #expect(loaded.keyPEM == "LEGACYKEY")
        // Migrated to device-key.pem and stripped from json.
        let migratedKey = try String(
            contentsOf: dir.appendingPathComponent("device-key.pem"),
            encoding: .utf8
        )
        #expect(migratedKey == "LEGACYKEY")
        let json = try String(
            contentsOf: dir.appendingPathComponent("provisioning.json"),
            encoding: .utf8
        )
        #expect(!json.contains("LEGACYKEY"))
    }

    @Test("save leaves the device unprovisioned if writing the key/cert files fails")
    func saveFailureDoesNotMarkEnrolled() throws {
        let dir = tempDir()
        let keyPath = dir.appendingPathComponent("device-key.pem")
        defer {
            // Restore write permissions so cleanup can actually remove everything.
            try? FileManager.default.setAttributes(
                [.posixPermissions: 0o700],
                ofItemAtPath: keyPath.path
            )
            try? FileManager.default.setAttributes(
                [.posixPermissions: 0o700],
                ofItemAtPath: dir.path
            )
            try? FileManager.default.removeItem(at: dir)
        }
        // The config directory itself stays writable: a provisioning.json
        // write would succeed here if `save` attempted it.
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)

        // Pre-create `device-key.pem` as a NON-EMPTY DIRECTORY and make *it*
        // (not the config dir) read-only. `writePEMFiles` writes the key
        // first via the `writeFile` helper, which does
        // `removeItem(at: device-key.pem)` before moving the new content
        // into place. Removing a directory's contents requires write
        // permission on that directory, not on the config dir containing it,
        // so this makes only the key write fail while leaving json writes
        // fully able to succeed -- the property that actually discriminates
        // PEM-first (correct) from json-first (buggy) save ordering. Making
        // the whole config dir read-only instead (the old approach) fails
        // *every* write unconditionally and does not discriminate.
        try FileManager.default.createDirectory(at: keyPath, withIntermediateDirectories: true)
        try "blocker".write(
            to: keyPath.appendingPathComponent("blocker"),
            atomically: true,
            encoding: .utf8
        )
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o500],
            ofItemAtPath: keyPath.path
        )

        let store = ProvisioningStore(configPath: dir)

        #expect(throws: (any Error).self) {
            try store.save(
                cloudHost: "c:50051",
                orgID: 1,
                assetID: 2,
                keyPEM: "KEYPEM",
                certPEM: "CERTPEM",
                chainPEM: "CHAINPEM"
            )
        }

        // The device must NOT be left looking enrolled: provisioning.json
        // (the commit marker) must not exist, and load() must return nil.
        #expect(
            !FileManager.default.fileExists(
                atPath: dir.appendingPathComponent("provisioning.json").path
            )
        )
        #expect(store.load() == nil)
    }

    @Test("clear removes every artifact and tolerates missing files")
    func clearRemovesAll() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = ProvisioningStore(configPath: dir)
        try store.save(
            cloudHost: "c:50051",
            orgID: 1,
            assetID: 2,
            keyPEM: "K",
            certPEM: "C",
            chainPEM: "CH"
        )

        try store.clear()

        #expect(store.load() == nil)
        for name in ["provisioning.json", "device-key.pem", "device.pem", "ca.pem", ".provisioned"]
        {
            #expect(!FileManager.default.fileExists(atPath: dir.appendingPathComponent(name).path))
        }
        // Second clear on an empty dir does not throw.
        try store.clear()
    }
}
