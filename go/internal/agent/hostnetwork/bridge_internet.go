package hostnetwork

// Reserve the explicit application grant chain before mesh Internet tunnels use it.
const BridgeInternetChain = "WENDY-BRIDGE-INTERNET"

func InitBridgeInternetChain() error { return ensureChain(BridgeInternetChain) }
