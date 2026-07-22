import Darwin
import Foundation

/// Host-level resource sample. CPU counters are cumulative ticks so the client
/// computes utilization from deltas between samples.
struct HostSample: Sendable {
    var cpuTotalTicks: UInt64
    var cpuIdleTicks: UInt64
    var cpuCount: UInt32
    var memTotalBytes: Int64
    var memAvailableBytes: Int64
}

/// Per-process resource sample.
struct ProcessSample: Sendable {
    /// cumulative user+system CPU time in nanoseconds
    var cpuUsageNanos: UInt64
    /// resident set size in bytes
    var memoryBytes: Int64
}

/// A listening socket owned by a process.
struct PortSample: Sendable, Equatable {
    var proto: String
    var port: UInt32
    var address: String
}

/// Host and per-process resource + port sampling for the macOS agent.
enum SystemStats {
    // MARK: - Host

    static func hostStats() -> HostSample {
        HostSample(
            cpuTotalTicks: cpuTicks().total,
            cpuIdleTicks: cpuTicks().idle,
            cpuCount: UInt32(clamping: ProcessInfo.processInfo.activeProcessorCount),
            memTotalBytes: Int64(clamping: ProcessInfo.processInfo.physicalMemory),
            memAvailableBytes: availableMemoryBytes()
        )
    }

    private static func cpuTicks() -> (total: UInt64, idle: UInt64) {
        var count = mach_msg_type_number_t(
            MemoryLayout<host_cpu_load_info_data_t>.size / MemoryLayout<integer_t>.size
        )
        var info = host_cpu_load_info()
        let result = withUnsafeMutablePointer(to: &info) { pointer in
            pointer.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
                host_statistics(mach_host_self(), HOST_CPU_LOAD_INFO, $0, &count)
            }
        }
        guard result == KERN_SUCCESS else { return (0, 0) }
        let user = UInt64(info.cpu_ticks.0)
        let system = UInt64(info.cpu_ticks.1)
        let idle = UInt64(info.cpu_ticks.2)
        let nice = UInt64(info.cpu_ticks.3)
        return (user + system + idle + nice, idle)
    }

    private static func availableMemoryBytes() -> Int64 {
        var pageSize: vm_size_t = 0
        host_page_size(mach_host_self(), &pageSize)

        var count = mach_msg_type_number_t(
            MemoryLayout<vm_statistics64_data_t>.size / MemoryLayout<integer_t>.size
        )
        var stats = vm_statistics64_data_t()
        let result = withUnsafeMutablePointer(to: &stats) { pointer in
            pointer.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
                host_statistics64(mach_host_self(), HOST_VM_INFO64, $0, &count)
            }
        }
        guard result == KERN_SUCCESS else { return 0 }
        let available = UInt64(stats.free_count) + UInt64(stats.inactive_count)
        return Int64(clamping: available * UInt64(pageSize))
    }

    // MARK: - Per-process

    static func processStats(pid: Int32) -> ProcessSample? {
        guard pid > 0 else { return nil }
        var usage = rusage_info_v2()
        let result = withUnsafeMutablePointer(to: &usage) { pointer -> Int32 in
            pointer.withMemoryRebound(to: rusage_info_t?.self, capacity: 1) {
                proc_pid_rusage(pid, RUSAGE_INFO_V2, $0)
            }
        }
        guard result == 0 else { return nil }
        return ProcessSample(
            cpuUsageNanos: usage.ri_user_time + usage.ri_system_time,
            memoryBytes: Int64(clamping: usage.ri_resident_size)
        )
    }

    // MARK: - Listening ports

    static func listeningPorts(pid: Int32) async -> [PortSample] {
        guard pid > 0 else { return [] }
        guard
            let result = try? await Subprocess.run(
                "/usr/sbin/lsof",
                ["-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-p", "\(pid)"],
                timeout: .seconds(10)
            )
        else {
            return []
        }
        return parseLsofListen(result.stdout)
    }

    /// Pure parser for `lsof -nP -iTCP -sTCP:LISTEN` output.
    static func parseLsofListen(_ output: String) -> [PortSample] {
        var samples: [PortSample] = []
        for rawLine in output.split(separator: "\n") {
            let columns = rawLine.split(separator: " ", omittingEmptySubsequences: true).map(
                String.init
            )
            guard columns.contains("(LISTEN)") else { continue }
            let isV6 = columns.contains("IPv6")
            guard let nodeIndex = columns.firstIndex(where: { $0 == "TCP" || $0 == "UDP" }),
                nodeIndex + 1 < columns.count
            else { continue }
            let proto = columns[nodeIndex].lowercased() + (isV6 ? "6" : "")
            guard let endpoint = splitAddressPort(columns[nodeIndex + 1], isV6: isV6) else {
                continue
            }
            samples.append(
                PortSample(proto: proto, port: endpoint.port, address: endpoint.address)
            )
        }
        return samples
    }

    private static func splitAddressPort(
        _ token: String,
        isV6: Bool
    ) -> (address: String, port: UInt32)? {
        guard let colonIndex = token.lastIndex(of: ":") else { return nil }
        let portString = token[token.index(after: colonIndex)...]
        guard let port = UInt32(portString) else { return nil }

        var address = String(token[..<colonIndex])
        address = address.trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        if address == "*" {
            address = isV6 ? "::" : "0.0.0.0"
        }
        return (address, port)
    }
}
