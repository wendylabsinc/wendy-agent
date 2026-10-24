package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthBase is the existing browser sign-in authority default.
const AuthBase = "https://auth.dev.wendy.sh"

// IdentityResource is the identity-token audience default.
const IdentityResource = "https://pki.wendy.sh/identity"

// IdentityEndpoint is the browser PKI identity endpoint default.
const IdentityEndpoint = "https://identity.dev.pki.wendy.sh/v1/identity/certificate"

// CloudResource is the browser Cloud-token audience default.
const CloudResource = "https://cloud.dev.wendy.sh/api"

// ClientID is the registered browser public client default.
const ClientID = "cloud-login"

// BrowserMetadata holds the strictly pinned browser OIDC endpoints.
type BrowserMetadata struct {
	Issuer    string `json:"issuer"`
	Authorize string `json:"authorization_endpoint"`
	Token     string `json:"token_endpoint"`
	JWKS      string `json:"jwks_uri"`
}

// BrowserToken is the browser token endpoint payload.
type BrowserToken struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Type    string `json:"token_type"`
	Expires int    `json:"expires_in"`
}

// BrowserProfile contains the verified identity and optional display hints.
type BrowserProfile struct {
	Email          string `json:"email"`
	Organization   string `json:"organization"`
	ProfileWarning string `json:"profileWarning,omitempty"`
	Name           string `json:"name"`
	Subject        string `json:"subject"`
	Tenant         string `json:"tenant"`
	Issuer         string `json:"issuer"`
	Principal      string `json:"principal"`
	Expires        string `json:"expires"`
}

// BrowserSession holds portable browser sign-in state. The host serializes
// access, supplies keys and persistence, and never serializes Key itself.
type BrowserSession struct {
	Client                    *Client
	Settings                  *BrowserSettings
	RequireHostedMCPPolicy    bool
	Metadata                  BrowserMetadata
	Key                       platform.IdentityKey
	State, Verifier, Redirect string
	Started                   time.Time
	Certificate               Certificate
	Tokens                    BrowserToken
	Profile                   BrowserProfile
}

// BrowserSettings pins browser identity, PKI and Cloud deployment services.
type BrowserSettings struct {
	AuthBase         string `json:"auth_base"`
	ClientID         string `json:"client_id"`
	IdentityResource string `json:"identity_resource"`
	IdentityEndpoint string `json:"identity_endpoint"`
	CloudResource    string `json:"cloud_resource"`
	CloudGRPC        string `json:"cloud_grpc"`
	RelayIssuer      string `json:"relay_issuer"`
}

func (s *BrowserSession) settings() BrowserSettings {
	if s.Settings != nil {
		return *s.Settings
	}
	return BrowserSettings{AuthBase, ClientID, IdentityResource, IdentityEndpoint, CloudResource, "api.dev.wendy.sh:443", "https://api.dev.wendy.sh"}
}

// Validate validates the pinned service authorities.
func (c BrowserSettings) Validate() error {
	for _, raw := range []string{c.AuthBase, c.IdentityResource, c.IdentityEndpoint, c.CloudResource, c.RelayIssuer} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("Cloud login services require explicit HTTPS URLs")
		}
	}
	auth, _ := url.Parse(c.AuthBase)
	relay, _ := url.Parse(c.RelayIssuer)
	identity, _ := url.Parse(c.IdentityEndpoint)
	host, port, err := net.SplitHostPort(c.CloudGRPC)
	if c.ClientID == "" || auth.Path != "" || relay.Path != "" || identity.Path != "/v1/identity/certificate" || err != nil || host == "" || port != "443" || strings.ContainsAny(c.CloudGRPC, "/?#@") {
		return fmt.Errorf("invalid Cloud login client or service authority")
	}
	return nil
}

