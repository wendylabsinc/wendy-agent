import Foundation

extension ContainerImageSpec {
    /// Represents a layer in a container image.
    public struct Layer {
        /// The content type of this layer
        public enum Content {
            /// A collection of individual files
            case files([File])

            /// A prebuilt layer tarball
            case tarball(URL, uncompressedSize: Int64)
        }

        /// The content of this layer
        public var content: Content

        /// The diffID (uncompressed digest) of this layer, if known
        public var diffID: String?

        /// Creates a new container layer with individual files.
        ///
        /// - Parameter files: The files to include in this layer.
        public init(files: [File], diffID: String? = nil) {
            self.content = .files(files)
            self.diffID = diffID
        }

        /// Creates a new container layer from a prebuilt tarball.
        ///
        /// - Parameter tarballURL: The URL of the tarball containing the layer.
        /// - Parameter diffID: The diffID (uncompressed digest) of this layer, if known.
        public init(tarball tarballURL: URL, uncompressedSize: Int64, diffID: String? = nil) {
            self.content = .tarball(tarballURL, uncompressedSize: uncompressedSize)
            self.diffID = diffID
        }

        /// The files in this layer if it's a files layer, otherwise empty.
        public var files: [File] {
            switch content {
            case .files(let files):
                return files
            case .tarball:
                return []
            }
        }
    }
}
