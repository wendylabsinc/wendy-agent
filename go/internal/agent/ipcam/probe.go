package ipcam

import (
	"bufio"
	"context"
	"crypto/md5" //nolint:gosec // RFC 2617 digest auth mandates MD5; this is not a security boundary.
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProbeResult classifies the outcome of ProbeCredentials.
type ProbeResult int

const (
	// ProbeOK means the camera accepted the DESCRIBE, with or without
	// credentials, or rejected only the stream path (404/454) rather than the
	// login. Either way the credentials themselves are not the problem.
	ProbeOK ProbeResult = iota
	// ProbeAuthFailed means the camera rejected the credentials: a 401/403
	// survived an authenticated retry, or the challenge itself could not be
	// answered (an auth scheme this probe does not implement).
	ProbeAuthFailed
	// ProbeUnreachable means the camera's RTSP port never produced a usable
	// answer: the dial failed, or the connection stopped responding partway
	// through the exchange.
	ProbeUnreachable
)

// probeDialContext is the seam ProbeCredentials dials through. Tests replace
// it to point at an in-process net.Listener instead of a real camera.
var probeDialContext = (&net.Dialer{Timeout: 3 * time.Second}).DialContext

// probeIOTimeout bounds the whole DESCRIBE/challenge/DESCRIBE exchange, not
// just the dial. A camera that completes the TCP handshake but never answers
// — a half-open firewall, a wedged RTSP stack — must not hang `wendy device
// camera test` forever; three seconds is ample for two small round trips to a
// LAN device.
const probeIOTimeout = 3 * time.Second

// ProbeCredentials dials the camera's RTSP port and performs a DESCRIBE with
// the given login, the same request a stream pull opens with. It is plain TCP
// plus crypto/md5 digest auth per RFC 2617 — no GStreamer, no kernel module —
// so `camera test` works on every WendyOS build and works remotely over the
// tunnel, unlike a pipeline probe that needs the local video stack.
//
// The DESCRIBE targets the camera's normal stream path (via streamPath, the
// same helper StreamURL uses) purely to exercise the same request a real
// pull would send; the path itself is not what is under test. That is why a
// 404/454 after the camera has accepted (or waived) authentication still
// counts as ProbeOK: it means the login worked and the path is wrong, which
// is a different problem than the one this probe answers.
func ProbeCredentials(ctx context.Context, cam Camera, cred Credential) (ProbeResult, string) {
	if cam.Address == "" {
		return ProbeUnreachable, RedactText(FormatUnreachable(cam), cred.Username, cred.Password)
	}

	dialAddr := net.JoinHostPort(cam.Address, strconv.Itoa(RTSPPort))
	conn, err := probeDialContext(ctx, "tcp", dialAddr)
	if err != nil {
		return ProbeUnreachable, RedactText(FormatUnreachable(cam), cred.Username, cred.Password)
	}
	defer conn.Close() //nolint:errcheck

	deadline := time.Now().Add(probeIOTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	// The URI names the address as configured, not the (possibly seam-redirected)
	// socket dialed: it is what a real client sends and what the digest response
	// is computed over, and it must match the URL a stream pull would build.
	uri := (&url.URL{
		Scheme: "rtsp",
		Host:   net.JoinHostPort(cam.Address, strconv.Itoa(RTSPPort)),
		Path:   streamPath(cam, StreamAuto),
	}).String()

	r := bufio.NewReader(conn)

	if err := writeDescribeRequest(conn, uri, 1, ""); err != nil {
		return ProbeUnreachable, RedactText(FormatUnreachable(cam), cred.Username, cred.Password)
	}
	resp, err := readRTSPResponse(r)
	if err != nil {
		return ProbeUnreachable, RedactText(FormatUnreachable(cam), cred.Username, cred.Password)
	}

	// challenged records whether the response being classified followed an
	// authenticated retry, or is the camera's first answer. A 404/454 means
	// something different in each case: after a challenge it means the login
	// was evaluated and accepted; on the first response it means the camera
	// never evaluated the login at all, which classifyProbeResponse's detail
	// string has to say honestly rather than imply the credentials were
	// checked.
	challenged := false
	if resp.status == 401 {
		authorization, ok := buildAuthorization(resp.headers["www-authenticate"], cred, uri)
		if !ok {
			return ProbeAuthFailed, RedactText(fmt.Sprintf(
				"camera %d at %s challenged with an authentication scheme this probe cannot answer",
				cam.ID, cam.Address), cred.Username, cred.Password)
		}
		if err := writeDescribeRequest(conn, uri, 2, authorization); err != nil {
			return ProbeUnreachable, RedactText(FormatUnreachable(cam), cred.Username, cred.Password)
		}
		resp, err = readRTSPResponse(r)
		if err != nil {
			return ProbeUnreachable, RedactText(FormatUnreachable(cam), cred.Username, cred.Password)
		}
		challenged = true
	}

	return classifyProbeResponse(resp, cam, cred, challenged)
}

// classifyProbeResponse turns the final RTSP status of the exchange into a
// ProbeResult. It only ever sees the last response: either the first one, if
// it was not a 401, or the one that followed an authenticated retry —
// challenged says which, since a 404/454 means something different in each
// case.
func classifyProbeResponse(resp rtspResponse, cam Camera, cred Credential, challenged bool) (ProbeResult, string) {
	switch {
	case resp.status >= 200 && resp.status < 300:
		return ProbeOK, RedactText(fmt.Sprintf(
			"camera %d at %s accepted the credentials", cam.ID, cam.Address), cred.Username, cred.Password)
	case resp.status == 401 || resp.status == 403:
		return ProbeAuthFailed, RedactText(fmt.Sprintf(
			"camera %d at %s rejected the credentials (RTSP %d)", cam.ID, cam.Address, resp.status),
			cred.Username, cred.Password)
	case resp.status == 404 || resp.status == 454:
		if challenged {
			// The login was not what stopped this request: the camera evaluated
			// and accepted it, then objected to the path instead. Credentials are
			// the only thing this probe promises to answer for.
			return ProbeOK, RedactText(fmt.Sprintf(
				"camera %d at %s accepted the credentials; the stream path returned RTSP %d, which is a separate issue",
				cam.ID, cam.Address, resp.status), cred.Username, cred.Password)
		}
		// No challenge means the camera never evaluated the credentials at all —
		// saying it "accepted" them here would overclaim. Still ProbeOK, because
		// nothing rejected the login; the path is what needs a look.
		return ProbeOK, RedactText(fmt.Sprintf(
			"camera %d at %s did not request authentication; the stream path returned RTSP %d and may be wrong",
			cam.ID, cam.Address, resp.status), cred.Username, cred.Password)
	default:
		// Neither an accept nor a recognized auth rejection: treated as
		// AuthFailed because ProbeOK would overclaim, but the message says only
		// what happened rather than asserting the credentials were the problem.
		return ProbeAuthFailed, RedactText(fmt.Sprintf(
			"camera %d at %s returned unexpected RTSP status %d to DESCRIBE, which this probe cannot read as a credential verdict",
			cam.ID, cam.Address, resp.status), cred.Username, cred.Password)
	}
}

// writeDescribeRequest sends a DESCRIBE request. CSeq is required by RTSP
// (RFC 2326 §12.17) on every request; authorization, when non-empty, is sent
// as the Authorization header value verbatim.
func writeDescribeRequest(w io.Writer, uri string, cseq int, authorization string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "DESCRIBE %s RTSP/1.0\r\n", uri)
	fmt.Fprintf(&b, "CSeq: %d\r\n", cseq)
	if authorization != "" {
		fmt.Fprintf(&b, "Authorization: %s\r\n", authorization)
	}
	b.WriteString("\r\n")
	_, err := w.Write([]byte(b.String()))
	return err
}

// rtspResponse is the subset of an RTSP response this probe needs: the status
// code and the headers, keyed lower-case with every value kept (a camera may
// repeat WWW-Authenticate once per scheme it offers).
type rtspResponse struct {
	status  int
	headers map[string][]string
}

// readRTSPResponse parses one RTSP response from r. It tolerates the variance
// a real camera exhibits: CRLF or bare LF line endings, and a response line
// with no reason phrase — only the numeric status in the second
// whitespace-separated field is required.
func readRTSPResponse(r *bufio.Reader) (rtspResponse, error) {
	statusLine, err := r.ReadString('\n')
	if err != nil {
		return rtspResponse{}, fmt.Errorf("reading RTSP status line: %w", err)
	}
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return rtspResponse{}, fmt.Errorf("malformed RTSP status line: %q", strings.TrimSpace(statusLine))
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return rtspResponse{}, fmt.Errorf("malformed RTSP status code: %q", fields[1])
	}

	headers := make(map[string][]string)
	contentLength := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return rtspResponse{}, fmt.Errorf("reading RTSP headers: %w", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue // not a well-formed header line; skip rather than abort the probe
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		headers[key] = append(headers[key], value)
		if key == "content-length" {
			if n, err := strconv.Atoi(value); err == nil {
				contentLength = n
			}
		}
	}

	// A DESCRIBE reply can carry an SDP body (Content-Length > 0). This probe
	// never reads it, but it must still be drained: a resend on the same
	// connection would otherwise read stale body bytes as the next status line.
	if contentLength > 0 {
		if _, err := r.Discard(contentLength); err != nil {
			return rtspResponse{}, fmt.Errorf("reading RTSP body: %w", err)
		}
	}

	return rtspResponse{status: status, headers: headers}, nil
}

