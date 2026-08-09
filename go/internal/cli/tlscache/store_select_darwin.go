package tlscache

import "github.com/wendylabsinc/wendy/go/internal/shared/secretstore"

func newPlatformStore() secretstore.Store { return secretstore.NewKeychain(keychainService) }
