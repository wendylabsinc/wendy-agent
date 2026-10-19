package cloudlink

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func TestAuthorizationResponsesIdentifyIssuer(t *testing.T) {
	m := newFixture(t, fixtureConfig(t))
	w := request(m, "GET", "/.well-known/oauth-authorization-server", nil, nil, "")
	var metadata map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["issuer"] != m.origin || metadata["authorization_response_iss_parameter_supported"] != true {
		t.Fatal("metadata must advertise issuer identification", metadata)
	}

	for _, test := range []struct {
		name, approve, upstreamCode, upstreamError, wantError string
	}{
		{name: "consent declined", approve: "no", wantError: "access_denied"},
		{name: "upstream denied", approve: "yes", upstreamError: "access_denied", wantError: "access_denied"},
		{name: "upstream exchange failed", approve: "yes", upstreamCode: "invalid", wantError: "server_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cookie, csrf := begin(t, m)
			w := request(m, "POST", "/oauth/authorize", url.Values{
				"transaction": {cookie.Value}, "csrf": {csrf}, "email": {"alice@example.com"}, "approve": {test.approve},
			}, cookie, m.origin)
			if w.Code != http.StatusSeeOther {
				t.Fatalf("approval returned %d: %s", w.Code, w.Body.String())
			}
			if test.approve == "yes" {
				callback := url.Values{"state": {"upstream-state"}, "iss": {"https://auth.example/realms/system"}}
				if test.upstreamError != "" {
					callback.Set("error", test.upstreamError)
				} else {
					callback.Set("code", test.upstreamCode)
				}
				w = request(m, "GET", "/auth/callback?"+callback.Encode(), nil, cookie, "")
			}
			redirect, err := url.Parse(w.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			q := redirect.Query()
			if w.Code != http.StatusSeeOther || q.Get("iss") != m.origin || q.Get("state") != "downstream-state" || q.Get("error") != test.wantError || q.Get("code") != "" {
				t.Fatalf("invalid error redirect: %d %s", w.Code, redirect)
			}
		})
	}
	// The same issuer also identifies successful code responses.
	linkCode(t, m, "alice@example.com")
}
