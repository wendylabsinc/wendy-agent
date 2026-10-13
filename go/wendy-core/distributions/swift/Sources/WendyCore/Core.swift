internal import CWendyCoreBridge
internal import Foundation
internal import Synchronization

private enum LoadingError: Error {
    case unsupportedTarget
    case missingLibrary
    case loadingFailed
}

private func loadLibrary() -> Result<OpaquePointer, LoadingError> {
    #if os(macOS)
    let platform = "darwin"
    let filename = "libWendyCore"
    let ext = "dylib"
    #elseif os(Linux)
    let platform = "linux"
    let filename = "libWendyCore"
    let ext = "so"
    #else
    return .failure(.unsupportedTarget)
    #endif
    #if os(macOS) || os(Linux)
    #if arch(arm64)
    let architecture = "arm64"
    #elseif arch(x86_64)
    let architecture = "x64"
    #else
    return .failure(.unsupportedTarget)
    #endif
    #if arch(arm64) || arch(x86_64)
    guard let url = Bundle.module.url(
        forResource: filename,
        withExtension: ext,
        subdirectory: "native/\(platform)-\(architecture)"
    ) else { return .failure(.missingLibrary) }
    guard let pointer = url.path.withCString({ WendyCoreBridgeOpen($0) }) else {
        return .failure(.loadingFailed)
    }
    return .success(pointer)
    #endif
    #endif
}

// A synchronized, once-initialized handle. The pointer never escapes and the
// native Go library remains loaded for the lifetime of this process.
private let library = Mutex(loadLibrary())

/// Returns 42 from Wendy Core. Throws only if the packaged native engine cannot load.
public func WendyHelloWorld() throws -> Int32 {
    try library.withLock { result in
        WendyCoreBridgeHelloWorld(try result.get())
    }
}
