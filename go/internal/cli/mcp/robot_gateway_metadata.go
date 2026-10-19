package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// mcp-go retains extension fields in _meta but cannot represent the canonical
// top-level securitySchemes field. Mirror the per-tool policy at the HTTP
// boundary, preserving the SDK's transport validation and authorization.
type gatewayToolListWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *gatewayToolListWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *gatewayToolListWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func (w *gatewayToolListWriter) finish() {
	body := w.body.Bytes()
	if w.status == http.StatusOK {
		var envelope map[string]any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if decoder.Decode(&envelope) == nil {
			if result, ok := envelope["result"].(map[string]any); ok {
				if tools, ok := result["tools"].([]any); ok {
					for _, item := range tools {
						if tool, ok := item.(map[string]any); ok {
							gatewayMirrorSecuritySchemes(tool)
						}
					}
					if encoded, err := json.Marshal(envelope); err == nil {
						body = encoded
					}
				}
			}
		}
	}
	w.Header().Del("Content-Length")
	if w.status != 0 {
		w.ResponseWriter.WriteHeader(w.status)
	}
	_, _ = w.ResponseWriter.Write(body)
}

func gatewayMirrorSecuritySchemes(tool map[string]any) {
	if meta, ok := tool["_meta"].(map[string]any); ok {
		if schemes, ok := meta["securitySchemes"]; ok {
			tool["securitySchemes"] = schemes
		}
	}
}
