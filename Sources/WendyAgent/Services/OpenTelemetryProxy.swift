import OpenTelemetryGRPC

actor OpenTelemetryProxy: Opentelemetry_Proto_Collector_Logs_V1_LogsService.SimpleServiceProtocol {
    let cloud: CloudClient

    init(cloud: CloudClient) {
        self.cloud = cloud
    }
    
    func export(
        request: Opentelemetry_Proto_Collector_Logs_V1_ExportLogsServiceRequest,
        context: ServerContext
    ) async throws -> Opentelemetry_Proto_Collector_Logs_V1_ExportLogsServiceResponse {
        do {
            let otel = await Opentelemetry_Proto_Collector_Logs_V1_LogsService.Client(wrapping: cloud.grpcClient)
            return try await otel.export(request)
        } catch {
            print("Error exporting logs: \(error)")
            return Opentelemetry_Proto_Collector_Logs_V1_ExportLogsServiceResponse()
        }
    }
}