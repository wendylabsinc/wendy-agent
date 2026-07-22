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
	return mcpgo.NewToolResultStructured(v, string(b))
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
	return mcpgo.NewToolResultStructured(v, string(b))
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
