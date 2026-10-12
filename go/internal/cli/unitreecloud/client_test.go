package unitreecloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

type cloudHTTPFunc func(*fhttp.Request) (*fhttp.Response, error)

func (f cloudHTTPFunc) Do(req *fhttp.Request) (*fhttp.Response, error) { return f(req) }

func TestCloudLoginFetchesBoundGo2Keys(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if r.Header.Get("AppName") != "Go2" || r.Header.Get("AppVersion") != "1.11.4" {
			t.Errorf("wrong Unitree app headers: AppName=%q AppVersion=%q", r.Header.Get("AppName"), r.Header.Get("AppVersion"))
		}
		ts, nonce := r.Header.Get("AppTimestamp"), r.Header.Get("AppNonce")
		if len(nonce) != 32 || ts == "" || r.Header.Get("AppSign") != md5Hex(cloudSignSecret+ts+nonce) {
			t.Error("invalid signed Unitree request")
		}
		switch r.URL.Path {
		case "/login/email":
			if r.Method != http.MethodPost || r.Header.Get("Token") != "" {
				t.Error("login must POST without token")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("email") != "robot@example.com" || r.Form.Get("password") != md5Hex("secret password") {
				t.Error("incorrect email/password form encoding")
			}
			fmt.Fprint(w, `{"code":100,"data":{"accessToken":"test-token","refreshToken":"never-save-me"}}`)
		case "/device/bind/list":
			if r.Method != http.MethodGet || r.Header.Get("Token") != "test-token" {
				t.Error("device list must use the login token")
			}
			fmt.Fprintf(w, `{"code":100,"data":[{"sn":"B42TEST","alias":"Rover","series":"Go2","key":%q},{"sn":"E21TEST","series":"G1","key":%q},{"sn":"B43TEST","series":"Go2","gcm_key":%q},{"sn":"OLDTEST","series":"Go2"}]}`, key, key, key)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c, err := NewClient("global")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = server.URL + "/"
	account, err := c.Login(context.Background(), " robot@example.com ", "secret password")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || len(account.Devices) != 3 || account.Token != "test-token" {
		t.Fatalf("unexpected login result: requests=%v devices=%d", requests, len(account.Devices))
	}
	if account.Devices[0].Serial != "B42TEST" || account.Devices[0].Name != "Rover" || account.Devices[0].EncryptionKey != key || account.Devices[1].EncryptionKey != key {
		t.Error("bound device identity/key was not decoded")
	}
	raw, err := json.Marshal(account)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{key, "test-token", "secret password", "never-save-me"} {
		if strings.Contains(string(raw), secret) {
			t.Error("public account JSON exposed a secret")
		}
	}
}

func TestCloudRejectsFailuresWithoutEchoingServerSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"credentials", 200, `{"code":1001,"errorMsg":"SECRET"}`},
		{"http", 403, "SECRET"},
		{"html", 200, "<html>SECRET</html>"},
		{"token", 200, `{"code":100,"data":{}}`},
		{"large", 200, strings.Repeat("SECRET", 200000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c, err := NewClient("global")
			if err != nil {
				t.Fatal(err)
			}
			c.baseURL = server.URL + "/"
			_, err = c.Login(context.Background(), "me@example.com", "password")
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("expected a redacted failure, got %v", err)
			}
		})
	}
}

func TestCloudDoesNotFollowRedirects(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = server.URL + "/"
	_, err = c.Login(context.Background(), "me@example.com", "password")
	if err == nil || leaked {
		t.Fatal("cloud request followed a credential-bearing redirect")
	}
}

func TestCloudRegionsAndCanceledLogin(t *testing.T) {
	for _, region := range []string{"", "global", "cn"} {
		client, err := NewClient(region)
		if err != nil || !strings.HasPrefix(client.baseURL, "https://") {
			t.Fatalf("region %q did not use HTTPS", region)
		}
	}
	if _, err := NewClient("https://other.example"); err == nil {
		t.Fatal("accepted an arbitrary credential endpoint")
	}
	c, _ := NewClient("global")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := c.Login(ctx, "me@example.com", "password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled login did not preserve context error: %v", err)
	}
}

func TestCloudTransportErrorCannotLeakCredentials(t *testing.T) {
	c := &Client{baseURL: "https://global-robot-api.unitree.com/", http: cloudHTTPFunc(func(req *fhttp.Request) (*fhttp.Response, error) {
		return nil, errors.New("transport echoed password-secret token-secret key-secret")
	})}
	_, err := c.Login(context.Background(), "me@example.com", "password-secret")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport error was not redacted: %v", err)
	}
}
