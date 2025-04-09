import Foundation

extension ContainerImageSpec.Layer {
    /// Represents a file to be included in a container layer.
    public struct File {
        /// The source URL of the file.
        public var source: URL

        /// The destination path of the file in the container.
        public var destination: String

        /// The permissions to set on the file.
        public var permissions: UInt16?

        /// Creates a new container file.
        ///
        /// - Parameters:
        ///   - source: The source URL of the file.
        ///   - destination: The destination path of the file in the container.
        ///   - permissions: The permissions to set on the file (optional).
        public init(source: URL, destination: String, permissions: UInt16? = nil) {
            self.source = source
            self.destination = destination
            self.permissions = permissions
        }
    }
}
