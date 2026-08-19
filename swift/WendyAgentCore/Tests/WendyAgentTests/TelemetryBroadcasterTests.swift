import OpenTelemetryGRPC
import Testing

@testable import WendyAgentCore

@Suite("Telemetry broadcaster")
struct TelemetryBroadcasterTests {
    @Test("log subscriptions separate recent history from live records")
    func subscriptionsSeparateRecentHistoryFromLiveRecords() async {
        let broadcaster = TelemetryBroadcaster()
        await broadcaster.broadcastLogs(logRequest(marker: "stale"))

        let (_, recent, stream) = await broadcaster.subscribeLogs()
        await broadcaster.broadcastLogs(logRequest(marker: "live"))

        #expect(recent.first?.resourceLogs.first?.schemaURL == "stale")
        var iterator = stream.makeAsyncIterator()
        let first = await iterator.next()
        #expect(first?.resourceLogs.first?.schemaURL == "live")
    }

    private func logRequest(
        marker: String
    ) -> Opentelemetry_Proto_Collector_Logs_V1_ExportLogsServiceRequest {
        var resourceLogs = Opentelemetry_Proto_Logs_V1_ResourceLogs()
        resourceLogs.schemaURL = marker
        var request = Opentelemetry_Proto_Collector_Logs_V1_ExportLogsServiceRequest()
        request.resourceLogs = [resourceLogs]
        return request
    }
}
