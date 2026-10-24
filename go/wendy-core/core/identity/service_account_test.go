package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServiceAccountAssertionAndEnrollmentUseHostSignatures(t *testing.T) {
	ctx := context.Background()
	key := testSigner{testKey(t)}
	jwk, _, err := OperatorPublicJWK(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := OperatorJWKThumbprint(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	previousJTI := ""
	client := &Client{Entropy: testEntropy{}, HTTP: testHTTP(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/realms/acme/.well-known/openid-configuration":
			return jsonResponse(200, ProviderMetadata{Issuer: fixtureIssuer, AuthorizationEndpoint: fixtureIssuer + "/authorize", TokenEndpoint: fixtureIssuer + "/oauth2/token"}), nil
		case "/realms/acme/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != jwtBearerGrantType || r.Form.Has("client_id") || r.Form.Get("resource") != "https://cloud.example/api" {
				t.Fatal("assertion grant changed")
			}
			parts := strings.Split(r.Form.Get("assertion"), ".")
			if len(parts) != 3 {
				t.Fatal("invalid assertion")
			}
			header, _ := base64.RawURLEncoding.DecodeString(parts[0])
			var h map[string]any
			_ = json.Unmarshal(header, &h)
			claims, err := DecodeJWTClaims(r.Form.Get("assertion"))
			if err != nil {
				t.Fatal(err)
			}
			signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
			if h["alg"] != "ML-DSA-65" || h["kid"] != thumb || claims["iss"] != "machine" || claims["sub"] != "machine" || claims["aud"] != fixtureIssuer+"/oauth2/token" || !verifyMLDSA65JWK(jwk, []byte(parts[0]+"."+parts[1]), signature) {
				t.Fatal("assertion binding changed")
			}
			jti, _ := claims["jti"].(string)
			if jti == "" || jti == previousJTI || claims["exp"].(float64)-claims["iat"].(float64) != 60 {
				t.Fatal("assertion freshness changed")
			}
			previousJTI = jti
			calls++
		case "/realms/acme/service-accounts/enroll":
			var body struct {
				Secret string            `json:"enrollment_secret"`
				JWK    map[string]string `json:"public_jwk"`
				Proof  string            `json:"pop"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Secret != "one-time-secret" || body.JWK["pub"] != jwk["pub"] {
				t.Fatal("enrollment key binding changed")
			}
			claims, err := DecodeJWTClaims(body.Proof)
			if err != nil || claims["htu"] != fixtureIssuer+"/service-accounts/enroll" || claims["htm"] != "POST" {
				t.Fatal("enrollment proof target changed")
			}
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
		claims, _ := json.Marshal(map[string]any{"iss": fixtureIssuer, "sub": "machine", "aud": "https://cloud.example/api", "exp": time.Now().Add(time.Hour).Unix(), "cnf": map[string]string{"jkt": thumb}})
		return jsonResponse(200, TokenResponse{AccessToken: "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"}), nil
	})}
	for i := 0; i < 2; i++ {
		if _, err := client.MintServiceAccountToken(ctx, key, fixtureIssuer, "machine", "https://cloud.example/api"); err != nil {
			t.Fatal(err)
		}
	}
	subject, err := client.EnrollServiceAccount(ctx, key, fixtureIssuer, "one-time-secret")
	if err != nil || subject != "machine" || calls != 2 {
		t.Fatalf("headless flow: %q, %v", subject, err)
	}
}
