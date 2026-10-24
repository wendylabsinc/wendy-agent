package commands

// The interactive wendy-auth browser round-trip, token persistence, and refresh
// path. The discovery, PKCE, JWK thumbprint, and DPoP primitives live in
// auth_oidc.go.

import (
	"context"
	"crypto"
	"crypto/subtle"
	"crypto/x509"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	wendy "github.com/wendylabsinc/wendy-go"
	"github.com/wendylabsinc/wendy-go/identity"
	"github.com/wendylabsinc/wendy/go/internal/cli/identityhost"

	clitimesync "github.com/wendylabsinc/wendy/go/internal/cli/timesync"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// oidcCallbackResult carries what the redirect handler observed.
type oidcCallbackResult struct {
	Code  string
	State string
	// Iss is the RFC 9207 issuer identifier the authorization response carried,
	// naming the realm that actually issued the code. It differs from the
	// requested realm when the browser org picker (wendy-auth) switched realms.
	// Empty when the server did not send it.
	Iss string
	Err error
}

// performOIDCLogin creates and saves a refreshable Cloud API session.
//
// The DPoP key is generated once the realm (or the universal start) is settled
// and before the browser opens, because wendy-auth binds the access and
// refresh-token family to its thumbprint.
func performOIDCLogin(ctx context.Context, opts oidcLoginOptions) error {
	client := identityhost.Client(http.DefaultClient)
	attempt, err := client.PrepareLogin(ctx, identity.LoginOptions{Issuer: opts.Issuer, AuthorizationBase: opts.AuthorizationBase, LoginHint: opts.LoginHint, ClientID: opts.ClientID, CloudResource: opts.CloudResource, IdentityResource: opts.IdentityResource, IdentityEndpoint: opts.IdentityEndpoint, CloudURL: opts.CloudURL, CloudGRPC: opts.CloudGRPC})
	if err != nil {
		return err
	}
	opts.Issuer = attempt.Options.Issuer
	opts.AuthorizationBase = attempt.Options.AuthorizationBase
	// Step 2: the key, once the authorization target is settled.
	privateKeyPEM, err := certs.GenerateMLDSAKeyPair()
	if err != nil {
		return fmt.Errorf("generating operator key: %w", err)
	}
	key, err := certs.ParseSigningPrivateKeyPEM([]byte(privateKeyPEM))
	if err != nil {
		return fmt.Errorf("parsing generated key: %w", err)
	}
	thumbprint, err := operatorJWKThumbprint(key)
	if err != nil {
		return fmt.Errorf("computing key thumbprint: %w", err)
	}
	fmt.Println(tui.SuccessMessage(fmt.Sprintf("Generated operator key (jkt %s).", thumbprint)))

	// Step 3: loopback listener for the redirect.
	listener, redirectURI, err := startLoopbackListener()
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	authURL, err := attempt.BeginAuthorization(ctx, redirectURI)
	if err != nil {
		return err
	}
	state := attempt.State()

	authority := opts.Issuer
	if authority == "" {
		authority = opts.AuthorizationBase
	}
	resultCh := make(chan oidcCallbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", oidcCallbackHandler(state, signedInURL(authority), resultCh))

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			resultCh <- oidcCallbackResult{Err: fmt.Errorf("callback server: %w", serveErr)}
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// Step 4: send the operator to the realm's authorize endpoint.
	if !humanPresent() {
		printLoginURLForManualOpen(authURL)
	} else {
		fmt.Println(tui.InfoMessage("Opening your browser to sign in..."))
		fmt.Println("  " + authURL)
		if openErr := openBrowser(authURL); openErr != nil {
			fmt.Println(tui.WarningMessage("Could not open a browser automatically; open the URL above manually."))
		}
	}

	// Step 5: wait for the redirect.
	var result oidcCallbackResult
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(browserLoginTimeout):
		return browserLoginTimeoutError()
	}
	if result.Err != nil {
		return result.Err
	}

	signer, err := identityhost.OperatorKey(key)
	if err != nil {
		return err
	}
	session, err := client.CompleteLogin(ctx, attempt, identity.Callback{Code: result.Code, State: result.State, Iss: result.Iss}, signer, func(progress identity.LoginProgress) {
		fmt.Println(tui.SuccessMessage("Access token signature and sender constraint verified."))
		if opts.PrintClaims {
			printClaims(progress.Claims, progress.Token)
		}
		if progress.Token.RefreshToken != "" {
			fmt.Println(tui.InfoMessage("Requesting an operator certificate from pki-core..."))
		}
	})
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	authEntry := config.AuthConfig{
		CloudDashboard: session.CloudDashboard,
		CloudGRPC:      session.CloudGRPC,
		APIKey:         session.APIKey,
		OAuthIssuer:    session.OAuthIssuer,
		OAuthClientID:  session.OAuthClientID,
		OAuthResource:  session.OAuthResource,
		PKIResource:    session.PKIResource,
		PKIEndpoint:    session.PKIEndpoint,
		OAuthExpiresAt: session.OAuthExpiresAt,
		RefreshToken:   session.RefreshToken,
		DPoPPrivateKey: privateKeyPEM,
	}
	for _, certificate := range session.Certificates {
		authEntry.Certificates = append(authEntry.Certificates, identityhost.Certificate(certificate, privateKeyPEM))
	}
	cfg.AddAuth(authEntry)
	cfg.EnsureContexts()
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("saving OAuth session and certificates: %w", err)
	}
	cloudOrganizationName(ctx, &authEntry)
	fmt.Println(tui.SuccessMessage(fmt.Sprintf("Signed in to %s. API session and certificates saved.", issuerRealm(session.OAuthIssuer))))
	fmt.Println(sessionKeyLine(keyAlgorithmName(key.Public()), "OIDC"))
	clitimesync.CacheProof(ctx)
	return nil
}

type oidcHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// requestPKIIdentityCertificate implements pki-core's operator identity wire
// contract: raw PKCS#10 body, DPoP authorization, and a leaf-first PEM chain.
// privateKeyPEM is the CSR key; key signs the DPoP proof. They differ only for
// the Lite leaf (P-256 CSR proved by the ML-DSA session).
func requestPKIIdentityCertificate(ctx context.Context, client oidcHTTPDoer, endpoint, privateKeyPEM string, key crypto.Signer, accessToken, expectedTenant, subject string) (config.CertificateInfo, error) {
	if accessToken == "" {
		return config.CertificateInfo{}, fmt.Errorf("requesting pki-core identity certificate: access token is empty")
	}
	csrKey, err := identityhost.IdentityKey(privateKeyPEM)
	if err != nil {
		return config.CertificateInfo{}, fmt.Errorf("generating operator CSR: %w", err)
	}
	signer, err := identityhost.OperatorKey(key)
	if err != nil {
		return config.CertificateInfo{}, fmt.Errorf("building pki-core DPoP proof: %w", err)
	}
	certificate, err := identityhost.Client(client).RequestIdentityCertificate(ctx, endpoint, csrKey, signer, accessToken, expectedTenant, subject)
	if err != nil {
		return config.CertificateInfo{}, err
	}
	return identityhost.Certificate(certificate, privateKeyPEM), nil
}

func splitCertificateChainPEM(chain []byte) (string, string, *x509.Certificate, error) {
	return identityhost.SplitCertificateChainPEM(chain)
}

