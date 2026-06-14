package mcp

import (
	"strconv"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// uiAppPrefix namespaces container app ui:// resources surfaced through Wendy.
const uiAppPrefix = "ui://app/"

// injectToolPrefix injects `window.__WENDY_TOOL_PREFIX__` into an HTML UI
// resource. Container tools are aggregated into the Wendy server under prefixed
// names (<app>__<tool>); the app's own UI calls its tools by their original
// names, so it must prepend this prefix for those calls to resolve through the
// proxy. No-op for non-HTML content; empty/standalone when not proxied.
func injectToolPrefix(html, mimeType, prefix string) string {
	if !strings.Contains(mimeType, "html") {
		return html
	}
	script := "<script>window.__WENDY_TOOL_PREFIX__=" + strconv.Quote(prefix) + ";</script>"
	if i := strings.Index(html, "<head>"); i >= 0 {
		n := i + len("<head>")
		return html[:n] + script + html[n:]
	}
	return script + html
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