// buildAuthorization turns a set of WWW-Authenticate challenge values into an
// Authorization header value for uri, preferring Digest over Basic when a
// camera offers both — Digest never puts the password on the wire. It
// reports ok=false when none of the offered schemes can be answered (no
// scheme recognized, or a Digest challenge missing the nonce it needs).
func buildAuthorization(challenges []string, cred Credential, uri string) (string, bool) {
	scheme, challenge, ok := selectChallenge(challenges)
	if !ok {
		return "", false
	}
	switch scheme {
	case "digest":
		c, ok := parseDigestChallenge(parseAuthParams(challenge))
		if !ok {
			return "", false
		}
		return c.authorization(cred, uri, digestCnonce()), true
	case "basic":
		token := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Password))
		return "Basic " + token, true
	default:
		return "", false
	}
}

// selectChallenge picks the challenge to answer from every WWW-Authenticate
// value a response carried, favoring Digest whenever it appears among them,
// regardless of header order.
func selectChallenge(challenges []string) (scheme, raw string, ok bool) {
	var basic string
	for _, c := range challenges {
		c = strings.TrimSpace(c)
		switch {
		case c == "":
			continue
		case strings.HasPrefix(strings.ToLower(c), "digest"):
			return "digest", c, true
		case strings.HasPrefix(strings.ToLower(c), "basic"):
			basic = c
		}
	}
	if basic != "" {
		return "basic", basic, true
	}
	return "", "", false
}

