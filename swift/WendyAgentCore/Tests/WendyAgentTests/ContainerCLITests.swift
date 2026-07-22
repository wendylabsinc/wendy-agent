import Testing

@testable import WendyAgentCore

@Suite struct ContainerCLITests {
    @Test func runArgumentsIncludeSchemeNameLabelAndImageLast() {
        let args = ContainerCLI.runArguments(
            containerName: "wendy-app",
            imageName: "localhost:5555/app:latest",
            specs: [
                .publishPort(host: 8080, container: 80),
                .volume(name: "wendy-app-data", path: "/data"),
            ],
            env: ["FOO": "bar"]
        )
        // Image must be the final positional argument.
        #expect(args.last == "localhost:5555/app:latest")
        #expect(args.first == "run")
        #expect(args.contains("--scheme"))
        #expect(argFollowing("--scheme", in: args) == "http")
        #expect(argFollowing("--name", in: args) == "wendy-app")
        #expect(args.contains("--label"))
        #expect(argFollowing("--label", in: args) == "wendy.managed=true")
        #expect(pairPresent("-p", "8080:80", in: args))
        #expect(pairPresent("-v", "wendy-app-data:/data", in: args))
        #expect(pairPresent("-e", "FOO=bar", in: args))
    }

    @Test func networkNoneRendersNetworkFlag() {
        let args = ContainerCLI.runArguments(
            containerName: "wendy-app",
            imageName: "img",
            specs: [.networkNone],
            env: [:]
        )
        #expect(pairPresent("--network", "none", in: args))
    }

    @Test func deleteArgumentsForce() {
        #expect(
            ContainerCLI.deleteArguments(containerName: "wendy-app") == [
                "delete", "--force", "wendy-app",
            ]
        )
    }

    @Test func parseListIncludesManagedContainer() {
        let json = """
            [
                {
                    "configuration": {
                        "id": "abc123",
                        "labels": {
                            "wendy.managed": "true"
                        }
                    },
                    "status": "running"
                }
            ]
            """
        let result = ContainerCLI.parseList(json)
        #expect(result.count == 1)
        #expect(result[0].id == "abc123")
        #expect(result[0].state == "running")
    }

    @Test func parseListFiltersUnmanagedContainer() {
        let json = """
            [
                {
                    "configuration": {
                        "id": "abc123",
                        "labels": {}
                    },
                    "status": "running"
                }
            ]
            """
        let result = ContainerCLI.parseList(json)
        #expect(result.isEmpty)
    }

    @Test func parseListReturnsEmptyForMalformed() {
        let result1 = ContainerCLI.parseList("")
        #expect(result1.isEmpty)

        let result2 = ContainerCLI.parseList("{}")
        #expect(result2.isEmpty)

        let result3 = ContainerCLI.parseList("invalid json")
        #expect(result3.isEmpty)
    }
}

private func argFollowing(_ flag: String, in args: [String]) -> String? {
    guard let i = args.firstIndex(of: flag), i + 1 < args.count else { return nil }
    return args[i + 1]
}

private func pairPresent(_ flag: String, _ value: String, in args: [String]) -> Bool {
    var i = args.startIndex
    while let j = args[i...].firstIndex(of: flag) {
        if j + 1 < args.count, args[j + 1] == value { return true }
        i = args.index(after: j)
    }
    return false
}
