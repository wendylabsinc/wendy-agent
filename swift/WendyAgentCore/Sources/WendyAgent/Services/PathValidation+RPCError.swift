public import GRPCCore

/// Surfaces `PathValidationError` as a `.invalidArgument` RPC status instead of
/// grpc-swift-2's default `.unknown` fallback for opaque `Error` types.
///
/// Kept in a separate file so `PathValidation.swift` (the actual path-containment
/// check) stays a pure, Foundation-only helper with no gRPC dependency.
extension PathValidationError: RPCErrorConvertible {
    public var rpcErrorCode: RPCError.Code { .invalidArgument }

    public var rpcErrorMessage: String {
        switch self {
        case .unsafePath(let path): return "unsafe path rejected: \(path)"
        }
    }
}
