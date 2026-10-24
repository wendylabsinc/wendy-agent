package commands

import (
	"context"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/identityhost"
)

func TestLiteIdentityCapabilityRequiresProfile(t *testing.T) {
	keyPEM, key := newP256KeyPEM(t)
	capability, err := identityhost.IdentityKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	checkedKey := liteOperatorIdentityKey{capability}
	principal := "spiffe://wendy.sh/tenant/" + testOperatorTenant + "/operator/" + testOperatorSubject
	for _, marked := range []bool{true, false} {
		leaf := liteLeafPEM(t, key, principal, time.Now().Add(time.Hour), marked)
		_, err := checkedKey.CheckCertificate(context.Background(), []byte(leaf))
		if (err == nil) != marked {
			t.Fatalf("marked=%v: CheckCertificate = %v", marked, err)
		}
	}
}
