package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

// ToolManifest describes the tool set `wendy cloud mcp` serves to a hosted
// chat through the relay; `wendy mcp serve` keeps every tool. It is published as service-protos docs/relay-mcp-tools.json, from
// which the hosted relay answers tools/list before a chat is paired.
type ToolManifest struct {
	// Version is "<CLI version>+<tool set hash>". Only the hash identifies
	// the tool set; CLI releases with the same tools share it.
	Version string       `json:"version"`
	Tools   []mcpgo.Tool `json:"tools"`
}

// relayExcluded are built-in tools a hosted chat never gets: those that take
// a credential from the chat (both stores ban collecting one), the hidden
// container_attach alias, and wendy_tools (the relay serves one fixed set).
var relayExcluded = []string{"wifi_connect", "provisioning_start", "container_attach", "wendy_tools"}

// inManifest reports whether the relay channel serves name: built-in tools
// minus relayExcluded. Tools proxied from device apps are never served.
func inManifest(name string) bool {
	if slices.Contains(relayExcluded, name) {
		return false
	}
	for _, names := range toolGroups {
		if slices.Contains(names, name) {
			return true
		}
	}
	return false
}

// relayManifestMiddleware refuses calls to tools outside the manifest, so a
// hosted chat can call exactly what the stores reviewed.
func (s *mcpServer) relayManifestMiddleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		if s.relay && !inManifest(req.Params.Name) {
			return errResultf(errCodeNotFound, "tool %s is not available in hosted chats; use a local MCP client (wendy mcp serve)", req.Params.Name), nil
		}
		return next(ctx, req)
	}
}

// Manifest returns the relay channel's tool list, sorted by name: what
// tools/list answers when `wendy cloud mcp` serves a hosted chat.
func Manifest() (ToolManifest, error) {
	s := New(&config.Config{}, nil)
	s.relay = true
	srv, err := s.newProtocolServer()
	if err != nil {
		return ToolManifest{}, err
	}
	var tools []mcpgo.Tool
	for _, t := range srv.ListTools() {
		tools = append(tools, t.Tool)
	}
	tools = s.filterTools(context.Background(), tools)
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
