package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

// ToolManifest describes the tool set `wendy mcp serve` and `wendy cloud mcp`
// expose. It is published as service-protos docs/relay-mcp-tools.json, from
// which the hosted relay answers tools/list before a chat is paired.
type ToolManifest struct {
	// Version is "<CLI version>+<tool set hash>". Only the hash identifies
	// the tool set; CLI releases with the same tools share it.
	Version string       `json:"version"`
	Tools   []mcpgo.Tool `json:"tools"`
}

// Manifest returns the built-in tool set, sorted by name. Tools proxied from
// apps running on a device are per-device and not part of it.
func Manifest() (ToolManifest, error) {
	srv, err := New(&config.Config{}, nil).newProtocolServer()
	if err != nil {
		return ToolManifest{}, err
	}
	var tools []mcpgo.Tool
	for _, t := range srv.ListTools() {
		tools = append(tools, t.Tool)
	}
	slices.SortFunc(tools, func(a, b mcpgo.Tool) int { return strings.Compare(a.Name, b.Name) })
	b, err := json.Marshal(tools)
	if err != nil {
		return ToolManifest{}, err
	}
	sum := sha256.Sum256(b)
	return ToolManifest{Version: version.Version + "+" + hex.EncodeToString(sum[:8]), Tools: tools}, nil
}

// CheckRelayManifest refuses a relay whose reported manifest version
// describes a different tool set than this CLI's: the chat would see tools
// before pairing that the CLI does not serve after it.
func CheckRelayManifest(relayVersion string) error {
	m, err := Manifest()
	if err != nil {
		return err
	}
	if manifestHash(relayVersion) != manifestHash(m.Version) {
		return fmt.Errorf("the relay publishes MCP tool manifest %q but this CLI serves %q; pairing refused until both run the same tool set", relayVersion, m.Version)
	}
	return nil
}

func manifestHash(v string) string {
	i := strings.LastIndex(v, "+")
	if i < 0 {
		return ""
	}
	return v[i+1:]
}
