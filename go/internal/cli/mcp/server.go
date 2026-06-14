package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/mcp/appsui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/status"
)

// WendyAppURI is the ui:// resource for Wendy's adaptive in-chat app.
const WendyAppURI = "ui://wendy/app"

// ConnectFunc connects to a wendy agent at the given address (host:port).
type ConnectFunc func(ctx context.Context, address string) (*grpcclient.AgentConnection, error)

type mcpServer struct {
	cfg           *config.Config
	connectFn     ConnectFunc
	conn          *grpcclient.AgentConnection
	connType      string
	connCache     map[string]*cachedConn
	cloudTunnels  map[string]*mcpCloudTunnel
	discoverLANFn func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error)
	appHasUI      map[string]bool
	appUIURI      map[string]string
	mu            sync.RWMutex

	srv           *server.MCPServer
	connectedApps map[string]func() // appName -> proxy cleanup; guards against double-connect
	connectMu     sync.Mutex        // serializes container connect setup
}

// setAppUI marks an app as UI-capable and records the host-visible (namespaced)
// URI of its primary ui:// resource. The first ui:// resource discovered wins,
// so the recorded URI is stable across re-scans. Safe for concurrent use.
func (s *mcpServer) setAppUI(app, nsURI string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appHasUI == nil {
		s.appHasUI = map[string]bool{}
	}
	if s.appUIURI == nil {
		s.appUIURI = map[string]string{}
	}
	s.appHasUI[app] = true
	if s.appUIURI[app] == "" {
		s.appUIURI[app] = nsURI
	}
}

// getAppUIURI returns the recorded namespaced ui:// URI for an app, or "" if the
// app exposed no ui:// resource.
func (s *mcpServer) getAppUIURI(app string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appUIURI[app]
}

// getAppHasUI reports the cached UI capability of an app.
func (s *mcpServer) getAppHasUI(app string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appHasUI[app]
}

func New(cfg *config.Config, connectFn ConnectFunc) *mcpServer {
	return &mcpServer{
		cfg:           cfg,
		connectFn:     connectFn,
		cloudTunnels:  make(map[string]*mcpCloudTunnel),
		discoverLANFn: discovery.DiscoverLAN,
	}
}

func (s *mcpServer) GetConn() *grpcclient.AgentConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn
}

// SetConn replaces the active connection, closing the previous one.
func (s *mcpServer) SetConn(conn *grpcclient.AgentConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.conn = conn
	if conn == nil {
		s.connType = ""
	}
}

// SetConnType records the transport type of the active connection ("direct" or "cloud").
func (s *mcpServer) SetConnType(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connType = t
}

func (s *mcpServer) GetConnType() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connType
}

// ConnectTo connects to address and stores the result as the active connection.
func (s *mcpServer) ConnectTo(ctx context.Context, address string) error {
	if s.connectFn == nil {
		return fmt.Errorf("no connect function configured")
	}
	conn, err := s.connectFn(ctx, address)
	if err != nil {
		return err
	}
	s.SetConn(conn)
	return nil
}

// buildServer constructs and fully registers the MCP server (transport-agnostic).
func (s *mcpServer) buildServer() *server.MCPServer {
	srv := server.NewMCPServer("wendy", version.Version,
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, false),
	)
	s.registerStatusTools(srv)
	s.registerGuideResource(srv)
	s.registerWendyAppUI(srv)
	s.registerDeviceTools(srv)
	s.registerContainerTools(srv)
	s.registerTelemetryTools(srv)
	s.registerWiFiTools(srv)
	s.registerBluetoothTools(srv)
	s.registerHardwareTools(srv)
	s.registerFileSyncTools(srv)
	s.registerProvisioningTools(srv)
	s.registerOSTools(srv)
	s.registerCloudTools(srv)
	s.registerAppsTools(srv)
	s.srv = srv
	return srv
}

// Start registers all tools and serves MCP over stdio (default transport).
func (s *mcpServer) Start(ctx context.Context) error {
	srv := s.buildServer()
	s.registerContainerMCPTools(ctx)
	defer s.cleanupConnectedApps()
	return server.ServeStdio(srv)
}

