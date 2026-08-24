package ipcam

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// probeTestCamera matches the fixed inputs the digest vector in
// TestDigestResponseVector was hand-derived from, so
// TestProbeCredentials_DigestAuthAccepted can assert the exact same hex
// string appears on the wire: proof that ProbeCredentials wires realm, nonce,
// method and uri into digestResponse the way the vector test proves
// digestResponse itself computes them.
func probeTestCamera() Camera {
	return Camera{MAC: "ec:71:db:2a:ae:7e", ID: 7, Address: "10.0.0.5"}
}

// withScriptedServer starts a TCP listener, accepts exactly one connection,
// and hands it to script on a goroutine. It points probeDialContext at the
// listener for the duration of the test (restored via t.Cleanup) so
// ProbeCredentials, which always dials Camera.Address:554, actually reaches
// this fake camera instead.
func withScriptedServer(t *testing.T, script func(t *testing.T, conn net.Conn)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		script(t, conn)
	}()
	t.Cleanup(func() { <-accepted })

	original := probeDialContext
	probeDialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String())
	}
	t.Cleanup(func() { probeDialContext = original })
}

// readRequest reads one RTSP request's headers (request line + header lines,
// up to the blank line) off the server side of the fake connection and
// returns them joined by "\n" so a test can assert on their content, e.g. the
// Authorization header a digest resend must carry.
func readRequest(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("server: reading request: %v", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, "\n")
}

// TestDigestResponseVector hand-derives one RFC 2617 digest chain outside
// this package's own code (via an independent Python md5 computation, not by
// calling digestResponse a second time) and checks digestResponse reproduces
// it, so a bug that broke the formula could not also fake the expected value.
//
// Inputs: username="admin", realm="IP Camera", password="hunter2",
// nonce="6629fae49393a05397450978507c4ef1", method="DESCRIBE",
// uri="rtsp://10.0.0.5:554/h264Preview_01_sub" — the same login, camera
// address and default sub-stream path TestProbeCredentials_DigestAuthAccepted
// uses, so both tests check the same computation from two directions.
//
// Derivation (python3 -c 'import hashlib; ...', reproducible by hand with any
// md5 tool):
//
//	HA1 input:      "admin:IP Camera:hunter2"
//	HA1 = md5(HA1 input) = e891128b69317a620410fe637e7ebf85
//
//	HA2 input:      "DESCRIBE:rtsp://10.0.0.5:554/h264Preview_01_sub"
//	HA2 = md5(HA2 input) = e36b666f9ea60f6e4563d1cd6312216b
//
//	response input: HA1 + ":" + nonce + ":" + HA2
//	              = "e891128b69317a620410fe637e7ebf85:6629fae49393a05397450978507c4ef1:e36b666f9ea60f6e4563d1cd6312216b"
//	response = md5(response input) = e5026857c5e65fe127d4876714c83a5b
func TestDigestResponseVector(t *testing.T) {
	const (
		username = "admin"
		realm    = "IP Camera"
		password = "hunter2"
		nonce    = "6629fae49393a05397450978507c4ef1"
		method   = "DESCRIBE"
		uri      = "rtsp://10.0.0.5:554/h264Preview_01_sub"
		want     = "e5026857c5e65fe127d4876714c83a5b"
	)
	if got := digestResponse(username, password, realm, nonce, method, uri); got != want {
		t.Fatalf("digestResponse = %q, want %q", got, want)
	}
}

// TestDigestResponseVector_RFC2617QopAuth checks the qop="auth" request-digest
// formula (RFC 7616 §3.4) against RFC 2617's own worked example (§3.5, the
// "Mufasa" vector) rather than a hand-derived one: the expected value comes
// from the RFC text itself, so a bug that broke the formula could not also
// happen to fake the published answer.
//
// Inputs, verbatim from RFC 2617 §3.5: username="Mufasa",
// realm="testrealm@host.com", password="Circle Of Life",
// nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", cnonce="0a4f113b", nc="00000001",
// qop="auth", method="GET", uri="/dir/index.html". The RFC gives response =
// "6629fae49393a05397450978507c4ef1".
func TestDigestResponseVector_RFC2617QopAuth(t *testing.T) {
	const (
		username = "Mufasa"
		realm    = "testrealm@host.com"
		password = "Circle Of Life"
		nonce    = "dcd98b7102dd2f0e8b11d0f600bfb0c093"
		cnonce   = "0a4f113b"
		method   = "GET"
		uri      = "/dir/index.html"
		want     = "6629fae49393a05397450978507c4ef1"
	)
	ha1 := digestHA1(username, password, realm)
	ha2 := digestHA2(method, uri)
	if got := digestResponseQop(ha1, nonce, cnonce, ha2); got != want {
		t.Fatalf("digestResponseQop = %q, want %q (RFC 2617 section 3.5 Mufasa example)", got, want)
	}
}