// parseAuthParams parses the comma-separated key=value parameters that
// follow a challenge's scheme token, accepting both quoted ("realm=\"x\"")
// and unquoted (realm=x) values. Splitting respects quotes, so a comma inside
// a quoted value (legal in a realm string) does not end the parameter early.
// A quoted value's escapes are resolved by unquoteAuthValue, so a param like
// realm="IP\"Camera, Ltd" comes out as the unescaped IP"Camera, Ltd.
func parseAuthParams(challenge string) map[string]string {
	params := make(map[string]string)
	_, rest, ok := strings.Cut(challenge, " ")
	if !ok {
		return params
	}
	for _, part := range splitAuthParams(rest) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		value = unquoteAuthValue(value)
		params[key] = value
	}
	return params
}

// splitAuthParams splits a challenge's parameter list on commas, treating
// text inside double quotes as opaque so a quoted value containing a comma —
// or a backslash-escaped quote, per RFC 7230's quoted-pair — is not mistaken
// for a parameter boundary. It is a three-state machine: outside a quoted
// value, inside one, and inside one having just seen a backslash. Runes are
// copied verbatim; unescaping is unquoteAuthValue's job, not this function's.
// An unterminated quote is tolerated leniently: everything from the opening
// quote to the end of the string becomes a single trailing part, matching
// this parser's existing posture of degrading gracefully on malformed input
// rather than dropping the challenge.
func splitAuthParams(s string) []string {
	const (
		outside = iota
		inQuotes
		inQuotesEscape
	)
	state := outside
	var parts []string
	var cur strings.Builder
	for _, r := range s {
		switch state {
		case outside:
			switch r {
			case '"':
				state = inQuotes
				cur.WriteRune(r)
			case ',':
				parts = append(parts, cur.String())
				cur.Reset()
			default:
				cur.WriteRune(r)
			}
		case inQuotes:
			cur.WriteRune(r)
			switch r {
			case '\\':
				state = inQuotesEscape
			case '"':
				state = outside
			}
		case inQuotesEscape:
			cur.WriteRune(r)
			state = inQuotes
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// unquoteAuthValue strips one layer of surrounding double quotes from an
// auth-param value and resolves \X escapes (RFC 7230's quoted-pair) within
// them, so a param that arrived as "IP\"Camera, Ltd" yields the actual
// realm text IP"Camera, Ltd. A value with no surrounding quotes — legal for
// tokens like qop or algorithm — is returned unchanged.
func unquoteAuthValue(v string) string {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return v
	}
	inner := v[1 : len(v)-1]
	var b strings.Builder
	escape := false
	for _, r := range inner {
		if escape {
			b.WriteRune(r)
			escape = false
			continue
		}
		if r == '\\' {
			escape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// quoteAuthValue renders s as an RFC 7230 quoted-string: wrapped in double
// quotes, with any backslash or double quote inside it escaped so the result
// round-trips through unquoteAuthValue back to s.
func quoteAuthValue(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// digestResponse computes the RFC 2617 request-digest for a request with no
// qop (the classic, pre-RFC 2617-qop form every RTSP camera this probe
// targets still accepts):
//
//	response = MD5( MD5(username:realm:password) ":" nonce ":" MD5(method:uri) )
func digestResponse(username, password, realm, nonce, method, uri string) string {
	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)
	return md5Hex(ha1 + ":" + nonce + ":" + ha2)
}

// md5Hex returns the lower-case hex encoding of s's MD5 sum, the form RFC
// 2617 digest values take on the wire.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // RFC 2617 digest auth mandates MD5.
	return hex.EncodeToString(sum[:])
}

// digestHA1 returns the RFC 7616 §3.4.2 A1 hash for the plain MD5 algorithm:
// HA1 = MD5(username:realm:password).
func digestHA1(username, password, realm string) string {
	return md5Hex(username + ":" + realm + ":" + password)
}

// digestHA1Sess folds the challenge's nonce and the client's cnonce into an
// already-computed HA1 for the MD5-sess algorithm (RFC 7616 §3.4.2):
//
//	HA1 = MD5( HA1 ":" nonce ":" cnonce )
func digestHA1Sess(ha1, nonce, cnonce string) string {
	return md5Hex(ha1 + ":" + nonce + ":" + cnonce)
}

// digestHA2 returns the RFC 7616 §3.4.3 A2 hash for a request with no
// entity-body protection (qop absent or "auth", never "auth-int"):
// HA2 = MD5(method:uri).
func digestHA2(method, uri string) string {
	return md5Hex(method + ":" + uri)
}

// digestResponseQop computes the RFC 7616 §3.4.1 request-digest for
// qop="auth" with nc fixed at "00000001" — this probe sends exactly one
// authorized request per connection, so there is never a second nonce-count
// to advance:
//
//	response = MD5( HA1 ":" nonce ":" "00000001" ":" cnonce ":" "auth" ":" HA2 )
func digestResponseQop(ha1, nonce, cnonce, ha2 string) string {
	return md5Hex(ha1 + ":" + nonce + ":00000001:" + cnonce + ":auth:" + ha2)
}

// digestChallenge is the parsed, unescaped view of one Digest challenge.
type digestChallenge struct {
	realm     string
	nonce     string
	opaque    string // "" when the challenge carried none
	algorithm string // verbatim token from the challenge; "" means MD5
	sess      bool   // algorithm was MD5-sess (case-insensitive)
	qopAuth   bool   // challenge's qop list offered "auth"
}

// parseDigestChallenge builds a digestChallenge from a Digest challenge's
// already-split, already-unescaped parameters (parseAuthParams' output). It
// returns ok=false when the challenge cannot be answered: no nonce, an
// algorithm other than MD5 or MD5-sess (the SHA-256 family and anything else
// is out of scope, though this is the seam a later table entry would extend
// through), or a qop list that never offers "auth" — including a challenge
// that offers only "auth-int", which this probe does not implement and would
// rather honestly refuse than silently misanswer. A challenge with no qop
// parameter at all is not a refusal: it is the legacy pre-RFC 2617-qop form,
// still answered via the classic response formula.
func parseDigestChallenge(params map[string]string) (digestChallenge, bool) {
	nonce := params["nonce"]
	if nonce == "" {
		return digestChallenge{}, false
	}

	algorithm := params["algorithm"]
	sess := false
	switch strings.ToLower(algorithm) {
	case "", "md5":
		// sess stays false; algorithm is either unset (MD5 is the default
		// per RFC 7616 §3.3) or explicitly MD5.
	case "md5-sess":
		sess = true
	default:
		return digestChallenge{}, false
	}

	qopAuth := false
	if qopOffered, present := params["qop"]; present {
		offersAuth := false
		for _, tok := range strings.Split(qopOffered, ",") {
			if strings.TrimSpace(tok) == "auth" {
				offersAuth = true
				break
			}
		}
		if !offersAuth {
			return digestChallenge{}, false
		}
		qopAuth = true
	}

	return digestChallenge{
		realm:     params["realm"],
		nonce:     nonce,
		opaque:    params["opaque"],
		algorithm: algorithm,
		sess:      sess,
		qopAuth:   qopAuth,
	}, true
}

// authorization renders the Authorization header value for a DESCRIBE
// request answering this challenge. cnonce is threaded in (not generated
// here) so the header and the hash it carries are testable against fixed
// vectors; digestCnonce is what callers use to supply a real one.
//
// Response math (RFC 7616 §3.4): HA1 = MD5(user:realm:pass), folded through
// digestHA1Sess when the algorithm is MD5-sess; HA2 = MD5(method:uri); a qop
// offer of "auth" uses response = MD5(HA1:nonce:00000001:cnonce:auth:HA2),
// otherwise the legacy response = MD5(HA1:nonce:HA2) — computed via
// digestResponse itself for the plain-MD5, no-qop case, so that shape stays
// byte-identical to the pre-existing behavior no camera's expectations have
// changed under.
func (c digestChallenge) authorization(cred Credential, uri, cnonce string) string {
	const method = "DESCRIBE"

	var response string
	switch {
	case !c.sess && !c.qopAuth:
		response = digestResponse(cred.Username, cred.Password, c.realm, c.nonce, method, uri)
	default:
		ha1 := digestHA1(cred.Username, cred.Password, c.realm)
		if c.sess {
			ha1 = digestHA1Sess(ha1, c.nonce, cnonce)
		}
		ha2 := digestHA2(method, uri)
		if c.qopAuth {
			response = digestResponseQop(ha1, c.nonce, cnonce, ha2)
		} else {
			response = md5Hex(ha1 + ":" + c.nonce + ":" + ha2)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Digest username=%s, realm=%s, nonce=%s, uri=%s, response=%s",
		quoteAuthValue(cred.Username), quoteAuthValue(c.realm), quoteAuthValue(c.nonce),
		quoteAuthValue(uri), quoteAuthValue(response))
	if c.algorithm != "" {
		// Echoed unquoted, in the challenge's exact spelling: algorithm is a
		// token per the digest ABNF, and a camera that sent "MD5-sess" may
		// reject a resend that echoes back "md5-sess" or a quoted variant.
		fmt.Fprintf(&b, ", algorithm=%s", c.algorithm)
	}
	if c.opaque != "" {
		// Echoed verbatim and quoted: opaque is server-chosen data this probe
		// must return unmodified, not reinterpreted.
		fmt.Fprintf(&b, ", opaque=%s", quoteAuthValue(c.opaque))
	}
	if c.qopAuth {
		// qop and nc are tokens (unquoted per the ABNF); cnonce is a
		// quoted-string. nc is always "00000001": this probe sends exactly
		// one authorized request per connection, so there is only ever a
		// first nonce-count.
		fmt.Fprintf(&b, ", qop=auth, nc=00000001, cnonce=%s", quoteAuthValue(cnonce))
	}
	return b.String()
}

// digestCnonce is the cnonce source for a live Authorization header: hex of
// 8 bytes from crypto/rand. If the CSPRNG read fails — a broken or exhausted
// entropy source — a timestamp-derived value keeps the probe able to answer
// rather than aborting: a cnonce only needs to be distinct per request to do
// its job (letting the server detect a replayed response), not
// cryptographically unpredictable, since it is the client's contribution to
// the hash, not a secret. Tests replace this var to pin the cnonce so the
// response hash matches a precomputed vector.
var digestCnonce = func() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf[:])
}
