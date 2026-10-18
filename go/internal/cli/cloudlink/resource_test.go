package cloudlink

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/browserauth"
)

func TestResourceRootAndMCPPathKeepExactOAuthAudience(t *testing.T) {
	for _, resourceURL := range []string{"https://gateway.example", "https://gateway.example/", "https://gateway.example/mcp"} {
		t.Run(resourceURL, func(t *testing.T) {
			m, err := New(fixtureConfig(t), resourceURL, func(string) string {
				return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
			})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if session, ok := m.newSession(&memoryStore{}).(*browserauth.Session); !ok || !session.RequireHostedMCPPolicy {
				t.Fatal("hosted Cloud connections must enforce tenant MCP policy")
			}
			for _, audience := range []string{"https://gateway.example", "https://gateway.example/", "https://gateway.example/mcp"} {
				query := authorizationQuery()
				query.Set("resource", audience)
				w := request(m, "GET", "/oauth/authorize?"+query.Encode(), nil, nil, "")
				want := http.StatusBadRequest
				if audience == resourceURL {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Errorf("resource %q returned %d, want %d", audience, w.Code, want)
				}
			}
		})
	}
}

func TestResourceRejectsAliasesAndUnsafeURLs(t *testing.T) {
	for _, resourceURL := range []string{"http://gateway.example", "https://gateway.example/other", "https://gateway.example/%6dcp", "https://gateway.example//", "https://user@gateway.example", "https://gateway.example?token=value", "https://gateway.example/#fragment"} {
		t.Run(resourceURL, func(t *testing.T) {
			m, err := New(fixtureConfig(t), resourceURL, func(string) string {
				return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
			})
			if err == nil {
				m.Close()
				t.Fatal("unsafe or unsupported MCP resource accepted")
			}
		})
	}
}
