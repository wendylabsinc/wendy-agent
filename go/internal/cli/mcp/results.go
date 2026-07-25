package mcp

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// okResult returns a success result carrying v as structuredContent plus an
// indented-JSON text fallback for hosts that do not render structured content.
func okResult(v any) *mcpgo.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResultf(errCodeInternal, "marshaling result: %s", err.Error())
	}
	// MCP requires structuredContent to be a JSON object. Wrap anything that is
	// not one (array, scalar, null) rather than emitting a result conformant
	// clients reject outright. Recurses at most once: a map[string]any always
	// marshals to an object. Prefer okList at call sites — it gives the payload
	// a meaningful key instead of the generic one used here.
	if len(b) == 0 || b[0] != '{' {
		return okResult(map[string]any{"result": v})
	}
	return mcpgo.NewToolResultStructured(v, string(b))
}

// okList wraps a list payload under key in an object envelope. MCP requires
// structuredContent to be a JSON object, so returning a bare array makes
// conformant clients reject the whole result. A nil slice is normalized to []
// so the key is always present.
func okList[T any](key string, items []T) *mcpgo.CallToolResult {
	return okResult(map[string]any{key: listOrEmpty(items)})
}

// okListBounded is okList with okResultBounded's byte ceiling on the payload.
func okListBounded[T any](key string, items []T, maxBytes int) *mcpgo.CallToolResult {
	return okResultBounded(map[string]any{key: listOrEmpty(items)}, maxBytes)
}

// listOrEmpty returns items, substituting an empty slice for nil so it
// marshals as [] rather than null.
func listOrEmpty[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// okText returns a plain-text success result (for simple confirmations).
func okText(msg string) *mcpgo.CallToolResult {
	return mcpgo.NewToolResultText(msg)
}

// okResultBounded is okResult with a byte ceiling on the JSON text fallback.
// gRPC streams are not resumable, so when the payload exceeds maxBytes we do
// not paginate — we return a truncation envelope telling the agent to narrow
// the query. maxBytes <= 0 disables the cap (behaves as okResult).
func okResultBounded(v any, maxBytes int) *mcpgo.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResultf(errCodeInternal, "marshaling result: %s", err.Error())
	}
	if maxBytes > 0 && len(b) > maxBytes {
		env := map[string]any{
			"truncated": true,
			"max_bytes": maxBytes,
			"bytes":     len(b),
			"note":      "output exceeded max_bytes; narrow the query (reduce max_batches / max_chunks, add filters, or raise max_bytes)",
		}
		eb, _ := json.MarshalIndent(env, "", "  ")
		return mcpgo.NewToolResultStructured(env, string(eb))
	}
	// Delegate so the under-budget path gets okResult's object guard too.
	return okResult(v)
}

// okTextBounded returns a plain-text success result like okText, but clamps s
// to maxBytes bytes and appends a truncation note when it exceeds the cap. The
// cut is backed off to the nearest UTF-8 rune boundary so the result is never
// invalid UTF-8 (which some hosts reject when serializing TextContent). hint is
// a tool-specific suggestion for narrowing output; a generic one is used when
// empty. Kept separate from okResultBounded because JSON-marshaling a plain
// string (as okResultBounded would) quotes and escapes it, changing the text
// output format for tools whose callers rely on the raw, unquoted text.
// maxBytes <= 0 disables the cap (behaves as okText).
func okTextBounded(s, hint string, maxBytes int) *mcpgo.CallToolResult {
	if maxBytes > 0 && len(s) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if hint == "" {
			hint = "narrow the query (reduce max_chunks, add filters, or raise max_bytes)"
		}
		s = s[:cut] + fmt.Sprintf("\n[truncated: output exceeded max_bytes=%d; %s]", maxBytes, hint)
	}
	return okText(s)
}