// A camera that challenges with Digest must be answered with the RFC 2617
// response, on the same connection, and the second DESCRIBE must carry it —
// checked against the exact hex string TestDigestResponseVector hand-derived.
func TestProbeCredentials_DigestAuthAccepted(t *testing.T) {
	const wantResponse = "e5026857c5e65fe127d4876714c83a5b" // see TestDigestResponseVector

	var secondRequest string
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r) // unauthenticated DESCRIBE

		// Header case and quoting deliberately vary from the second response
		// below, to exercise case-insensitive header parsing here too.
		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			"www-authenticate: Digest realm=\"IP Camera\", nonce=\"6629fae49393a05397450978507c4ef1\"\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		secondRequest = readRequest(t, r) // authenticated DESCRIBE

		_, err = conn.Write([]byte("RTSP/1.0 200 OK\r\n" +
			"CSeq: 2\r\n" +
			"Content-Type: application/sdp\r\n" +
			"Content-Length: 0\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 200: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK; detail=%q", result, detail)
	}
	if !strings.Contains(secondRequest, `response="`+wantResponse+`"`) {
		t.Fatalf("resend did not carry the RFC 2617 response: %q", secondRequest)
	}
	if !strings.Contains(secondRequest, `username="admin"`) || !strings.Contains(secondRequest, `realm="IP Camera"`) {
		t.Fatalf("resend missing digest fields: %q", secondRequest)
	}
	if strings.Contains(detail, "hunter2") {
		t.Fatalf("detail leaked the password: %q", detail)
	}
}

// Wrong credentials still get a properly-formed digest resend, but the camera
// keeps rejecting it: the outcome must be ProbeAuthFailed, not ProbeOK, and
// nothing about the failed password may reach the detail string. The
// challenge also carries a Basic offer ahead of the Digest one, as two
// separate WWW-Authenticate headers, to check that Digest is preferred
// regardless of header order.
func TestProbeCredentials_DigestAuthRejected(t *testing.T) {
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r) // unauthenticated DESCRIBE

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			"WWW-Authenticate: Basic realm=\"IP Camera\"\r\n" +
			"WWW-Authenticate: Digest realm=\"IP Camera\", nonce=\"deadbeefnonce\"\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		second := readRequest(t, r) // authenticated DESCRIBE, still wrong
		if !strings.HasPrefix(second, "DESCRIBE ") || !strings.Contains(second, "Digest") {
			t.Fatalf("server did not see a Digest resend: %q", second)
		}

		_, err = conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 2\r\n" +
			"WWW-Authenticate: Digest realm=\"IP Camera\", nonce=\"deadbeefnonce\"\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing second 401: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "wrong-password"})

	if result != ProbeAuthFailed {
		t.Fatalf("result = %v, want ProbeAuthFailed; detail=%q", result, detail)
	}
	if strings.Contains(detail, "wrong-password") {
		t.Fatalf("detail leaked the password: %q", detail)
	}
}

// A camera offering only Basic must get a base64 Authorization header, and
// accepting it must read as ProbeOK.
func TestProbeCredentials_BasicAuth(t *testing.T) {
	// base64("viewer:s3cret") — asserted against directly so the test does not
	// simply call the same encoder the implementation does.
	const wantAuth = "Basic dmlld2VyOnMzY3JldA=="

	var secondRequest string
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r)

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			"WWW-Authenticate: Basic realm=\"IP Camera\"\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		secondRequest = readRequest(t, r)

		_, err = conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nContent-Length: 0\r\n\r\n"))
		if err != nil {
			t.Fatalf("server: writing 200: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "viewer", Password: "s3cret"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK; detail=%q", result, detail)
	}
	if !strings.Contains(secondRequest, "Authorization: "+wantAuth) {
		t.Fatalf("resend did not carry the expected Basic header: %q", secondRequest)
	}
}