// ValidIssuer accepts canonical realm issuers on the configured authority.
func (s *BrowserSession) ValidIssuer(raw string) bool {
	u, err := url.Parse(raw)
	base, _ := url.Parse(s.settings().AuthBase)
	if err != nil || base == nil || u.Scheme != "https" || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return false
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 3 || parts[1] != "realms" || parts[2] == "" {
		return false
	}
	for _, c := range parts[2] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// orgIssuer is a valid issuer other than the system realm, which is never a
// login target. Realm names are compared without case, so /realms/SYSTEM is
// still the system realm.

// OrgIssuer additionally excludes the system realm.
func (s *BrowserSession) OrgIssuer(raw string) bool {
	return s.ValidIssuer(raw) && !strings.EqualFold(raw, s.settings().AuthBase+"/realms/system")
}

func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *BrowserSession) request(ctx context.Context, method, target, body string, headers map[string]string) ([]byte, *http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if e != nil {
		return nil, nil, e
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := s.Client.HTTP.Do(req)
	if e != nil {
		return nil, nil, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil {
		return nil, resp, e
	}
	if len(data) > 1<<20 {
		return nil, resp, errors.New("Authentication response too large")
	}
	return data, resp, nil
}

// Discover loads and strictly pins every browser OIDC endpoint.
func (s *BrowserSession) Discover(ctx context.Context, issuer string) (BrowserMetadata, error) {
	var m BrowserMetadata
	if !s.ValidIssuer(issuer) {
		return m, errors.New("Untrusted authentication issuer")
	}
	b, r, e := s.request(ctx, "GET", issuer+"/.well-known/openid-configuration", "", nil)
	if e != nil {
		return m, e
	}
	if r.StatusCode != 200 {
		return m, fmt.Errorf("Auth discovery returned %d", r.StatusCode)
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return m, e
	}
	if m.Issuer != issuer || m.Authorize != issuer+"/authorize" || m.Token != issuer+"/oauth2/token" || m.JWKS != issuer+"/.well-known/jwks.json" {
		return m, errors.New("Unexpected authentication endpoints")
	}
	return m, nil
}

// Begin settles discovery before asking the host for a key and producing a PKCE URL.
func (s *BrowserSession) Begin(ctx context.Context, email, issuer, redirect string, newKey func(context.Context) (platform.IdentityKey, error)) (string, error) {
	if err := s.settings().Validate(); err != nil {
		return "", err
	}
	u, e := url.Parse(redirect)
	if e != nil || u.User != nil || u.Path != "/auth/callback" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && u.Host == "localhost:5173")) {
		return "", errors.New("Invalid browser callback")
	}
	// Like `wendy auth login`: with no pinned realm the browser starts at the
	// realm-less universal page and the realm arrives as the callback issuer.
	// The system realm is never a login target. A caller-pinned issuer is held
	// to the same rules as one from the email lookup.
	if issuer != "" && !s.OrgIssuer(issuer) {
		return "", errors.New("Sign-in did not identify a valid organization realm")
	}
	pinned := issuer != ""
	if issuer == "" && email != "" {
		body, _ := json.Marshal(map[string]string{"email": email})
		b, r, e := s.request(ctx, "POST", s.settings().AuthBase+"/api/login/realm", string(body), map[string]string{"Content-Type": "application/json"})
		if e != nil {
			return "", e
		}
		if r.StatusCode != 200 {
			return "", fmt.Errorf("Email lookup returned %d", r.StatusCode)
		}
		var out struct {
			LoginURL string `json:"loginURL"`
			Realm    string `json:"realm"`
			State    string `json:"state"`
		}
		if e = json.Unmarshal(b, &out); e != nil {
			return "", e
		}
		if out.Realm != "" {
			out.LoginURL = "/realms/" + out.Realm
		}
		if out.State != "unknown" && out.LoginURL != "" {
			if issuer, e = s.IssuerFromRealmLoginURL(out.LoginURL); e != nil {
				return "", e
			}
		}
		if issuer != "" && !s.OrgIssuer(issuer) {
			issuer = ""
		}
	}
	m := BrowserMetadata{Authorize: s.settings().AuthBase + "/authorize"}
	if issuer != "" {
		if m, e = s.Discover(ctx, issuer); e != nil {
			return "", e
		}
	}
	key, e := newKey(ctx)
	if e != nil {
		return "", e
	}
	s.Metadata = m
	s.Key = key
	s.State = random()
	s.Verifier = random()
	s.Redirect = redirect
	s.Started = time.Now()
	s.Tokens = BrowserToken{}
	s.Profile = BrowserProfile{}
	s.Certificate = Certificate{}
	challenge := sha256.Sum256([]byte(s.Verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {s.settings().ClientID}, "redirect_uri": {redirect}, "scope": {"openid email profile groups"}, "resource": {s.settings().IdentityResource}, "state": {s.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	// A caller-pinned realm keeps the hint; a looked-up one, like `wendy auth
	// login`, does not need it.
	if email != "" && (issuer == "" || pinned) {
		q.Set("login_hint", email)
	}
	return m.Authorize + "?" + strings.ReplaceAll(q.Encode(), "+", "%20"), nil
}

// Exchange performs the browser DPoP exchange and enforces a refreshable session.
func (s *BrowserSession) Exchange(ctx context.Context, form url.Values) (BrowserToken, error) {
	form.Set("client_id", s.settings().ClientID)
	nonce := ""
	for attempt := 0; attempt < 2; attempt++ {
		proof, e := s.Client.DPoPProof(ctx, s.Key, "POST", s.Metadata.Token, nonce)
		if e != nil {
			return BrowserToken{}, e
		}
		b, r, e := s.request(ctx, "POST", s.Metadata.Token, form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "DPoP": proof})
		if e != nil {
			return BrowserToken{}, e
		}
		if r.StatusCode != 200 {
			var problem struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(b, &problem)
			if attempt == 0 && problem.Error == "use_dpop_nonce" && r.Header.Get("DPoP-Nonce") != "" {
				nonce = r.Header.Get("DPoP-Nonce")
				continue
			}
			return BrowserToken{}, fmt.Errorf("Token exchange failed (%d): %s", r.StatusCode, problem.Error)
		}
		var t BrowserToken
		if e = json.Unmarshal(b, &t); e != nil {
			return t, e
		}
		if t.Access == "" || t.Refresh == "" || !strings.EqualFold(t.Type, "DPoP") {
			return t, errors.New("Auth server did not return a DPoP session and refresh token")
		}
		return t, nil
	}
	return BrowserToken{}, errors.New("DPoP nonce retry failed")
}

// Complete consumes a callback once and establishes the operator and Cloud session.
func (s *BrowserSession) Complete(ctx context.Context, code, state, issuer string) (BrowserProfile, error) {
	if s.State == "" || subtle.ConstantTimeCompare([]byte(state), []byte(s.State)) != 1 {
		return BrowserProfile{}, errors.New("Login state mismatch; start sign-in again")
	}
	if time.Since(s.Started) > 10*time.Minute {
		s.State = ""
		return BrowserProfile{}, errors.New("Login expired; start sign-in again")
	}
	s.State = "" // Consume before network access, including failed code exchanges.
	if s.Metadata.Issuer != "" && issuer != s.Metadata.Issuer {
		return BrowserProfile{}, errors.New("Authentication issuer changed; start sign-in again")
	}
	if s.Metadata.Issuer == "" && !s.OrgIssuer(issuer) {
		return BrowserProfile{}, errors.New("Sign-in did not identify a valid organization realm; start sign-in again")
	}
	if code == "" {
		return BrowserProfile{}, errors.New("No authorization code received")
	}
	m, e := s.Discover(ctx, issuer)
	if e != nil {
		return BrowserProfile{}, e
	}
	s.Metadata = m
	t, e := s.Exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {s.Verifier}, "redirect_uri": {s.Redirect}, "resource": {s.settings().IdentityResource}})
	if e != nil {
		return BrowserProfile{}, e
	}
	claims, e := s.VerifyAccess(ctx, t.Access, s.settings().IdentityResource)
	if e != nil {
		return BrowserProfile{}, e
	}
	subject, _ := claims["sub"].(string)
	tenant, _ := claims["tenant_uuid"].(string)
	if subject == "" || !canonicalUUID(tenant) {
		return BrowserProfile{}, errors.New("Token is missing its operator or tenant identity")
	}
	certificate, e := s.Client.RequestIdentityCertificate(ctx, s.settings().IdentityEndpoint, s.Key, s.Key, t.Access, tenant, subject)
	if e != nil {
		return BrowserProfile{}, e
	}
	cloud, e := s.Exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}, "resource": {s.settings().CloudResource}})
	if e != nil {
		return BrowserProfile{}, e
	}
	cloudClaims, e := s.VerifyAccess(ctx, cloud.Access, s.settings().CloudResource)
	if e != nil {
		return BrowserProfile{}, e
	}
	if cloudClaims["sub"] != subject || cloudClaims["tenant_uuid"] != tenant {
		return BrowserProfile{}, errors.New("Cloud token identity changed")
	}
	name, _ := claims["email"].(string)
	if name == "" {
		name = "Signed in"
	}
	s.Certificate = certificate
	s.Tokens = cloud
	email, _ := claims["email"].(string)
	s.Profile = BrowserProfile{Email: email, Name: name, Subject: subject, Tenant: tenant, Issuer: issuer, Principal: certificate.PrincipalURI, Expires: time.Now().Add(time.Duration(cloud.Expires) * time.Second).UTC().Format(time.RFC3339)}
	s.Verifier = ""
	if err := s.LoadUserInfo(ctx); err != nil {
		s.Profile.ProfileWarning = "Email lookup unavailable: " + err.Error()
	}

	return s.Profile, nil
}

