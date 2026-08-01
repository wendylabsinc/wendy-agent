package commands

// shouldOfferWendyLiteESPIDFScaffold reports whether wendy run should offer to
// scaffold Wendy Lite ESP-IDF components + a wendy.json for the current
// project. True only when wendy.json is missing, the project directory looks
// like an ESP-IDF project ("esp-idf" projectType, per detectProjectType), and
// the already-resolved run target is a USB-connected wendy-lite provider
// device.
func shouldOfferWendyLiteESPIDFScaffold(cfgMissing bool, projectType string, target *SelectedDevice) bool {
	if !cfgMissing || projectType != "esp-idf" || target == nil {
		return false
	}
	if target.External == nil || target.Provider == nil {
		return false
	}
	if target.Provider.Key() != "wendy-lite" {
		return false
	}
	return target.External.ConnectionType() == "USB"
}