// A camera with no auth at all must be accepted on the first DESCRIBE, with
// no resend — and the LF-only line endings here (no \r) check that the
// response parser does not require CRLF.
func TestProbeCredentials_NoAuthRequired200(t *testing.T) {
	var requests int
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r)
		requests++

		if _, err := conn.Write([]byte("RTSP/1.0 200 OK\nCSeq: 1\nContent-Length: 0\n\n")); err != nil {
			t.Fatalf("server: writing 200: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK; detail=%q", result, detail)
	}
	if requests != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 (no resend without a challenge)", requests)
	}
}

// A camera whose port never answers a connection attempt must report
// ProbeUnreachable with FormatUnreachable's message, not hang or panic.
func TestProbeCredentials_Unreachable(t *testing.T) {
	original := probeDialContext
	t.Cleanup(func() { probeDialContext = original })
	sentinel := errors.New("connection refused")
	probeDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, sentinel
	}

	cam := probeTestCamera()
	result, detail := ProbeCredentials(context.Background(), cam, Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeUnreachable {
		t.Fatalf("result = %v, want ProbeUnreachable; detail=%q", result, detail)
	}
	if !strings.Contains(detail, "10.0.0.5") {
		t.Fatalf("detail = %q, want it to name the camera FormatUnreachable would describe", detail)
	}
}

// A camera that accepts the login but has no content at the probed path
// (404) or no active session (454) is not a credential failure: the probe
// exists to test the login, and the login worked. The second response here
// also uses a non-standard reason phrase, checked defensively since RTSP
// implementations vary in wording.
func TestProbeCredentials_404AfterAuthIsOK(t *testing.T) {
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r)

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			"WWW-Authenticate: Digest realm=\"IP Camera\", nonce=\"anoncevalue\"\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		readRequest(t, r)

		_, err = conn.Write([]byte("RTSP/1.0 404 Stream Path Not Configured\r\nCSeq: 2\r\n\r\n"))
		if err != nil {
			t.Fatalf("server: writing 404: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK (path issue, not a credential failure); detail=%q", result, detail)
	}
	if strings.Contains(detail, "hunter2") {
		t.Fatalf("detail leaked the password: %q", detail)
	}
}

// A blocked or dead-air TCP connection (accepted, then silent) must time out
// rather than hang the caller indefinitely. The fake server here must not
// read or close the connection while the probe is in flight: either would
// unblock the client early and defeat the point of the test, which is that
// probeIOTimeout — not the peer — is what ends the wait.
func TestProbeCredentials_UnreachableAfterConnectHangs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn
	}()

	original := probeDialContext
	t.Cleanup(func() { probeDialContext = original })
	probeDialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String())
	}

	start := time.Now()
	result, _ := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})
	elapsed := time.Since(start)

	// Only now, after the probe has given up, retire the server-side
	// connection so the accept goroutine does not outlive the test.
	select {
	case conn := <-accepted:
		conn.Close() //nolint:errcheck
	case <-time.After(time.Second):
		t.Fatal("server never accepted the connection")
	}

	if result != ProbeUnreachable {
		t.Fatalf("result = %v, want ProbeUnreachable", result)
	}
	if elapsed > probeIOTimeout+time.Second {
		t.Fatalf("ProbeCredentials took %v, want it bounded by probeIOTimeout (%v)", elapsed, probeIOTimeout)
	}
}

// parseAuthParams must handle both quoting styles a camera might send, and
// must not let a comma inside a quoted value (legal in a realm string) end
// the parameter early.
func TestParseAuthParams(t *testing.T) {
	cases := []struct {
		name      string
		challenge string
		want      map[string]string
	}{
		{
			name:      "quoted values, comma inside a quoted realm",
			challenge: `Digest realm="Login, Please", nonce="abc123"`,
			want:      map[string]string{"realm": "Login, Please", "nonce": "abc123"},
		},
		{
			name:      "quoted nonce containing a comma",
			challenge: `Digest realm="IP Camera", nonce="a,b,c-nonce"`,
			want:      map[string]string{"realm": "IP Camera", "nonce": "a,b,c-nonce"},
		},
		{
			name:      "unquoted values",
			challenge: `Digest realm=IPCam, nonce=abc123`,
			want:      map[string]string{"realm": "IPCam", "nonce": "abc123"},
		},
		{
			name:      "mixed quoted and unquoted",
			challenge: `Digest realm="IP Camera", nonce=abc123`,
			want:      map[string]string{"realm": "IP Camera", "nonce": "abc123"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAuthParams(tc.challenge)
			for key, want := range tc.want {
				if got[key] != want {
					t.Errorf("params[%q] = %q, want %q (all params: %v)", key, got[key], want, got)
				}
			}
		})
	}
}