// Verify both BrowserToken signature and sender binding before displaying a signed-in identity.

// VerifyAccess checks the browser token signature, key binding and hosted MCP policy.
func (s *BrowserSession) VerifyAccess(ctx context.Context, raw, audience string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("Invalid access token")
	}
	decode := base64.RawURLEncoding.DecodeString
	h, e := decode(parts[0])
	if e != nil {
		return nil, e
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if e = json.Unmarshal(h, &header); e != nil {
		return nil, e
	}
	sig, e := decode(parts[2])
	if e != nil {
		return nil, e
	}
	body, r, e := s.request(ctx, "GET", s.Metadata.JWKS, "", nil)
	if e != nil {
		return nil, e
	}
	if r.StatusCode != 200 {
		return nil, errors.New("Could not load issuer signing keys")
	}
	var jwks struct {
		Keys []map[string]string `json:"keys"`
	}
	if e = json.Unmarshal(body, &jwks); e != nil {
		return nil, e
	}
	verified := false
	message := []byte(parts[0] + "." + parts[1])
	for _, j := range jwks.Keys {
		if j["kid"] != header.Kid || j["alg"] != header.Alg {
			continue
		}
		if s.Client.Verifier == nil {
			return nil, errors.New("token signature verifier is required")
		}
		verifyErr := s.Client.Verifier.VerifySignature(ctx, j, message, sig)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		verified = verifyErr == nil
		if verified {
			break
		}
	}
	if !verified {
		return nil, errors.New("Access token signature verification failed")
	}
	payload, e := decode(parts[1])
	if e != nil {
		return nil, e
	}
	var claims map[string]any
	if e = json.Unmarshal(payload, &claims); e != nil {
		return nil, e
	}
	audOK := claims["aud"] == audience
	if a, ok := claims["aud"].([]any); ok {
		for _, v := range a {
			if v == audience {
				audOK = true
			}
		}
	}
	exp, _ := claims["exp"].(float64)
	if claims["iss"] != s.Metadata.Issuer || !audOK || exp <= float64(time.Now().Unix()) {
		return nil, errors.New("Access token issuer, audience or expiry is invalid")
	}
	thumb, e := OperatorJWKThumbprint(ctx, s.Key)
	if e != nil {
		return nil, e
	}
	cnf, _ := claims["cnf"].(map[string]any)
	if cnf["jkt"] != thumb {
		return nil, errors.New("Access token belongs to a different operator key")
	}
	if s.RequireHostedMCPPolicy {
		allowMCP, _ := claims["allow_mcp"].(bool)
		allowHostedMCP, _ := claims["allow_hosted_mcp"].(bool)
		if !allowMCP || !allowHostedMCP {
			return nil, errors.New("Wendy Cloud organization does not allow hosted MCP access")
		}
	}
	return claims, nil
}

func canonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IssuerFromRealmLoginURL resolves realm discovery only against the pinned auth host.
func (s *BrowserSession) IssuerFromRealmLoginURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" {
		return "", errors.New("Untrusted realm login URL")
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		base, _ := url.Parse(s.settings().AuthBase)
		u = base.ResolveReference(u)
	}
	base, _ := url.Parse(s.settings().AuthBase)
	if base == nil || u.Scheme != "https" || u.Host != base.Host {
		return "", errors.New("Untrusted realm login URL")
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) < 3 || parts[1] != "realms" || parts[2] == "" {
		return "", errors.New("Realm lookup did not return an issuer")
	}
	for _, c := range parts[2] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", errors.New("Realm lookup returned an invalid realm")
		}
	}
	return s.settings().AuthBase + "/realms/" + parts[2], nil
}

// UserInfo carries scope-gated email claims that access tokens may omit.
// Bind both the request and response to the authenticated operator.

// LoadUserInfo loads email claims bound to the authenticated operator subject.
func (s *BrowserSession) LoadUserInfo(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	endpoint := s.Metadata.Issuer + "/userinfo"
	proof, err := s.Client.DPoPAccessProof(ctx, s.Key, "GET", endpoint, s.Tokens.Access)
	if err != nil {
		return err
	}
	body, resp, err := s.request(ctx, "GET", endpoint, "", map[string]string{"Authorization": "DPoP " + s.Tokens.Access, "DPoP": proof})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("UserInfo returned %d", resp.StatusCode)
	}
	var info struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return err
	}
	if info.Subject != s.Profile.Subject {
		return errors.New("UserInfo subject does not match the signed-in operator")
	}
	if info.Email == "" {
		return errors.New("UserInfo did not include an email")
	}
	s.Profile.Email = info.Email
	s.Profile.Name = info.Email
	return nil
}

