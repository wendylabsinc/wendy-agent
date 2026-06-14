package mcp

// uiAppPrefix namespaces container app ui:// resources surfaced through Wendy.
const uiAppPrefix = "ui://app/"

// namespacedUIURI builds the host-visible URI for an app's main UI entry point.
func namespacedUIURI(app string) string {
	return uiAppPrefix + sanitizeMCPPrefix(app) + "/main"
}
