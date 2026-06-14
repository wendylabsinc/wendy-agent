// Package appsui implements the MCP Apps extension wire shape (ui:// resources
// and _meta.ui annotations) for the Wendy MCP server. It is the single place
// the extension's metadata format is defined.
package appsui

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UIResourceOptions configures a ui:// resource.
type UIResourceOptions struct {
	// CSP lists external origins the app may load resources from.
	CSP []string
	// Permissions lists host capabilities the app requests (e.g. "camera").
	Permissions []string
	// Description is shown to hosts listing resources.
	Description string
}

// uiMeta builds the _meta.ui object for a resource URI.
func uiMeta(resourceURI string, opts *UIResourceOptions) map[string]any {
	ui := map[string]any{"resourceUri": resourceURI}
	if opts != nil {
		if len(opts.CSP) > 0 {
			ui["csp"] = opts.CSP
		}
		if len(opts.Permissions) > 0 {
			ui["permissions"] = opts.Permissions
		}
	}
	return ui
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

// WithUI annotates a tool so hosts render the given ui:// resource for it.
func WithUI(tool mcpgo.Tool, resourceURI string) mcpgo.Tool {
	tool.Meta = ensureMeta(tool.Meta)
	tool.Meta.AdditionalFields["ui"] = uiMeta(resourceURI, nil)
	return tool
}

// ResultWithUI annotates a tool result with the ui:// resource to render plus
// the view name and data the iframe should display.
func ResultWithUI(res *mcpgo.CallToolResult, resourceURI, view string, data any) *mcpgo.CallToolResult {
	res.Meta = ensureMeta(res.Meta)
	res.Meta.AdditionalFields["ui"] = uiMeta(resourceURI, nil)
	res.StructuredContent = map[string]any{"view": view, "data": data}
	return res
}

// RegisterUIResource serves html at resourceURI as text/html, recording any
// CSP/permissions in the resource's _meta.ui for hosts that read them there.
func RegisterUIResource(srv *server.MCPServer, resourceURI, name string, html []byte, opts *UIResourceOptions) {
	desc := "WendyOS interactive app UI"
	if opts != nil && opts.Description != "" {
		desc = opts.Description
	}
	res := mcpgo.NewResource(resourceURI, name,
		mcpgo.WithResourceDescription(desc),
		mcpgo.WithMIMEType("text/html"),
	)
	res.Meta = &mcpgo.Meta{AdditionalFields: map[string]any{"ui": uiMeta(resourceURI, opts)}}
	srv.AddResource(res, func(_ context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		return []mcpgo.ResourceContents{
			mcpgo.TextResourceContents{URI: req.Params.URI, MIMEType: "text/html", Text: string(html)},
		}, nil
	})
}
