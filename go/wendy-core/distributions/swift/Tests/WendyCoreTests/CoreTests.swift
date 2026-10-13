import Testing
import WendyCore

@Test
func `calls the packaged Go engine`() throws {
    #expect(try WendyHelloWorld() == 42)
}
