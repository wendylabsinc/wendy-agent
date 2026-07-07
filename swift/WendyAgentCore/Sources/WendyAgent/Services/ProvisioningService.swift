import GRPCCore
import WendyAgentGRPC

struct ProvisioningService: Wendy_Agent_Services_V1_WendyProvisioningService.SimpleServiceProtocol {
    func startProvisioning(
        request: Wendy_Agent_Services_V1_StartProvisioningRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_StartProvisioningResponse {
        Wendy_Agent_Services_V1_StartProvisioningResponse()
    }

    func isProvisioned(
        request: Wendy_Agent_Services_V1_IsProvisionedRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_IsProvisionedResponse {
        var response = Wendy_Agent_Services_V1_IsProvisionedResponse()
        response.notProvisioned = Wendy_Agent_Services_V1_NotProvisionedResponse()
        return response
    }

    func unprovision(
        request: Wendy_Agent_Services_V1_UnprovisionRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_UnprovisionResponse {
        throw RPCError(
            code: .unimplemented,
            message: "Unprovisioning is currently not supported by Wendy Agent for Mac."
        )
    }
}
