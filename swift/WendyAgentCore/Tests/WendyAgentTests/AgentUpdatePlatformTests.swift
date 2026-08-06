import Foundation
import GRPCCore
import Testing

@testable import WendyAgentCore

@Suite("AgentUpdateCodesignPolicy")
struct AgentUpdateCodesignPolicyTests {
    private func info(
        teamID: String?,
        isDeveloperID: Bool,
        bundleIdentifier: String?
    ) -> CodesignInfo {
        CodesignInfo(
            teamID: teamID,
            isDeveloperID: isDeveloperID,
            bundleIdentifier: bundleIdentifier
        )
    }

    @Test("Developer ID incoming and running with the same team ID is allowed")
    func sameTeamDeveloperIDAllowed() {
        let running = self.info(
            teamID: "TEAM1",
            isDeveloperID: true,
            bundleIdentifier: "sh.wendy.agent"
        )
        let incoming = self.info(
            teamID: "TEAM1",
            isDeveloperID: true,
            bundleIdentifier: "sh.wendy.agent"
        )
        #expect(AgentUpdateCodesignPolicy.check(incoming: incoming, running: running) == nil)
    }

    @Test("Developer ID incoming and running with different team IDs is denied")
    func differentTeamDeveloperIDDenied() {
        let running = self.info(
            teamID: "TEAM1",
            isDeveloperID: true,
            bundleIdentifier: "sh.wendy.agent"
        )
        let incoming = self.info(
            teamID: "TEAM2",
            isDeveloperID: true,
            bundleIdentifier: "sh.wendy.agent"
        )
        let error = AgentUpdateCodesignPolicy.check(incoming: incoming, running: running)
        #expect(error?.code == .permissionDenied)
        #expect(error?.message.contains("TEAM2") == true)
        #expect(error?.message.contains("TEAM1") == true)
    }

    @Test("Developer ID running rejects a non-Developer-ID incoming bundle")
    func developerIDRunningRejectsNonDeveloperIDIncoming() {
        let running = self.info(
            teamID: "TEAM1",
            isDeveloperID: true,
            bundleIdentifier: "sh.wendy.agent"
        )
        let incoming = self.info(
            teamID: nil,
            isDeveloperID: false,
            bundleIdentifier: "sh.wendy.agent"
        )
        let error = AgentUpdateCodesignPolicy.check(incoming: incoming, running: running)
        #expect(error?.code == .failedPrecondition)
        #expect(error?.message.contains("not Developer ID signed") == true)
    }

    @Test("non-Developer-ID running accepts an ad-hoc-signed incoming bundle (dev escape hatch)")
    func nonDeveloperIDRunningAllowsAdHocIncoming() {
        let running = self.info(
            teamID: nil,
            isDeveloperID: false,
            bundleIdentifier: "sh.wendy.agent"
        )
        let incoming = self.info(
            teamID: nil,
            isDeveloperID: false,
            bundleIdentifier: "sh.wendy.agent"
        )
        #expect(AgentUpdateCodesignPolicy.check(incoming: incoming, running: running) == nil)
    }

    @Test("bundle identifier mismatch is denied even for a dev running build")
    func bundleIdentifierMismatchDeniedForDevRunning() {
        let running = self.info(
            teamID: nil,
            isDeveloperID: false,
            bundleIdentifier: "sh.wendy.agent"
        )
        let incoming = self.info(
            teamID: nil,
            isDeveloperID: false,
            bundleIdentifier: "sh.wendy.evil"
        )
        let error = AgentUpdateCodesignPolicy.check(incoming: incoming, running: running)
        #expect(error?.code == .failedPrecondition)
        #expect(error?.message.contains("sh.wendy.evil") == true)
        #expect(error?.message.contains("sh.wendy.agent") == true)
    }

    @Test("nil vs. set bundle identifier is allowed (not treated as a mismatch)")
    func nilVersusSetBundleIdentifierAllowed() {
        let running = self.info(teamID: "TEAM1", isDeveloperID: true, bundleIdentifier: nil)
        let incoming = self.info(
            teamID: "TEAM1",
            isDeveloperID: true,
            bundleIdentifier: "sh.wendy.agent"
        )
        #expect(AgentUpdateCodesignPolicy.check(incoming: incoming, running: running) == nil)
    }
}

@Suite("AgentUpdateLock")
struct AgentUpdateLockTests {
    @Test("tryAcquire succeeds once, fails while held, then succeeds again after release")
    func tryAcquireThenRelease() async {
        let lock = AgentUpdateLock()
        #expect(await lock.tryAcquire() == true)
        #expect(await lock.tryAcquire() == false)
        await lock.release()
        #expect(await lock.tryAcquire() == true)
    }
}

@Suite("AgentRelauncher.makeArguments")
struct AgentRelauncherMakeArgumentsTests {
    @Test("pid and bundle path land in $1/$2 positions; script polls kill -0 then execs open")
    func argumentsShapeAndScriptContent() {
        let arguments = AgentRelauncher.makeArguments(
            pid: 4242,
            bundlePath: "/Applications/Wendy Agent.app"
        )

        #expect(arguments[0] == "-c")

        let script = arguments[1]
        #expect(script.contains(#"while /bin/kill -0 "$1" 2>/dev/null; do"#))
        #expect(script.contains("/bin/sleep 0.5"))
        #expect(script.contains(#"[ "$i" -ge 600 ] && exit 1"#))
        #expect(script.contains(#"exec /usr/bin/open "$2""#))

        // $0 is a placeholder positional arg (script name); $1/$2 are pid/path.
        #expect(arguments[2] == "wendy-agent-relaunch")
        #expect(arguments[3] == "4242")
        #expect(arguments[4] == "/Applications/Wendy Agent.app")
        #expect(arguments.count == 5)
    }
}