// refreshOIDCCertificate replays the direct pki-core flow for an existing
// OAuth session. The refresh-token family is first scoped to pki-core and then
// rotated back to the Cloud API resource. The DPoP key cannot change here:
// pki-core requires it to remain the CSR key as well. With lite set it issues
// the session's P-256 Lite leaf instead of the primary: same token and DPoP
// key, a fresh P-256 CSR key.
func refreshOIDCCertificate(ctx context.Context, auth *config.AuthConfig, lite bool) error {
	refresh, err := auth.OAuthRefreshToken()
	if err != nil {
		return fmt.Errorf("loading OAuth refresh token: %w", err)
	}
	if refresh == "" {
		return fmt.Errorf("OAuth session has no refresh token; sign in again")
	}
	privatePEM, err := auth.OAuthDPoPKey()
	if err != nil {
		return fmt.Errorf("loading OAuth DPoP key: %w", err)
	}
	key, err := certs.ParseSigningPrivateKeyPEM([]byte(privatePEM))
	if err != nil {
		return fmt.Errorf("parsing OAuth DPoP key: %w", err)
	}
	signer, err := identityhost.OperatorKey(key)
	if err != nil {
		return fmt.Errorf("computing OAuth DPoP thumbprint: %w", err)
	}
	csrPEM := privatePEM
	if lite {
		csrPEM, err = certs.GenerateKeyPair()
		if err != nil {
			return err
		}
	}
	csrKey, err := identityhost.IdentityKey(csrPEM)
	if err != nil {
		return err
	}
	if lite {
		csrKey = liteOperatorIdentityKey{csrKey}
	}
	session := auth.IdentitySession()
	session.RefreshToken = refresh
	err = identityhost.Client(http.DefaultClient).RefreshCertificate(ctx, &session, signer, csrKey, lite)
	if session.RefreshToken != refresh {
		auth.RefreshToken = session.RefreshToken
	}
	if err != nil {
		return err
	}
	auth.APIKey = session.APIKey
	auth.OAuthExpiresAt = session.OAuthExpiresAt
	auth.PKIResource = session.PKIResource
	auth.PKIEndpoint = session.PKIEndpoint
	if lite {
		auth.LiteCertificate = session.LiteCertificate
		auth.LitePrivateKey = csrPEM
	} else {
		auth.Certificates = nil
		for _, certificate := range session.Certificates {
			auth.Certificates = append(auth.Certificates, identityhost.Certificate(certificate, privatePEM))
		}
	}
	clitimesync.CacheProof(ctx)
	return nil
}

// Keep the Lite certificate profile check at the host certificate boundary,
// before Core rotates back to the Cloud audience.
type liteOperatorIdentityKey struct{ wendy.IdentityKey }

func (key liteOperatorIdentityKey) CheckCertificate(ctx context.Context, chain []byte) (wendy.CertificateIdentity, error) {
	checked, err := key.IdentityKey.CheckCertificate(ctx, chain)
	if err != nil {
		return wendy.CertificateIdentity{}, err
	}
	if err := checkLiteOperatorLeaf(checked.CertificatePEM); err != nil {
		return wendy.CertificateIdentity{}, err
	}
	return checked, nil
}

// oauthAccessTokenFreshFor reports whether the stored access token stays valid
// for longer than d.
func oauthAccessTokenFreshFor(auth *config.AuthConfig, margin time.Duration) bool {
	session := auth.IdentitySession()
	return identity.AccessTokenFreshFor(&session, margin)
}

func ensureOAuthAccessToken(ctx context.Context, auth *config.AuthConfig) error {
	if oauthAccessTokenFreshFor(auth, 90*time.Second) {
		return nil
	}
	unlock, err := acquireAuthRefreshLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := reloadOAuthSession(auth); err != nil {
		return err
	}
	// The process that held the lock may already have consumed our old token.
	if oauthAccessTokenFreshFor(auth, 90*time.Second) {
		return nil
	}
	if auth.ServiceAccount != "" {
		return remintServiceAccountSession(ctx, auth)
	}

	refresh, err := auth.OAuthRefreshToken()
	if err != nil {
		return fmt.Errorf("loading OAuth refresh token: %w", err)
	}
	if refresh == "" {
		return fmt.Errorf("OAuth session expired and has no refresh token; run 'wendy auth login --email <email>' again")
	}
	keyPEM, err := auth.OAuthDPoPKey()
	if err != nil {
		return fmt.Errorf("loading OAuth DPoP key: %w", err)
	}
	key, err := certs.ParseSigningPrivateKeyPEM([]byte(keyPEM))
	if err != nil {
		return fmt.Errorf("parsing OAuth DPoP key: %w", err)
	}
	signer, err := identityhost.OperatorKey(key)
	if err != nil {
		return fmt.Errorf("computing OAuth DPoP thumbprint: %w", err)
	}
	session := auth.IdentitySession()
	session.RefreshToken = refresh
	if err := identityhost.Client(http.DefaultClient).RefreshAccessToken(ctx, &session, signer); err != nil {
		return err
	}
	auth.APIKey = session.APIKey
	auth.OAuthExpiresAt = session.OAuthExpiresAt
	if session.RefreshToken != refresh {
		auth.RefreshToken = session.RefreshToken
	}
	return persistOAuthSession(auth)
}

