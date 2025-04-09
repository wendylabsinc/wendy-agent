import Foundation

extension ContainerImageSpec {
    /// Represents a layer in a container image.
    public struct Layer {
        /// The files to include in this layer.
        public var files: [File]

        /// Creates a new container layer with the specified files.
        ///
        /// - Parameter files: The files to include in this layer.
        public init(files: [File]) {
            self.files = files
        }
    }
}
