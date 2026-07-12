import Testing

@testable import WendyAgentCore

@Suite("OrgIdentity")
struct OrgIdentityTests {
    @Test("parses org from a well-formed common name")
    func parsesOrg() {
        #expect(OrgIdentity.organizationID(fromCommonName: "sh/wendy/7/42") == 7)
    }

    @Test("rejects malformed common names")
    func rejectsMalformed() {
        #expect(OrgIdentity.organizationID(fromCommonName: "sh/wendy/7") == nil)
        #expect(OrgIdentity.organizationID(fromCommonName: "nope") == nil)
        #expect(OrgIdentity.organizationID(fromCommonName: "sh/wendy/abc/42") == nil)
        #expect(OrgIdentity.organizationID(fromCommonName: "sh/wendy/0/42") == nil)
        #expect(OrgIdentity.organizationID(fromCommonName: "") == nil)
    }

    @Test("does not read an org from a user common name")
    func ignoresUserCommonName() {
        // A `wendy/user/<uid>` CN carries no org in the CN; the org lives in a
        // SAN URN instead. The CN-only helper must not invent one.
        #expect(OrgIdentity.organizationID(fromCommonName: "wendy/user/abc") == nil)
    }
}

@Suite("ClientCertAuthorizer.OrgEnforcementMode.parse")
struct OrgEnforcementModeParseTests {
    @Test("empty or absent defaults to grace")
    func defaultsToGrace() {
        #expect(ClientCertAuthorizer.OrgEnforcementMode.parse(nil) == (.grace, true))
        #expect(ClientCertAuthorizer.OrgEnforcementMode.parse("") == (.grace, true))
        #expect(ClientCertAuthorizer.OrgEnforcementMode.parse("   ") == (.grace, true))
    }

    @Test("recognizes off/grace/strict case-insensitively")
    func recognizesKnownValues() {
        #expect(ClientCertAuthorizer.OrgEnforcementMode.parse("off") == (.off, true))
        #expect(ClientCertAuthorizer.OrgEnforcementMode.parse("GRACE") == (.grace, true))
        #expect(ClientCertAuthorizer.OrgEnforcementMode.parse(" Strict ") == (.strict, true))
    }

    @Test("unknown values fall back to grace and report unrecognized")
    func unknownFallsBack() {
        #expect(ClientCertAuthorizer.OrgEnforcementMode.parse("lenient") == (.grace, false))
    }
}
