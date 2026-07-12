import CoreWLAN
import Foundation
import SecurityFoundation

/// Best-effort Wi-Fi security classification, mirroring the proto enum.
enum WiFiSecurity: Sendable, Equatable {
    case unspecified
    case open
    case wep
    case wpaPersonal
    case wpa2Personal
    case wpa3Personal
    case wpa2Enterprise
}

struct WiFiStatus: Sendable, Equatable {
    var connected: Bool
    var ssid: String?
    var errorMessage: String?
}

struct WiFiScanResult: Sendable, Equatable {
    var ssid: String
    var rssiDbm: Int
    var signalStrength: Int32
    var security: WiFiSecurity
    var isKnown: Bool
    var isConnected: Bool
}

struct KnownWiFi: Sendable, Equatable {
    var ssid: String
    var priority: Int32
    var security: WiFiSecurity
}

enum WiFiError: Error, CustomStringConvertible {
    case noInterface
    case scanFailed(String)

    var description: String {
        switch self {
        case .noInterface:
            return "No Wi-Fi interface found."
        case .scanFailed(let message):
            return "Wi-Fi scan failed (Location access may be required): \(message)"
        }
    }
}

struct WiFiActionResult: Sendable, Equatable {
    var success: Bool
    var errorMessage: String?

    static let ok = WiFiActionResult(success: true, errorMessage: nil)
    static func failed(_ message: String) -> WiFiActionResult {
        WiFiActionResult(success: false, errorMessage: message)
    }
}

/// Manages Wi-Fi on the host.
protocol WiFiManaging: Sendable {
    func status() async -> WiFiStatus
    func scan() async throws -> [WiFiScanResult]
    func knownNetworks() async -> [KnownWiFi]
    func connect(
        ssid: String,
        password: String,
        security: WiFiSecurity?,
        hidden: Bool
    ) async
        -> WiFiActionResult
    func disconnect() async -> WiFiActionResult
    func forget(ssid: String) async -> WiFiActionResult
    func setPriority(ssid: String, priority: Int32) async -> WiFiActionResult
    func reorder(ssids: [String]) async -> WiFiActionResult
}

/// Live CoreWLAN-backed implementation.
///
/// Scanning and reading the current SSID require Location authorization on
/// macOS 14+. Mutating the saved-network configuration (forget / priority /
/// reorder) requires administrator rights via `commitConfiguration`; a
/// non-privileged agent surfaces the failure in `WiFiActionResult.errorMessage`.
struct WiFiController: WiFiManaging {
    private var interface: CWInterface? {
        CWWiFiClient.shared().interface()
    }

    // Every CoreWLAN call below is synchronous and blocking — `scanForNetworks`
    // in particular blocks for seconds. They run on `BlockingExecutor` so they
    // never occupy a gRPC handler's cooperative thread. `self` is trivially
    // Sendable (no stored state) and only `Sendable` value types cross back out.

    func status() async -> WiFiStatus {
        await BlockingExecutor.run {
            guard let interface = self.interface else {
                return WiFiStatus(
                    connected: false,
                    ssid: nil,
                    errorMessage: "No Wi-Fi interface found."
                )
            }
            let ssid = interface.ssid()
            return WiFiStatus(connected: ssid != nil, ssid: ssid, errorMessage: nil)
        }
    }

    func scan() async throws -> [WiFiScanResult] {
        try await BlockingExecutor.run {
            guard let interface = self.interface else {
                throw WiFiError.noInterface
            }
            let currentSSID = interface.ssid()
            let known = Set(self.knownProfiles(interface).compactMap { $0.ssid })

            let networks: Set<CWNetwork>
            do {
                networks = try interface.scanForNetworks(withSSID: nil)
            } catch {
                throw WiFiError.scanFailed(error.localizedDescription)
            }

            // Deduplicate by SSID, keeping the strongest signal.
            var strongest: [String: CWNetwork] = [:]
            for network in networks {
                guard let ssid = network.ssid, !ssid.isEmpty else { continue }
                if let existing = strongest[ssid], existing.rssiValue >= network.rssiValue {
                    continue
                }
                strongest[ssid] = network
            }

            let results = strongest.values.map { network -> WiFiScanResult in
                let ssid = network.ssid ?? ""
                return WiFiScanResult(
                    ssid: ssid,
                    rssiDbm: network.rssiValue,
                    signalStrength: Self.rssiToSignalStrength(network.rssiValue),
                    security: Self.security(of: network),
                    isKnown: known.contains(ssid),
                    isConnected: ssid == currentSSID
                )
            }
            return results.sorted { $0.rssiDbm > $1.rssiDbm }
        }
    }

    func knownNetworks() async -> [KnownWiFi] {
        await BlockingExecutor.run {
            guard let interface = self.interface else { return [] }
            let profiles = self.knownProfiles(interface)
            return profiles.enumerated().compactMap { index, profile in
                guard let ssid = profile.ssid else { return nil }
                return KnownWiFi(
                    ssid: ssid,
                    priority: Int32(clamping: profiles.count - index),
                    security: Self.security(of: profile.security)
                )
            }
        }
    }

