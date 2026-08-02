// Verify the bluetooth entitlement grants a usable system bus.
//
// The entitlement's only effect inside the container is the filtered D-Bus
// socket that applyBluetooth mounts, plus DBUS_SYSTEM_BUS_ADDRESS. Neither
// /sys/class/bluetooth (visible in every container, entitled or not) nor a raw
// HCI socket (refused either way) says anything about it, so this connects to
// the socket to prove a proxy is listening on the other end. python-bluetooth
// carries the deeper assertion that the bus is scoped to org.bluez.
import Foundation

#if canImport(Glibc)
import Glibc
#elseif canImport(Darwin)
import Darwin
#endif

let defaultSocket = "/var/run/dbus/system_bus_socket"

var failures: [String] = []

let address = ProcessInfo.processInfo.environment["DBUS_SYSTEM_BUS_ADDRESS"]
print("DBUS_SYSTEM_BUS_ADDRESS: \(address ?? "(unset)")")
if address == nil {
    failures.append("DBUS_SYSTEM_BUS_ADDRESS is unset; the entitlement was not applied")
}

let socketPath: String
if let address, let path = address.components(separatedBy: "unix:path=").last, address.contains("unix:path=") {
    socketPath = path
} else {
    socketPath = defaultSocket
}

var info = stat()
if stat(socketPath, &info) != 0 {
    failures.append("\(socketPath) does not exist; the entitlement mounted no filtered D-Bus proxy socket")
} else if info.st_mode & S_IFMT != S_IFSOCK {
    failures.append("\(socketPath) exists but is not a socket")
} else {
    // connect() succeeds only if the proxy is bound and accepting.
    #if canImport(Glibc)
    let streamType = Int32(SOCK_STREAM.rawValue)
    #else
    let streamType = SOCK_STREAM
    #endif
    let fd = socket(AF_UNIX, streamType, 0)
    if fd < 0 {
        failures.append("could not create a unix socket (errno \(errno))")
    } else {
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let capacity = MemoryLayout.size(ofValue: addr.sun_path)
        if socketPath.utf8.count >= capacity {
            failures.append("\(socketPath) is too long for sockaddr_un")
        } else {
            withUnsafeMutablePointer(to: &addr.sun_path) { dst in
                socketPath.withCString { src in
                    _ = strncpy(UnsafeMutableRawPointer(dst).assumingMemoryBound(to: CChar.self), src, capacity - 1)
                }
            }
            let rc = withUnsafePointer(to: &addr) { ptr in
                ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                    connect(fd, sa, socklen_t(MemoryLayout<sockaddr_un>.size))
                }
            }
            if rc == 0 {
                print("OK  connected to the filtered D-Bus proxy at \(socketPath)")
            } else {
                failures.append("could not connect to \(socketPath): nothing is listening (errno \(errno))")
            }
        }
        close(fd)
    }
}

if !failures.isEmpty {
    print("\nFAIL: bluetooth entitlement did not grant a usable system bus:")
    for failure in failures {
        print("  - \(failure)")
    }
    exit(1)
}

print("\nPASS: Bluetooth entitlement grants a connectable filtered D-Bus proxy")
