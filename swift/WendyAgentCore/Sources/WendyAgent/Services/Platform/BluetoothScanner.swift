import CoreBluetooth
import Foundation

struct DiscoveredPeripheral: Sendable, Equatable {
    var name: String
    var address: String
    var rssi: Int32
    var deviceType: String
    var paired: Bool
    var connected: Bool
    var trusted: Bool
}

struct BluetoothActionResult: Sendable, Equatable {
    var success: Bool
    var errorMessage: String?

    static let ok = BluetoothActionResult(success: true, errorMessage: nil)
    static func failed(_ message: String) -> BluetoothActionResult {
        BluetoothActionResult(success: false, errorMessage: message)
    }
}

/// Discovers and connects Bluetooth Low Energy peripherals.
///
/// CoreBluetooth is BLE-only and, unlike Linux BlueZ, does not expose classic
/// pairing/forget or hardware MAC addresses. Peripherals are addressed by their
/// per-host `CBPeripheral.identifier` UUID string.
protocol BluetoothManaging: Sendable {
    func scan() -> AsyncStream<DiscoveredPeripheral>
    func connect(address: String) async -> BluetoothActionResult
    func disconnect(address: String) async -> BluetoothActionResult
}

/// Live CoreBluetooth implementation. Cheap to construct; the `CBCentralManager`
/// is created lazily per scan/connect session so that merely instantiating the
/// service does not trigger a Bluetooth permission prompt.
struct BluetoothScanner: BluetoothManaging {
    func scan() -> AsyncStream<DiscoveredPeripheral> {
        AsyncStream { continuation in
            let session = BluetoothSession(mode: .scan(continuation))
            continuation.onTermination = { _ in session.stop() }
            session.start()
        }
    }

    func connect(address: String) async -> BluetoothActionResult {
        await connectionAction(address: address, connect: true)
    }

    func disconnect(address: String) async -> BluetoothActionResult {
        await connectionAction(address: address, connect: false)
    }

    private func connectionAction(address: String, connect: Bool) async -> BluetoothActionResult {
        guard let uuid = UUID(uuidString: address) else {
            return .failed("Invalid peripheral identifier \"\(address)\" (expected a UUID).")
        }
        let session = BluetoothSession(mode: connect ? .connect(uuid) : .disconnect(uuid))
        // CoreBluetooth's connect has no timeout of its own, so a peripheral that
        // never answers would hang the RPC forever. Bound it, and tear the
        // session down if the caller's task is cancelled.
        return await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                session.begin(timeout: .seconds(30)) { result in
                    continuation.resume(returning: result)
                }
            }
        } onCancel: {
            session.cancel()
        }
    }
}

/// Bridges `CBCentralManager` delegate callbacks into async consumers. Retains
/// itself for the lifetime of a connect/disconnect action; for scans it is held
/// by the stream's `onTermination` handler.
///
/// `@unchecked Sendable` safety invariant: all mutable state (`manager`,
/// `pendingPeripheral`, `selfRetain`, `hasFinished`, `finishedResult`,
/// `completion`) is read and written **only** on `queue`. The delegate callbacks
/// are delivered on `queue` (it is the `CBCentralManager` dispatch queue), and
/// `start`/`stop`/`begin`/`cancel` all hop onto `queue` before touching state,
/// so there is no cross-thread access to synchronize.
private final class BluetoothSession: NSObject, CBCentralManagerDelegate, @unchecked Sendable {
    enum Mode {
        case scan(AsyncStream<DiscoveredPeripheral>.Continuation)
        case connect(UUID)
        case disconnect(UUID)
    }

    private let mode: Mode
    private let queue = DispatchQueue(label: "sh.wendy.agent.bluetooth")
    private var manager: CBCentralManager?
    private var pendingPeripheral: CBPeripheral?
    private var selfRetain: BluetoothSession?

    // Connect/disconnect completion state — all touched only on `queue`.
    private var completion: (@Sendable (BluetoothActionResult) -> Void)?
    private var hasFinished = false
    private var finishedResult: BluetoothActionResult?

    init(mode: Mode) {
        self.mode = mode
        super.init()
    }

    /// Starts a scan session. Held alive by the stream's `onTermination`.
    func start() {
        queue.async { [self] in
            selfRetain = self
            manager = CBCentralManager(delegate: self, queue: queue)
        }
    }

    func stop() {
        queue.async { [self] in
            manager?.stopScan()
            manager = nil
            selfRetain = nil
        }
    }