// RefreshCloudToken rotates only when the browser token has less than a minute remaining.
func (s *BrowserSession) RefreshCloudToken(ctx context.Context) error {
	expiry, _ := time.Parse(time.RFC3339, s.Profile.Expires)
	if time.Until(expiry) >= time.Minute {
		return nil
	}
	return s.RotateCloudToken(ctx)
}

// rotateCloudToken exchanges the refresh BrowserToken now, whatever the access BrowserToken's
// remaining lifetime. The issuer rotates the refresh BrowserToken on every exchange.

// RotateCloudToken rotates now and checks the same operator and tenant before updating state.
func (s *BrowserSession) RotateCloudToken(ctx context.Context) error {
	refreshed, err := s.Exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.Tokens.Refresh}, "resource": {s.settings().CloudResource}})
	if err != nil {
		return err
	}
	claims, err := s.VerifyAccess(ctx, refreshed.Access, s.settings().CloudResource)
	if err != nil {
		return err
	}
	if claims["sub"] != s.Profile.Subject || claims["tenant_uuid"] != s.Profile.Tenant {
		return errors.New("Refreshed session identity changed")
	}
	s.Tokens = refreshed
	s.Profile.Expires = time.Now().Add(time.Duration(refreshed.Expires) * time.Second).UTC().Format(time.RFC3339)
	return nil
}