func persistOAuthSession(auth *config.AuthConfig) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	for i := range cfg.Auth {
		if sameOAuthSession(&cfg.Auth[i], auth) {
			cfg.Auth[i] = *auth
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving refreshed OAuth session: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("OAuth session is no longer present in config")
}

// buildAuthorizeURL assembles the authorization request.
func buildAuthorizeURL(meta *oidcProviderMetadata, clientID, redirectURI, challenge, state, nonce, resource, loginHint string) (string, error) {
	return identity.BuildAuthorizeURL(meta, clientID, redirectURI, challenge, state, nonce, resource, loginHint)
}

// audienceContains reports whether `aud` includes want. RFC 7519 §4.1.3 permits
// either a bare string or an array.
func audienceContains(aud any, want string) bool { return identity.AudienceContains(aud, want) }

func printClaims(claims map[string]any, token *oidcTokenResponse) {
	fmt.Println()
	fmt.Println("Access token claims:")
	for _, k := range []string{"iss", "sub", "org_id", "tenant_uuid", "aud", "scope", "exp"} {
		v, ok := claims[k]
		if !ok {
			continue
		}
		// encoding/json decodes every JSON number as float64, so a numeric date
		// like exp prints as 1.785860416e+09 under %v. Render whole numbers as
		// integers, and expiries as a human-readable time as well.
		if f, isNum := v.(float64); isNum && f == math.Trunc(f) {
			if k == "exp" || k == "iat" || k == "nbf" {
				ts := time.Unix(int64(f), 0)
				fmt.Printf("  %-12s %d  (%s, in %s)\n", k, int64(f),
					ts.Format(time.RFC3339), time.Until(ts).Round(time.Second))
				continue
			}
			fmt.Printf("  %-12s %d\n", k, int64(f))
			continue
		}
		fmt.Printf("  %-12s %v\n", k, v)
	}
	if _, ok := claims["tenant_uuid"]; !ok {
		// Cloud uses this claim to map the wendy-auth realm to an organization.
		fmt.Println(tui.WarningMessage(
			"  no tenant_uuid claim — this realm is not linked to a Cloud organization."))
	}
	if token.RefreshToken != "" {
		fmt.Printf("  %-12s (present)\n", "refresh")
	}
	fmt.Println()
}

// signedInURL is wendy-auth's themed "signed in" page on the session's
// authority. It is built from the validated issuer or authorization base, never
// from the callback request, so the loopback reply cannot redirect elsewhere.
func signedInURL(authority string) string {
	u, err := url.Parse(authority)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/signed-in"
}

// oidcCallbackHandler receives the authorization response on the loopback
// redirect URI and sends the browser on to signedIn.
func oidcCallbackHandler(state, signedIn string, resultCh chan<- oidcCallbackResult) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errCode := q.Get("error"); errCode != "" {
			desc := q.Get("error_description")
			http.Error(w, "authorization failed: "+errCode, http.StatusBadRequest)
			resultCh <- oidcCallbackResult{Err: fmt.Errorf("authorization failed: %s %s", errCode, desc)}
			return
		}
		code := q.Get("code")
		gotState := q.Get("state")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- oidcCallbackResult{Err: fmt.Errorf("callback received without an authorization code")}
			return
		}
		// Constant-time compare: `state` is a CSRF defence, so treat it like a
		// secret even though a timing leak here is a stretch.
		if subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- oidcCallbackResult{Err: fmt.Errorf("state mismatch: the callback did not originate from this login attempt")}
			return
		}
		// The themed page lives on wendy-auth; the plain-text body is for
		// callers that do not follow redirects.
		w.Header().Set("Location", signedIn)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusSeeOther)
		_, _ = io.WriteString(w, "Signed in. You can close this tab and return to the terminal.\n")
		resultCh <- oidcCallbackResult{Code: code, State: gotState, Iss: q.Get("iss")}
	}
}
