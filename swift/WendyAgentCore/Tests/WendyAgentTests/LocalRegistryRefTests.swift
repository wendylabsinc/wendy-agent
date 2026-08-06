import Testing

@testable import WendyAgentCore

@Suite("LocalRegistryRef")
struct LocalRegistryRefTests {
    @Test("device-local push-port refs are rewritten to the pull listener")
    func rewritesLocalPushRefs() {
        #expect(
            LocalRegistryRef.rewriteForLocalPull("localhost:5555/app:latest")
                == "127.0.0.1:5556/app:latest")
        #expect(
            LocalRegistryRef.rewriteForLocalPull("127.0.0.1:5555/app:latest")
                == "127.0.0.1:5556/app:latest")
        #expect(
            LocalRegistryRef.rewriteForLocalPull("[::1]:5555/app:latest")
                == "127.0.0.1:5556/app:latest")
    }

    @Test("tags and digest suffixes after the authority are preserved")
    func preservesSuffixes() {
        #expect(
            LocalRegistryRef.rewriteForLocalPull("localhost:5555/app@sha256:abc123")
                == "127.0.0.1:5556/app@sha256:abc123")
        #expect(
            LocalRegistryRef.rewriteForLocalPull("localhost:5555/nested/repo:v1")
                == "127.0.0.1:5556/nested/repo:v1")
    }

    @Test("non-local references pass through unchanged")
    func passesThroughOtherRefs() {
        for ref in [
            "ghcr.io/wendylabsinc/app:latest",
            "otherhost:5555/app:latest",
            "localhost:5556/app:latest",
            "localhost:5000/app:latest",
            "docker.io/library/python:3.11-slim",
            "sha256:0123456789abcdef",
            "my-binary",
            "",
        ] {
            #expect(LocalRegistryRef.rewriteForLocalPull(ref) == ref)
        }
    }
}