    func connect(
        ssid: String,
        password: String,
        security: WiFiSecurity?,
        hidden: Bool
    ) async
        -> WiFiActionResult
    {
        await BlockingExecutor.run {
            guard let interface = self.interface else {
                return .failed("No Wi-Fi interface found.")
            }
            do {
                let networks = try interface.scanForNetworks(withSSID: nil)
                guard let target = networks.first(where: { $0.ssid == ssid }) else {
                    return .failed("Network \"\(ssid)\" not found in scan results.")
                }
                try interface.associate(to: target, password: password.isEmpty ? nil : password)
                return .ok
            } catch {
                return .failed("Failed to connect to \"\(ssid)\": \(error.localizedDescription)")
            }
        }
    }

    func disconnect() async -> WiFiActionResult {
        await BlockingExecutor.run {
            guard let interface = self.interface else {
                return .failed("No Wi-Fi interface found.")
            }
            interface.disassociate()
            return .ok
        }
    }

    func forget(ssid: String) async -> WiFiActionResult {
        await BlockingExecutor.run {
            self.mutateConfiguration { profiles in
                profiles.filter { $0.ssid != ssid }
            }
        }
    }

    func setPriority(ssid: String, priority: Int32) async -> WiFiActionResult {
        // macOS represents Wi-Fi priority by preferred-network order (index 0 =
        // highest). We move the target network to the front for any positive
        // priority; exact integer priorities are not representable on macOS.
        await BlockingExecutor.run {
            self.mutateConfiguration { profiles in
                guard let target = profiles.first(where: { $0.ssid == ssid }) else {
                    return profiles
                }
                return [target] + profiles.filter { $0.ssid != ssid }
            }
        }
    }

    func reorder(ssids: [String]) async -> WiFiActionResult {
        await BlockingExecutor.run {
            self.mutateConfiguration { profiles in
                var ordered: [CWNetworkProfile] = []
                for ssid in ssids {
                    if let match = profiles.first(where: { $0.ssid == ssid }) {
                        ordered.append(match)
                    }
                }
                // Append the untouched remainder in original order.
                for profile in profiles where !ssids.contains(profile.ssid ?? "") {
                    ordered.append(profile)
                }
                return ordered
            }
        }
    }

    // MARK: - Helpers

    private func knownProfiles(_ interface: CWInterface) -> [CWNetworkProfile] {
        guard let configuration = interface.configuration() else { return [] }
        return configuration.networkProfiles.compactMap { $0 as? CWNetworkProfile }
    }

    /// Applies a transform to the saved-network profile list and commits it.
    /// Commit requires administrator authorization; failures are returned as
    /// honest error results.
    private func mutateConfiguration(
        _ transform: ([CWNetworkProfile]) -> [CWNetworkProfile]
    ) -> WiFiActionResult {
        guard let interface else { return .failed("No Wi-Fi interface found.") }
        let configuration = CWMutableConfiguration(
            configuration: interface.configuration() ?? CWConfiguration()
        )
        let current = configuration.networkProfiles.compactMap { $0 as? CWNetworkProfile }
        configuration.networkProfiles = NSOrderedSet(array: transform(current))

        let authorization = SFAuthorization.authorization() as? SFAuthorization
        do {
            try interface.commitConfiguration(configuration, authorization: authorization)
            return .ok
        } catch {
            return .failed(
                "Failed to update saved Wi-Fi networks (requires administrator privileges): "
                    + error.localizedDescription
            )
        }
    }

    // MARK: - Pure mapping (testable)

    /// Maps RSSI in dBm to a 0–100 signal-strength percentage using a linear
    /// mapping over the usable range (-100 dBm … -30 dBm).
    static func rssiToSignalStrength(_ dbm: Int) -> Int32 {
        let clamped = max(-100, min(-30, dbm))
        let percent = Double(clamped + 100) * 100.0 / 70.0
        return Int32((percent).rounded())
    }

    static func security(of network: CWNetwork) -> WiFiSecurity {
        if network.supportsSecurity(.wpa3Personal) { return .wpa3Personal }
        if network.supportsSecurity(.wpa2Enterprise) || network.supportsSecurity(.enterprise) {
            return .wpa2Enterprise
        }
        if network.supportsSecurity(.wpa2Personal) { return .wpa2Personal }
        if network.supportsSecurity(.wpaPersonal) { return .wpaPersonal }
        if network.supportsSecurity(.WEP) { return .wep }
        if network.supportsSecurity(.none) { return .open }
        return .unspecified
    }

    static func security(of cwSecurity: CWSecurity) -> WiFiSecurity {
        switch cwSecurity {
        case .none: return .open
        case .WEP, .dynamicWEP: return .wep
        case .wpaPersonal, .wpaPersonalMixed: return .wpaPersonal
        case .wpa2Personal, .personal: return .wpa2Personal
        case .wpa3Personal, .wpa3Transition: return .wpa3Personal
        case .wpaEnterprise, .wpaEnterpriseMixed, .wpa2Enterprise, .wpa3Enterprise, .enterprise:
            return .wpa2Enterprise
        default: return .unspecified
        }
    }
}
