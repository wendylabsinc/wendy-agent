# MCPExample

A minimal reference implementation of a WendyOS app that exposes device tools over the [Model Context Protocol (MCP)](https://modelcontextprotocol.io). Deploy it with `wendy run` and its tools become instantly available inside `wendy mcp serve`.

## What it does

The example runs a Python MCP server built with [FastMCP](https://github.com/jlowin/fastmcp) and served over HTTP by `uvicorn`. It exposes three tools:

| Tool | Description |
|------|-------------|
| `ping` | Returns `"pong"` — useful for checking reachability |
| `device_info` | Returns hostname, CPU architecture, OS, and Python version |
| `run_command` | Runs an arbitrary shell command and returns `exit_code`, `stdout`, and `stderr` |

## File layout

```
MCPExample/
  Dockerfile          # Python 3.11-slim image
  main.py             # FastMCP server definition
  requirements.txt    # mcp[cli] + uvicorn[standard]
  wendy.json          # App config with mcp entitlement
```

## wendy.json

```json
{
  "appId": "com.wendylabs.examples.mcp-example",
  "version": "1.0.0",
  "language": "python",
  "python": {
    "container": {
      "sourceRoot": "/app"
    }
  },
  "entitlements": [
    { "type": "network", "mode": "host" },
    { "type": "mcp", "port": 3000 }
  ]
}
```

Two entitlements are required:

- **`network` (host mode)** — lets the agent reach the container's MCP port over loopback (`127.0.0.1:3000`).
- **`mcp`** — tells the agent that this container runs an MCP server on port `3000`. The agent stores the port in the `sh.wendy/mcp.port` container label and exposes the container's tools through `wendy mcp serve`.

## Running the example

```sh
cd Examples/MCPExample
wendy run
```

`wendy run`:
1. Builds the Docker image for the target device architecture.
2. Pushes and starts the container on the device.
3. Because of the `mcp` entitlement, the container's port (`3000`) is registered with the wendy agent.

Once the container is running, start the MCP server:

```sh
wendy mcp serve
```

The `ping`, `device_info`, and `run_command` tools appear prefixed with the app name (e.g. `com.wendylabs.examples.mcp-example/ping`) in any connected AI assistant.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_PORT` | `3000` | TCP port the MCP server listens on. Must match `wendy.json`. |

## Dependencies

```
mcp[cli]>=1.9.4
uvicorn[standard]>=0.27.0
```

## How the proxy works

When an AI assistant calls a tool on this container, the request flows through:

```
AI Assistant
    │  MCP (stdio)
    ▼
wendy mcp serve
    │  StreamMCP gRPC (bidirectional streaming)
    ▼
wendy-agent (on device)
    │  TCP
    ▼
container MCP server (127.0.0.1:3000)
```

The wendy agent's `StreamMCP` RPC proxies raw bytes between the gRPC stream and the container's TCP port. The agent verifies that:

- The container has an `mcp` entitlement (non-zero `sh.wendy/mcp.port` label).
- The container is in the `RUNNING` state before attempting the TCP connection.

See [wendy-agent/mcp.md](../../wendy-agent/mcp.md) for full API documentation.
