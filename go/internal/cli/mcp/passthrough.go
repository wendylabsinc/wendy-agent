package mcp

import (
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// uiAppPrefix namespaces container app ui:// resources surfaced through Wendy.
const uiAppPrefix = "ui://app/"

// namespacedUIURI builds the host-visible URI for an app's main UI entry point.
func namespacedUIURI(app string) string {
	return uiAppPrefix + sanitizeMCPPrefix(app) + "/main"
}

// namespacedUIURI2 wraps an inner container ui:// URI under the app namespace.
func namespacedUIURI2(app, inner string) string {
	return uiAppPrefix + sanitizeMCPPrefix(app) + "/" + strings.TrimPrefix(inner, "ui://")
}

// parseNamespacedUIURI splits a namespaced URI back into (sanitizedApp, innerURI).
func parseNamespacedUIURI(uri string) (app, inner string, ok bool) {
	rest, found := strings.CutPrefix(uri, uiAppPrefix)
	if !found {
		return "", "", false
	}
	app, path, found := strings.Cut(rest, "/")
	if !found {
		return "", "", false
	}
	return app, "ui://" + path, true
}

// rewriteToolUIMeta namespaces a proxied tool's _meta.ui.resourceUri (if any).
func rewriteToolUIMeta(tool *mcpgo.Tool, app string) {
	if tool.Meta == nil || tool.Meta.AdditionalFields == nil {
		return
	}
	ui, ok := tool.Meta.AdditionalFields["ui"].(map[string]any)
	if !ok {
		return
	}
	if inner, ok := ui["resourceUri"].(string); ok && strings.HasPrefix(inner, "ui://") {
		ui["resourceUri"] = namespacedUIURI2(app, inner)
	}
}
