import Foundation
import Testing

@testable import WendyAgentCore

@Suite("SystemStats.parseLsofListen")
struct SystemStatsParseTests {
    @Test("parses an IPv4 TCP listener with wildcard address")
    func parsesIPv4Wildcard() {
        let output = """
            COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
            node    12345 joan   20u  IPv4 0x1234      0t0  TCP *:8080 (LISTEN)
            """
        let ports = SystemStats.parseLsofListen(output)
        #expect(ports == [PortSample(proto: "tcp", port: 8080, address: "0.0.0.0")])
    }

    @Test("parses an IPv6 TCP listener with explicit address")
    func parsesIPv6() {
        let output = """
            server  222 joan   7u  IPv6 0xabcd      0t0  TCP [::1]:5000 (LISTEN)
            """
        let ports = SystemStats.parseLsofListen(output)
        #expect(ports == [PortSample(proto: "tcp6", port: 5000, address: "::1")])
    }

    @Test("parses a bound IPv4 address")
    func parsesBoundIPv4() {
        let output = """
            svc  9 joan   3u  IPv4 0x1      0t0  TCP 127.0.0.1:3000 (LISTEN)
            """
        let ports = SystemStats.parseLsofListen(output)
        #expect(ports == [PortSample(proto: "tcp", port: 3000, address: "127.0.0.1")])
    }

    @Test("ignores non-listen lines and headers")
    func ignoresNoise() {
        let output = """
            COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
            node    12345 joan   30u  IPv4 0x9      0t0  TCP 1.2.3.4:80->5.6.7.8:443 (ESTABLISHED)
            """
        #expect(SystemStats.parseLsofListen(output).isEmpty)
    }
}

@Suite("SystemStats live sampling")
struct SystemStatsLiveTests {
    @Test("host stats report positive totals")
    func hostStats() {
        let sample = SystemStats.hostStats()
        #expect(sample.cpuCount >= 1)
        #expect(sample.memTotalBytes > 0)
        #expect(sample.cpuTotalTicks > 0)
        #expect(sample.cpuTotalTicks >= sample.cpuIdleTicks)
    }

    @Test("process stats for the current process are non-nil")
    func processStats() {
        let pid = ProcessInfo.processInfo.processIdentifier
        let sample = SystemStats.processStats(pid: pid)
        #expect(sample != nil)
        #expect((sample?.memoryBytes ?? 0) > 0)
    }

    @Test("invalid pid yields nil")
    func invalidPid() {
        #expect(SystemStats.processStats(pid: -1) == nil)
    }
}