    /// Starts a connect/disconnect action, delivering exactly one result to
    /// `completion` (on success, failure, timeout, or cancellation).
    func begin(
        timeout: Duration,
        completion: @escaping @Sendable (BluetoothActionResult) -> Void
    ) {
        queue.async { [self] in
            // A cancel may already have raced ahead of us.
            if hasFinished {
                completion(finishedResult ?? .failed("Bluetooth operation cancelled."))
                return
            }
            self.completion = completion
            selfRetain = self
            manager = CBCentralManager(delegate: self, queue: queue)

            let seconds =
                Double(timeout.components.seconds)
                + Double(timeout.components.attoseconds) / 1e18
            queue.asyncAfter(deadline: .now() + seconds) { [self] in
                finish(.failed("Bluetooth operation timed out."))
            }
        }
    }

    /// Cancels an in-flight connect/disconnect action.
    func cancel() {
        queue.async { [self] in
            finish(.failed("Bluetooth operation cancelled."))
        }
    }

    /// Delivers the result exactly once and releases the session. Must run on
    /// `queue`.
    private func finish(_ result: BluetoothActionResult) {
        guard !hasFinished else { return }
        hasFinished = true
        finishedResult = result

        if let pendingPeripheral, case .connect = mode {
            // Stop an outstanding connection attempt we're abandoning.
            manager?.cancelPeripheralConnection(pendingPeripheral)
        }
        manager = nil
        pendingPeripheral = nil
        selfRetain = nil

        let completion = self.completion
        self.completion = nil
        completion?(result)
    }

    // MARK: - CBCentralManagerDelegate

    func centralManagerDidUpdateState(_ central: CBCentralManager) {
        switch mode {
        case .scan(let continuation):
            if central.state == .poweredOn {
                central.scanForPeripherals(withServices: nil)
            } else if central.state != .unknown, central.state != .resetting {
                continuation.finish()
            }
        case .connect(let uuid):
            guard central.state == .poweredOn else {
                if central.state != .unknown, central.state != .resetting {
                    finish(
                        .failed("Bluetooth is not available (state: \(central.state.rawValue)).")
                    )
                }
                return
            }
            guard let peripheral = central.retrievePeripherals(withIdentifiers: [uuid]).first else {
                finish(.failed("Peripheral \(uuid.uuidString) not found."))
                return
            }
            pendingPeripheral = peripheral
            central.connect(peripheral)
        case .disconnect(let uuid):
            guard central.state == .poweredOn else {
                if central.state != .unknown, central.state != .resetting {
                    finish(
                        .failed("Bluetooth is not available (state: \(central.state.rawValue)).")
                    )
                }
                return
            }
            guard let peripheral = central.retrievePeripherals(withIdentifiers: [uuid]).first else {
                finish(.failed("Peripheral \(uuid.uuidString) not found."))
                return
            }
            pendingPeripheral = peripheral
            central.cancelPeripheralConnection(peripheral)
        }
    }

    func centralManager(
        _ central: CBCentralManager,
        didDiscover peripheral: CBPeripheral,
        advertisementData: [String: Any],
        rssi RSSI: NSNumber
    ) {
        guard case .scan(let continuation) = mode else { return }
        let name =
            (advertisementData[CBAdvertisementDataLocalNameKey] as? String)
            ?? peripheral.name ?? ""
        continuation.yield(
            DiscoveredPeripheral(
                name: name,
                address: peripheral.identifier.uuidString,
                rssi: Int32(clamping: RSSI.intValue),
                deviceType: "ble",
                paired: false,
                connected: peripheral.state == .connected,
                trusted: false
            )
        )
    }

    func centralManager(
        _ central: CBCentralManager,
        didConnect peripheral: CBPeripheral
    ) {
        if case .connect = mode {
            // Clear the pending peripheral so `finish` keeps the connection we
            // just established rather than cancelling it.
            pendingPeripheral = nil
            finish(.ok)
        }
    }

    func centralManager(
        _ central: CBCentralManager,
        didFailToConnect peripheral: CBPeripheral,
        error: (any Error)?
    ) {
        if case .connect = mode {
            finish(.failed("Failed to connect: \(error?.localizedDescription ?? "unknown error")"))
        }
    }

    func centralManager(
        _ central: CBCentralManager,
        didDisconnectPeripheral peripheral: CBPeripheral,
        error: (any Error)?
    ) {
        if case .disconnect = mode { finish(.ok) }
    }
}
