package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// BindingPolicy is what one web client bound through the hosted MCP relay may
// do (Relay MCP spec, WDY-3660). The relay can forge any call, so the CLI is
// where the limits hold: a tool outside the allow-list is refused, and every
// call that is not read-only waits for the user's confirmation. The local
// stdio server has no policy.
type BindingPolicy struct {
	AllowAll bool     // every tool; otherwise the read-only built-ins plus Tools
	Tools    []string // further tools allowed by name
	// Confirm asks the user about one call that is not read-only. nil refuses
	// every such call.
	Confirm func(ctx context.Context, tool string, args map[string]any) bool
	// Log receives one line per call: the tool and what happened to it.
	Log func(tool, outcome string)
}

func (s *mcpServer) setBindingPolicy(p *BindingPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binding = p
}

func (s *mcpServer) bindingPolicy() *BindingPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding
}

func (p *BindingPolicy) allows(tool mcpgo.Tool) bool {
	return p.AllowAll || slices.Contains(p.Tools, tool.Name) || readOnlyTool(tool)
}

func (p *BindingPolicy) log(tool, outcome string) {
	if p.Log != nil {
		p.Log(tool, outcome)
	}
}

// readOnlyTool reports whether tool is a built-in annotated read-only. Tools
// proxied from app containers describe themselves, so they never count.
func readOnlyTool(tool mcpgo.Tool) bool {
	builtin := false
	for _, names := range toolGroups {
		builtin = builtin || slices.Contains(names, tool.Name)
	}
	hint := tool.Annotations.ReadOnlyHint
	return builtin && hint != nil && *hint
}

// bindingMiddleware enforces the binding policy on every tool call.
func (s *mcpServer) bindingMiddleware(srv *server.MCPServer) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			p := s.bindingPolicy()
			if p == nil {
				return next(ctx, req)
			}
			name := req.Params.Name
			registered := srv.GetTool(name)
			if registered == nil || !p.allows(registered.Tool) {
				p.log(name, "refused: not allowed for this client")
				return errResultf(errCodeDenied, "%s is not allowed for this client; the user can allow it with 'wendy cloud mcp --allow-tool %s'", name, name), nil
			}
			if !readOnlyTool(registered.Tool) {
				if p.Confirm == nil || !p.Confirm(ctx, name, req.GetArguments()) {
					p.log(name, "refused by the user")
					return errResultf(errCodeDenied, "the user did not confirm %s", name), nil
				}
			}
			result, err := next(ctx, req)
			switch {
			case err != nil:
				p.log(name, "failed: "+err.Error())
			case result != nil && result.IsError:
				p.log(name, "returned an error")
			default:
				p.log(name, "ok")
			}
			return result, err
		}
	}
}

// allowMCPClaim is the wendy-auth claim carrying the realm's "Allow MCP"
// setting. ponytail: name from the Relay MCP contract draft (cloud-worker
// t-08); confirm when service-protos docs/relay-mcp.md lands.
const allowMCPClaim = "allow_mcp"

// RealmAllowsMCP refuses MCP for a session whose realm turned "Allow MCP" off.
// Only an explicit false refuses: the realm default is on, and older tokens
// and personal access tokens do not carry the claim. The token is decoded,
// not verified; Cloud enforces the setting on the channel it is sent.
func RealmAllowsMCP(auth *config.AuthConfig) error {
	if auth == nil {
		return nil
	}
	parts := strings.Split(auth.APIKey, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	if allowed, ok := claims[allowMCPClaim].(bool); ok && !allowed {
		return fmt.Errorf("your organization does not allow MCP clients (session %q); ask an organization admin to turn on Allow MCP", auth.Name)
	}
	return nil
}