// splitAuthParams must treat a quoted value as opaque to comma-splitting —
// including a quoted value that itself contains a backslash-escaped quote —
// while still splitting on commas outside quotes, and must degrade gracefully
// (rest-as-one-part) when a challenge sends an unterminated quote.
func TestSplitAuthParams_EscapesAndQuotedCommas(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int // number of parts
	}{
		{
			name:  "escaped quote and comma inside realm, unescaped nonce follows",
			input: `realm="IP\"Camera, Ltd", nonce="n"`,
			want:  2,
		},
		{
			name:  "escaped backslash inside a quoted value does not end the quote early",
			input: `a="x\\", b=c`,
			want:  2,
		},
		{
			name:  "unterminated quote swallows the rest as one part",
			input: `realm="abc, nonce=n`,
			want:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitAuthParams(tc.input)
			if len(got) != tc.want {
				t.Fatalf("splitAuthParams(%q) = %d parts %v, want %d parts", tc.input, len(got), got, tc.want)
			}
		})
	}
}

// unquoteAuthValue and quoteAuthValue must be inverses on values that need
// escaping (an embedded quote or backslash), and unquoteAuthValue must pass
// an unquoted token through unchanged since qop, algorithm and nc are legal
// unquoted per the digest ABNF.
func TestUnquoteAndQuoteAuthValue(t *testing.T) {
	cases := []struct {
		name   string
		quoted string
		raw    string
	}{
		{"plain", `"abc123"`, "abc123"},
		{"internal space", `"IP Camera"`, "IP Camera"},
		{"escaped quote and comma", `"IP\"Camera, Ltd"`, `IP"Camera, Ltd`},
		{"escaped backslash", `"x\\"`, `x\`},
		{"empty", `""`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unquoteAuthValue(tc.quoted); got != tc.raw {
				t.Errorf("unquoteAuthValue(%q) = %q, want %q", tc.quoted, got, tc.raw)
			}
			if got := quoteAuthValue(tc.raw); got != tc.quoted {
				t.Errorf("quoteAuthValue(%q) = %q, want %q", tc.raw, got, tc.quoted)
			}
		})
	}

	if got := unquoteAuthValue("MD5"); got != "MD5" {
		t.Errorf("unquoteAuthValue(%q) = %q, want unchanged (no surrounding quotes)", "MD5", got)
	}
}

// parseDigestChallenge must refuse to answer challenges this probe cannot
// safely respond to (no nonce, an unrecognized algorithm, or a qop list that
// never offers "auth"), and must otherwise carry the challenge's fields
// through faithfully — including the algorithm token's exact spelling and a
// verbatim opaque.
func TestParseDigestChallenge(t *testing.T) {
	t.Run("missing nonce fails", func(t *testing.T) {
		if _, ok := parseDigestChallenge(map[string]string{"realm": "IP Camera"}); ok {
			t.Fatal("parseDigestChallenge succeeded with no nonce")
		}
	})

	t.Run("algorithm=md5-sess is recognized case-insensitively", func(t *testing.T) {
		c, ok := parseDigestChallenge(map[string]string{"nonce": "n", "algorithm": "md5-sess"})
		if !ok {
			t.Fatal("parseDigestChallenge failed on algorithm=md5-sess")
		}
		if !c.sess {
			t.Error("sess = false, want true")
		}
		if c.algorithm != "md5-sess" {
			t.Errorf("algorithm = %q, want the verbatim token %q", c.algorithm, "md5-sess")
		}
	})

	t.Run("algorithm=SHA-256 refuses (out of scope, structure admits it later)", func(t *testing.T) {
		if _, ok := parseDigestChallenge(map[string]string{"nonce": "n", "algorithm": "SHA-256"}); ok {
			t.Fatal("parseDigestChallenge succeeded with algorithm=SHA-256")
		}
	})

	t.Run(`qop="auth,auth-int" offers auth`, func(t *testing.T) {
		c, ok := parseDigestChallenge(map[string]string{"nonce": "n", "qop": "auth,auth-int"})
		if !ok {
			t.Fatal("parseDigestChallenge failed on qop=auth,auth-int")
		}
		if !c.qopAuth {
			t.Error("qopAuth = false, want true")
		}
	})

	t.Run(`qop="auth-int" alone refuses`, func(t *testing.T) {
		if _, ok := parseDigestChallenge(map[string]string{"nonce": "n", "qop": "auth-int"}); ok {
			t.Fatal("parseDigestChallenge succeeded with qop=auth-int only")
		}
	})

	t.Run("opaque is echoed verbatim", func(t *testing.T) {
		c, ok := parseDigestChallenge(map[string]string{"nonce": "n", "opaque": "5ccc069c403ebaf9f0171e9517f40e41"})
		if !ok {
			t.Fatal("parseDigestChallenge failed")
		}
		if c.opaque != "5ccc069c403ebaf9f0171e9517f40e41" {
			t.Errorf("opaque = %q, want verbatim echo", c.opaque)
		}
	})

	t.Run("no qop at all is legacy, not a refusal", func(t *testing.T) {
		c, ok := parseDigestChallenge(map[string]string{"nonce": "n"})
		if !ok {
			t.Fatal("parseDigestChallenge failed with no qop parameter at all")
		}
		if c.qopAuth {
			t.Error("qopAuth = true, want false when the challenge sent no qop")
		}
	})
}

// selectChallenge must recognize Digest and Basic regardless of case, and
// must prefer Digest over Basic no matter which order the headers arrived in.
func TestSelectChallenge(t *testing.T) {
	cases := []struct {
		name       string
		challenges []string
		wantScheme string
		wantOK     bool
	}{
		{"lowercase digest", []string{`digest realm="x", nonce="y"`}, "digest", true},
		{"mixed-case Digest", []string{`DiGeSt realm="x", nonce="y"`}, "digest", true},
		{"uppercase BASIC", []string{`BASIC realm="x"`}, "basic", true},
		{"lowercase basic only", []string{`basic realm="x"`}, "basic", true},
		{"basic then digest, digest still wins", []string{`Basic realm="x"`, `Digest realm="x", nonce="y"`}, "digest", true},
		{"digest then basic, digest still wins", []string{`Digest realm="x", nonce="y"`, `Basic realm="x"`}, "digest", true},
		{"unrecognized scheme only", []string{`NTLM realm="x"`}, "", false},
		{"empty list", nil, "", false},
		{"blank entries ignored", []string{"", "   ", `Basic realm="x"`}, "basic", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme, _, ok := selectChallenge(tc.challenges)
			if scheme != tc.wantScheme || ok != tc.wantOK {
				t.Fatalf("selectChallenge(%v) = (%q, %v), want (%q, %v)",
					tc.challenges, scheme, ok, tc.wantScheme, tc.wantOK)
			}
		})
	}
}

// A Digest challenge with no nonce cannot be answered — there is nothing to
// compute the response over — so this must resolve as ProbeAuthFailed rather
// than sending a malformed Authorization header or panicking.
func TestProbeCredentials_DigestChallengeMissingNonceIsAuthFailed(t *testing.T) {
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r) // unauthenticated DESCRIBE

		// realm with no nonce: a Digest challenge this probe cannot answer.
		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			"WWW-Authenticate: Digest realm=\"IP Camera\"\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}
		// No second request: ProbeCredentials must give up rather than resend.
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeAuthFailed {
		t.Fatalf("result = %v, want ProbeAuthFailed; detail=%q", result, detail)
	}
	if strings.Contains(detail, "hunter2") {
		t.Fatalf("detail leaked the password: %q", detail)
	}
}

// A camera that returns 404/454 on the very FIRST request — no 401 challenge
// at all — never evaluated the credentials, unlike the post-auth 404 case in
// TestProbeCredentials_404AfterAuthIsOK. The result is still ProbeOK (nothing
// rejected the login), but the detail must say the camera never asked for
// authentication rather than claiming it "accepted the credentials", which
// would overclaim what actually happened.
func TestProbeCredentials_404OnFirstRequestIsOKWithPathNote(t *testing.T) {
	var requests int
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r)
		requests++
		if _, err := conn.Write([]byte("RTSP/1.0 404 Not Found\r\nCSeq: 1\r\n\r\n")); err != nil {
			t.Fatalf("server: writing 404: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK (nothing rejected the login); detail=%q", result, detail)
	}
	if requests != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 (a bare 404 is not a 401 challenge)", requests)
	}
	if !strings.Contains(detail, "did not request authentication") {
		t.Fatalf("detail = %q, want it to note the camera never challenged for credentials", detail)
	}
	if strings.Contains(detail, "accepted the credentials") {
		t.Fatalf("detail = %q, overclaims credentials were evaluated when no challenge occurred", detail)
	}
}

// pinDigestCnonce points digestCnonce at a fixed value for the duration of a
// test, so the qop=auth response hash — which folds the cnonce into HA1
// (MD5-sess) and into the response itself — is reproducible against a
// precomputed vector instead of a randomly generated one.
func pinDigestCnonce(t *testing.T, cnonce string) {
	t.Helper()
	original := digestCnonce
	digestCnonce = func() string { return cnonce }
	t.Cleanup(func() { digestCnonce = original })
}

// A challenge carrying opaque, algorithm=MD5 and qop="auth" must be answered
// with the full RFC 7616 qop response — computed with nc fixed at
// "00000001" — and must echo the opaque verbatim and the algorithm's exact
// spelling, alongside the qop/nc/cnonce trio the challenge's qop offer
// requires.
func TestProbeCredentials_QopOpaqueAlgorithmChallenge(t *testing.T) {
	pinDigestCnonce(t, "0a4f113b")
	const wantResponse = "5859f533b31f0ed417fd2d03d6bd1d74"

	var secondRequest string
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r) // unauthenticated DESCRIBE

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			`WWW-Authenticate: Digest realm="IP Camera", nonce="6629fae49393a05397450978507c4ef1", opaque="5ccc069c403ebaf9f0171e9517f40e41", algorithm=MD5, qop="auth"` + "\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		secondRequest = readRequest(t, r) // authenticated DESCRIBE

		_, err = conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nContent-Length: 0\r\n\r\n"))
		if err != nil {
			t.Fatalf("server: writing 200: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK; detail=%q", result, detail)
	}
	for _, want := range []string{
		`response="` + wantResponse + `"`,
		`opaque="5ccc069c403ebaf9f0171e9517f40e41"`,
		"algorithm=MD5",
		"qop=auth",
		"nc=00000001",
		`cnonce="0a4f113b"`,
	} {
		if !strings.Contains(secondRequest, want) {
			t.Fatalf("resend missing %q: %q", want, secondRequest)
		}
	}
}

// A challenge with algorithm=MD5-sess but no qop must still fold the pinned
// cnonce into HA1 (per RFC 7616 §3.4.3) and use the legacy no-qop response
// formula, echoing algorithm=MD5-sess but omitting the qop/nc/cnonce trio
// that only applies when the challenge itself offered qop.
func TestProbeCredentials_MD5SessChallenge(t *testing.T) {
	pinDigestCnonce(t, "0a4f113b")
	const wantResponse = "48f2fc4e0f254afbeaf323ce9a205f99"

	var secondRequest string
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r)

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			`WWW-Authenticate: Digest realm="IP Camera", nonce="6629fae49393a05397450978507c4ef1", algorithm=MD5-sess` + "\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		secondRequest = readRequest(t, r)

		_, err = conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nContent-Length: 0\r\n\r\n"))
		if err != nil {
			t.Fatalf("server: writing 200: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK; detail=%q", result, detail)
	}
	if !strings.Contains(secondRequest, `response="`+wantResponse+`"`) {
		t.Fatalf("resend did not carry the MD5-sess response: %q", secondRequest)
	}
	if !strings.Contains(secondRequest, "algorithm=MD5-sess") {
		t.Fatalf("resend did not echo algorithm=MD5-sess: %q", secondRequest)
	}
	if strings.Contains(secondRequest, "qop=") {
		t.Fatalf("resend included a qop group for a challenge that offered no qop: %q", secondRequest)
	}
}

// A challenge combining algorithm=MD5-sess with qop="auth" must compose both:
// HA1 is folded through the nonce and cnonce (MD5-sess), and the response is
// then computed with the full qop=auth formula over that sess HA1 — the one
// authorization branch neither TestProbeCredentials_MD5SessChallenge (sess,
// no qop) nor TestProbeCredentials_QopOpaqueAlgorithmChallenge (qop, plain
// MD5) exercises on its own.
func TestProbeCredentials_MD5SessQopChallenge(t *testing.T) {
	pinDigestCnonce(t, "0a4f113b")
	const wantResponse = "52a00aea551b6b87ebb257ff253006e7"

	var secondRequest string
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r) // unauthenticated DESCRIBE

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			`WWW-Authenticate: Digest realm="IP Camera", nonce="6629fae49393a05397450978507c4ef1", algorithm=MD5-sess, qop="auth"` + "\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		secondRequest = readRequest(t, r) // authenticated DESCRIBE

		_, err = conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nContent-Length: 0\r\n\r\n"))
		if err != nil {
			t.Fatalf("server: writing 200: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK; detail=%q", result, detail)
	}
	for _, want := range []string{
		`response="` + wantResponse + `"`,
		"algorithm=MD5-sess",
		"qop=auth",
		"nc=00000001",
		`cnonce="0a4f113b"`,
	} {
		if !strings.Contains(secondRequest, want) {
			t.Fatalf("resend missing %q: %q", want, secondRequest)
		}
	}
}

// A realm containing a literal double quote and a comma — legal in a digest
// realm string, escaped on the wire as IP\"Camera, Ltd — must survive
// splitAuthParams without the embedded comma being mistaken for a parameter
// boundary (the nonce that follows must still parse), the response hash must
// be computed over the *unescaped* realm, and the resend must re-escape the
// realm rather than sending the broken realm="IP"Camera, Ltd" header the old
// unconditional %s formatting produced.
func TestProbeCredentials_EscapedRealmDigest(t *testing.T) {
	pinDigestCnonce(t, "0a4f113b")
	const wantResponse = "e6b49fe9324362a4e26a832ecbf7a873"

	var secondRequest string
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r)

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			`WWW-Authenticate: Digest realm="IP\"Camera, Ltd", nonce="6629fae49393a05397450978507c4ef1"` + "\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}

		secondRequest = readRequest(t, r)

		_, err = conn.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nContent-Length: 0\r\n\r\n"))
		if err != nil {
			t.Fatalf("server: writing 200: %v", err)
		}
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeOK {
		t.Fatalf("result = %v, want ProbeOK; detail=%q", result, detail)
	}
	if !strings.Contains(secondRequest, `response="`+wantResponse+`"`) {
		t.Fatalf("resend did not carry the response computed over the unescaped realm: %q", secondRequest)
	}
	if !strings.Contains(secondRequest, `realm="IP\"Camera, Ltd"`) {
		t.Fatalf("resend did not re-escape the realm: %q", secondRequest)
	}
	if !strings.Contains(secondRequest, `nonce="6629fae49393a05397450978507c4ef1"`) {
		t.Fatalf("resend lost the nonce despite the embedded comma in realm: %q", secondRequest)
	}
}

// A Digest challenge advertising an algorithm this probe does not implement
// (SHA-512, out of scope alongside the rest of the SHA-256 family) must be
// refused rather than answered with a silently-wrong MD5 response: no
// resend, ProbeAuthFailed, and a detail that honestly says the scheme cannot
// be answered rather than implying the credentials themselves were rejected.
func TestProbeCredentials_UnsupportedAlgorithmRefused(t *testing.T) {
	var requests int
	withScriptedServer(t, func(t *testing.T, conn net.Conn) {
		r := bufio.NewReader(conn)
		readRequest(t, r)
		requests++

		_, err := conn.Write([]byte("RTSP/1.0 401 Unauthorized\r\n" +
			"CSeq: 1\r\n" +
			`WWW-Authenticate: Digest realm="IP Camera", nonce="6629fae49393a05397450978507c4ef1", algorithm=SHA-512` + "\r\n" +
			"\r\n"))
		if err != nil {
			t.Fatalf("server: writing 401: %v", err)
		}
		// No second request: an unanswerable challenge must not be resent.
	})

	result, detail := ProbeCredentials(context.Background(), probeTestCamera(),
		Credential{Username: "admin", Password: "hunter2"})

	if result != ProbeAuthFailed {
		t.Fatalf("result = %v, want ProbeAuthFailed; detail=%q", result, detail)
	}
	if !strings.Contains(detail, "cannot answer") {
		t.Fatalf("detail = %q, want it to say the scheme cannot be answered", detail)
	}
	if strings.Contains(detail, "hunter2") {
		t.Fatalf("detail leaked the password: %q", detail)
	}
	if requests != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 (no resend for an unanswerable challenge)", requests)
	}
}