// StartHTTP serves MCP over streamable HTTP at addr (e.g. "127.0.0.1:7777").
// token, if non-empty, is required as a Bearer token on the MCP endpoint.
func (s *mcpServer) StartHTTP(ctx context.Context, addr, token string) error {
	srv := s.buildServer()
	s.registerContainerMCPTools(ctx)
	defer s.cleanupConnectedApps()
	streamSrv := server.NewStreamableHTTPServer(srv)
	if token == "" {
		return streamSrv.Start(addr)
	}
	// Token required. WithHTTPContextFunc can't reject requests, so wrap the
	// handler. The streamable server's default endpoint path is /mcp.
	mux := http.NewServeMux()
	mux.Handle("/mcp", bearerAuth(token, streamSrv))
	return http.ListenAndServe(addr, mux)
}

// ensureContainerConnected proxies appName's MCP server into s.srv if not
// already connected (tools + ui:// resources registered, appHasUI cached).
// Idempotent and safe for concurrent use. maxAttempts bounds the Initialize
// retries — pass a small value on demand so a slow/unhealthy app doesn't stall
// the request; failures aren't cached, so a later call re-probes.
func (s *mcpServer) ensureContainerConnected(ctx context.Context, conn *grpcclient.AgentConnection, appName string, maxAttempts int) {
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	if s.connectedApps == nil {
		s.connectedApps = map[string]func(){}
	}
	if _, ok := s.connectedApps[appName]; ok {
		return
	}
	if s.srv == nil {
		return
	}
	if cleanup := s.connectContainerMCPTools(ctx, s.srv, conn, appName, maxAttempts); cleanup != nil {
		s.connectedApps[appName] = cleanup
	}
}

// cleanupConnectedApps closes every container proxy opened during the session.
func (s *mcpServer) cleanupConnectedApps() {
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	for _, c := range s.connectedApps {
		c()
	}
	s.connectedApps = nil
}

// bearerAuth wraps next, requiring "Authorization: Bearer <token>".
func bearerAuth(token string, next http.Handler) http.Handler {
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func errNotConnected() *mcpgo.CallToolResult {
	return mcpgo.NewToolResultError("no device connected — use device_connect first")
}

// grpcErrString unwraps a gRPC status error into a human-readable string.
func grpcErrString(err error) string {
	if s, ok := status.FromError(err); ok {
		return s.Message()
	}
	return err.Error()
}

// stringParam extracts a string argument from an MCP tool request.
func stringParam(req mcpgo.CallToolRequest, name string) string {
	return req.GetString(name, "")
}

// intParam extracts an integer argument, falling back to defaultVal.
func intParam(req mcpgo.CallToolRequest, name string, defaultVal int) int {
	return req.GetInt(name, defaultVal)
}

// registerContainerMCPTools scans running containers for mcp_port > 0 and
// eagerly connects each into the session (tools + ui:// resources). Newly
// deployed containers are picked up lazily later via ensureContainerConnected.
// Errors per-container are warnings; they do not prevent the session from starting.
func (s *mcpServer) registerContainerMCPTools(ctx context.Context) {
	conn := s.GetConn()
	if conn == nil {
		return
	}

	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: listing containers for MCP tools: %v\n", err)
		return
	}

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: reading container list: %v\n", err)
			return
		}
		c := resp.GetContainer()
		if c == nil || c.GetMcpPort() == 0 || c.GetRunningState() != agentpb.AppRunningState_RUNNING {
			continue
		}
		s.ensureContainerConnected(ctx, conn, c.GetAppName(), 4)
	}
}

