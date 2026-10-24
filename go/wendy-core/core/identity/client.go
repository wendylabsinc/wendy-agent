// Package identity implements shared Wendy sign-in and session protocols.
// Hosts retain private keys, networking, signature verification, secret storage,
// refresh locking, browser interaction and presentation.
package identity

import (
	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
	"io"
	"net/url"
)

// Client uses host-supplied capabilities. Callers serialize mutations of the
// same session, including across processes sharing a refresh-token family.
type Client struct {
	Entropy  platform.Entropy
	HTTP     platform.HTTPTransport
	Verifier platform.SignatureVerifier
}

const (
	methodGet          = "GET"
	methodPost         = "POST"
	statusOK           = 200
	statusBadRequest   = 400
	statusUnauthorized = 401
)

func newHTTPRequest(method, target string, body io.Reader) (platform.HTTPRequest, error) {
	if _, err := url.Parse(target); err != nil {
		return platform.HTTPRequest{}, err
	}
	request := platform.HTTPRequest{Method: method, URL: target, Headers: make(platform.HTTPHeaders)}
	if body != nil {
		data, err := io.ReadAll(body)
		if err != nil {
			return platform.HTTPRequest{}, err
		}
		request.Body = data
	}
	return request, nil
}
