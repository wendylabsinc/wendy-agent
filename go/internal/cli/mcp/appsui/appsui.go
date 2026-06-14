// Package appsui implements the MCP Apps extension wire shape (ui:// resources
// and _meta.ui annotations) for the Wendy MCP server. It is the single place
// the extension's metadata format is defined.
package appsui

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MIMEType is the MIME type MCP Apps hosts require for a ui:// resource's HTML.
// Hosts (e.g. Claude Desktop) reject bare "text/html" with "Unsupported UI
// resource content format" — it must carry the mcp-app profile. This matches
// the RESOURCE_MIME_TYPE constant in @modelcontextprotocol/ext-apps.
const MIMEType = "text/html;profile=mcp-app"

// UIResourceOptions configures a ui:// resource. csp/permissions belong on the
// resource's _meta.ui (per the MCP Apps spec); the tool's _meta.ui carries only
// the resourceUri.
type UIResourceOptions struct {
	// Description is shown to hosts listing resources.
	Description string
	// Permissions are browser capabilities the sandboxed UI requests. Valid
	// names: "camera", "microphone", "geolocation", "clipboardWrite".
	Permissions []string
	// CSP origins the sandboxed UI may reach (empty = none allowed).
	CSPConnectDomains  []string
	CSPResourceDomains []string
	CSPFrameDomains    []string
}

func ensureMeta(m *mcpgo.Meta) *mcpgo.Meta {
	if m == nil {
		m = &mcpgo.Meta{}
	}
	if m.AdditionalFields == nil {
		m.AdditionalFields = map[string]any{}
	}
	return m
}

// resourceUIMeta builds the resource-level _meta.ui (csp + permissions as
// OBJECTS, matching @modelcontextprotocol/ext-apps), or nil if nothing is
// requested. NB: permissions is an object ({"microphone":{}}), not an array,
// and csp is an object of domain lists — hosts reject the array forms.
func resourceUIMeta(opts *UIResourceOptions) map[string]any {
	if opts == nil {
		return nil
	}
	ui := map[string]any{}
	csp := map[string]any{}
	if len(opts.CSPConnectDomains) > 0 {
		csp["connectDomains"] = opts.CSPConnectDomains
	}
	if len(opts.CSPResourceDomains) > 0 {
		csp["resourceDomains"] = opts.CSPResourceDomains
	}
	if len(opts.CSPFrameDomains) > 0 {
		csp["frameDomains"] = opts.CSPFrameDomains
	}
	if len(csp) > 0 {
		ui["csp"] = csp
	}
	if len(opts.Permissions) > 0 {
		perms := map[string]any{}
		for _, p := range opts.Permissions {
			perms[p] = map[string]any{}
		}
		ui["permissions"] = perms
	}
	if len(ui) == 0 {
		return nil
	}
	return ui
}

// WithUI annotates a tool so hosts render the given ui:// resource for it. The
// tool's _meta.ui carries only the resourceUri.
func WithUI(tool mcpgo.Tool, resourceURI string) mcpgo.Tool {
	tool.Meta = ensureMeta(tool.Meta)
	tool.Meta.AdditionalFields["ui"] = map[string]any{"resourceUri": resourceURI}
	return tool
}

// ResultWithUI annotates a tool result with the ui:// resource to render plus
// the view name and data the iframe should display.
func ResultWithUI(res *mcpgo.CallToolResult, resourceURI, view string, data any) *mcpgo.CallToolResult {
	res.Meta = ensureMeta(res.Meta)
	res.Meta.AdditionalFields["ui"] = map[string]any{"resourceUri": resourceURI}
	res.StructuredContent = map[string]any{"view": view, "data": data}
	return res
}

// RegisterUIResource serves html at resourceURI with the MCP Apps MIME type.
// Any csp/permissions go on the resource's _meta.ui (in object form); when none
// are requested no _meta.ui is set, so strict host schemas don't reject it.
func RegisterUIResource(srv *server.MCPServer, resourceURI, name string, html []byte, opts *UIResourceOptions) {
	desc := "WendyOS interactive app UI"
	if opts != nil && opts.Description != "" {
		desc = opts.Description
	}
	res := mcpgo.NewResource(resourceURI, name,
		mcpgo.WithResourceDescription(desc),
		mcpgo.WithMIMEType(MIMEType),
	)
	if ui := resourceUIMeta(opts); ui != nil {
		res.Meta = &mcpgo.Meta{AdditionalFields: map[string]any{"ui": ui}}
	}
	srv.AddResource(res, func(_ context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		return []mcpgo.ResourceContents{
			mcpgo.TextResourceContents{URI: req.Params.URI, MIMEType: MIMEType, Text: string(html)},
		}, nil
	})
}
