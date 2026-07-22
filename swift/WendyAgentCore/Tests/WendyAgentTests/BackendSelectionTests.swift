import Testing

@testable import WendyAgentCore

@Suite struct BackendSelectionTests {
    @Test func prefersAppleContainerWhenBothAvailable() {
        #expect(
            LinuxRuntimeSelector.choose(containerAvailable: true, dockerAvailable: true)
                == .appleContainer
        )
    }
    @Test func fallsBackToDocker() {
        #expect(
            LinuxRuntimeSelector.choose(containerAvailable: false, dockerAvailable: true) == .docker
        )
    }
    @Test func nilWhenNeitherAvailable() {
        #expect(
            LinuxRuntimeSelector.choose(containerAvailable: false, dockerAvailable: false) == nil
        )
    }
}
