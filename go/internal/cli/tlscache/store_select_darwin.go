package tlscache

func newPlatformStore() sessionStore { return newKeychainStore() }
