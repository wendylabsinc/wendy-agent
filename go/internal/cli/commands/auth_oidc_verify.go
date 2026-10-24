package commands

import (
	"context"
	"net/http"

	wendy "github.com/wendylabsinc/wendy-go"
	"github.com/wendylabsinc/wendy-go/identity"
	"github.com/wendylabsinc/wendy/go/internal/cli/identityhost"
)

type oidcJWK = identity.JWK
type oidcTokenVerifier = identity.TokenVerifier

func loadOIDCTokenVerifier(ctx context.Context, meta *oidcProviderMetadata) (*oidcTokenVerifier, error) {
	return identityhost.Client(http.DefaultClient).LoadTokenVerifier(ctx, meta)
}
func verifyIDToken(verifier *oidcTokenVerifier, token *oidcTokenResponse, clientID, nonce string) (string, error) {
	return identity.VerifyIDToken(context.Background(), verifier, token, clientID, nonce)
}
func verifyAccessToken(verifier *oidcTokenVerifier, raw, audience, thumbprint string) (map[string]any, error) {
	return identity.VerifyAccessToken(context.Background(), verifier, raw, audience, thumbprint)
}

func verifyES256JWK(key oidcJWK, message, signature []byte) bool {
	publicKey := make(map[string]string, len(key)+1)
	for name, value := range key {
		publicKey[name] = value
	}
	publicKey["alg"] = "ES256"
	return (wendy.GoSignatureVerifier{}).VerifySignature(context.Background(), publicKey, message, signature) == nil
}
