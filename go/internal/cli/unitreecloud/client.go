// Package unitreecloud accesses Unitree accounts from the CLI only.
// Keeping its mobile-TLS compatibility dependencies out of the LAN package
// avoids linking them into the agent that stores keys and connects to robots.
package unitreecloud

import (
	"context"
	"crypto/md5" // Unitree's API requires MD5 for password and request signing.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/shared/unitree"
)

const cloudSignSecret = "XyvkwK45hp5PHfA8"

type cloudHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Client accesses the Unitree Go account namespace.
type Client struct {
	baseURL string
	http    cloudHTTPClient
}

// NewClient selects the account's registration region, "global" or "cn".
// It matches the mobile client's TLS fingerprint, required by the cloud API.
func NewClient(region string) (*Client, error) {
	var endpoint string
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", "global":
		endpoint = "https://global-robot-api.unitree.com/"
	case "cn":
		endpoint = "https://robot-api.unitree.com/"
	default:
		return nil, errors.New("Unitree region must be global or cn")
	}
	c, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithClientProfile(profiles.Chrome_120),
		tlsclient.WithNotFollowRedirects(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating Unitree cloud client: %w", err)
	}
	return &Client{baseURL: endpoint, http: c}, nil
}

// Login logs in and fetches robots already bound to this Unitree account.
// A robot must first be bound using the official Unitree Go app.
func (c *Client) Login(ctx context.Context, email, password string) (*unitree.Account, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return nil, errors.New("Unitree email and password are required")
	}
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	if err := c.request(ctx, http.MethodPost, "login/email", "", url.Values{
		"email":    {email},
		"password": {md5Hex(password)},
	}, &login); err != nil {
		return nil, err
	}
	if login.AccessToken == "" {
		return nil, errors.New("Unitree login returned no access token")
	}
	var bound []struct {
		Serial string `json:"sn"`
		Alias  string `json:"alias"`
		Series string `json:"series"`
		Model  string `json:"model"`
		Key    string `json:"key"`
		GCMKey string `json:"gcm_key"`
	}
	if err := c.request(ctx, http.MethodGet, "device/bind/list", login.AccessToken, nil, &bound); err != nil {
		return nil, err
	}
	account := &unitree.Account{Token: login.AccessToken}
	seen := make(map[string]bool)
	for _, item := range bound {
		if item.Series != "" && !strings.EqualFold(item.Series, "Go2") {
			continue
		}
		serial := strings.TrimSpace(item.Serial)
		if !unitree.ValidSerial(serial) || seen[serial] {
			continue
		}
		key := item.Key
		if key == "" {
			key = item.GCMKey
		}
		if key != "" && !unitree.ValidEncryptionKey(key) {
			return nil, errors.New("Unitree returned an invalid device encryption key")
		}
		name := strings.TrimSpace(item.Alias)
		if name == "" {
			name = "Unitree Go2"
		}
		account.Devices = append(account.Devices, unitree.Device{
			Serial: serial, Name: name, Model: item.Model, EncryptionKey: key,
		})
		seen[serial] = true
	}
	return account, nil
}

func (c *Client) request(ctx context.Context, method, path, token string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("creating Unitree request: %w", err)
	}
	now := time.Now()
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	nonce := strings.ReplaceAll(uuid.NewString(), "-", "")
	zone, _ := now.Zone()
	req.Header = http.Header{
		"Content-Type":   {"application/x-www-form-urlencoded"},
		"DeviceId":       {"Samsung/Samsung/SM-S931B/s24/14/34"},
		"DevicePlatform": {"Android"},
		"DeviceModel":    {"SM-S931B"},
		"SystemVersion":  {"34"},
		"AppVersion":     {"1.11.4"},
		"AppLocale":      {"en_US"},
		"Channel":        {"UMENG_CHANNEL"},
		"User-Agent":     {"Mozilla/5.0 (Linux; Android 14; SM-S931B Build/AP3A.240905.015.A2; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/127.0.6533.103 Mobile Safari/537.36"},
		"AppTimezone":    {zone},
		"AppTimestamp":   {ts},
		"AppNonce":       {nonce},
		"AppSign":        {md5Hex(cloudSignSecret + ts + nonce)},
		"AppName":        {"Go2"},
		"Token":          {token},
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Unitree %s request failed: %w", path, ctx.Err())
		}
		// Transport errors can include request headers or echoed content.
		return fmt.Errorf("Unitree %s could not reach the cloud service", path)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Never echo response bodies, which may contain credentials or keys.
		return fmt.Errorf("Unitree %s returned HTTP %d", path, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("reading Unitree %s response: %w", path, ctx.Err())
		}
		return fmt.Errorf("Unitree %s response could not be read", path)
	}
	if len(raw) > 1<<20 {
		return errors.New("Unitree cloud response exceeds 1 MiB")
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return fmt.Errorf("Unitree %s returned an invalid response", path)
	}
	if envelope.Code != 100 {
		return fmt.Errorf("Unitree %s failed (code %d); check your account credentials and region", path, envelope.Code)
	}
	if json.Unmarshal(envelope.Data, out) != nil {
		return fmt.Errorf("Unitree %s returned invalid data", path)
	}
	return nil
}

func md5Hex(value string) string {
	digest := md5.Sum([]byte(value))
	return hex.EncodeToString(digest[:])
}