// connectContainerMCPTools proxies a single container's MCP server into srv.
// It retries Initialize up to maxAttempts times with exponential backoff
// (2s, 4s, 8s). On success it returns a cleanup function that closes the proxy;
// on failure it returns nil (after cleaning up internally).
func (s *mcpServer) connectContainerMCPTools(ctx context.Context, srv *server.MCPServer, conn *grpcclient.AgentConnection, appName string, maxAttempts int) func() {
	addr, closeProxy, err := startMCPProxy(ctx, conn, appName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: MCP proxy for %s: %v\n", appName, err)
		return nil
	}

	// Container MCP servers serve the streamable-HTTP endpoint at the standard
	// /mcp path (FastMCP default, per the MCP spec); the proxy listener forwards
	// raw bytes, so the path lives in this client URL.
	mcpCli, err := mcpclient.NewStreamableHttpClient("http://" + addr + "/mcp")
	if err != nil {
		closeProxy()
		fmt.Fprintf(os.Stderr, "Warning: MCP client for %s: %v\n", appName, err)
		return nil
	}

	var initErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				closeProxy()
				return nil
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		_, initErr = mcpCli.Initialize(ctx, mcpgo.InitializeRequest{})
		if initErr == nil {
			break
		}
	}
	if initErr != nil {
		closeProxy()
		fmt.Fprintf(os.Stderr, "Warning: MCP init for %s: %v\n", appName, initErr)
		return nil
	}

	result, err := mcpCli.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		closeProxy()
		fmt.Fprintf(os.Stderr, "Warning: listing MCP tools for %s: %v\n", appName, err)
		return nil
	}

	prefix := sanitizeMCPPrefix(appName)
	for _, tool := range result.Tools {
		proxied := tool
		proxied.Name = prefix + "__" + tool.Name
		originalName := tool.Name
		rewriteToolUIMeta(&proxied, appName)
		srv.AddTool(proxied, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			inner := mcpgo.CallToolRequest{}
			inner.Params.Name = originalName
			inner.Params.Arguments = req.Params.Arguments
			return mcpCli.CallTool(ctx, inner)
		})
	}

	// Forward the container's resources (including ui:// app resources) under
	// the app namespace so the host can fetch them through Wendy.
	if resList, lerr := mcpCli.ListResources(ctx, mcpgo.ListResourcesRequest{}); lerr == nil {
		for _, r := range resList.Resources {
			origURI := r.URI
			nsRes := r
			nsRes.URI = namespacedUIURI2(appName, origURI)
			if strings.HasPrefix(origURI, "ui://") {
				s.setAppUI(appName, nsRes.URI)
			}
			toolPrefix := prefix + "__"
			srv.AddResource(nsRes, func(ctx context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
				out, rerr := mcpCli.ReadResource(ctx, mcpgo.ReadResourceRequest{Params: mcpgo.ReadResourceParams{URI: origURI}})
				if rerr != nil {
					return nil, rerr
				}
				for i := range out.Contents {
					switch c := out.Contents[i].(type) {
					case mcpgo.TextResourceContents:
						c.URI = req.Params.URI
						// The container's tools are aggregated into this server under
						// prefixed names; tell its UI the prefix so its own tools/call
						// resolve. (No-op for non-HTML resources.)
						c.Text = injectToolPrefix(c.Text, c.MIMEType, toolPrefix)
						out.Contents[i] = c
					case mcpgo.BlobResourceContents:
						c.URI = req.Params.URI
						out.Contents[i] = c
					}
				}
				return out.Contents, nil
			})
		}
	}
	return closeProxy
}

// registerWendyAppUI registers the adaptive WendyOS in-chat app as a ui:// resource.
func (s *mcpServer) registerWendyAppUI(srv *server.MCPServer) {
	appsui.RegisterUIResource(srv, WendyAppURI, "WendyOS App", appsui.WendyAppHTML, &appsui.UIResourceOptions{
		Description: "Adaptive WendyOS app: device dashboard, controls, and app launcher.",
	})
}

// sanitizeMCPPrefix converts an app name to a valid MCP tool name prefix
// by replacing non-alphanumeric characters with underscores.
func sanitizeMCPPrefix(appName string) string {
	b := make([]byte, len(appName))
	for i := range appName {
		c := appName[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			b[i] = c
		} else {
			b[i] = '_'
		}
	}
	return string(b)
}
