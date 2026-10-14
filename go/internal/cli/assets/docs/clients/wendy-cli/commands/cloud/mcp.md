# `wendy cloud mcp`

Lets a web chat (ChatGPT, Claude) use Wendy through your own CLI session.

## Usage

```sh
wendy cloud mcp [flags]
```

## Description

`wendy cloud mcp` opens a session with the hosted Wendy MCP relay. Add the Wendy connector in your web chat and sign in. When the chat first calls a tool, the relay shows a pairing code in the chat and the CLI asks you, with the client's name, client ID and the same code, whether to accept it. Accept only if the codes match. If the relay's published tool set differs from this CLI's, the CLI warns and refuses every pairing.

Once allowed, the chat gets the same tools as [`wendy mcp serve`](/docs/integrations/mcp). Every call runs on your machine and is signed with your operator key; the relay holds no Wendy credential. Each call is shown as one log line with the client name, tool and outcome.

- **Read-only tools** run without asking, except those that reach your machine or local network (local paths and drives, simulators, LAN scans, explicit addresses such as `os_install_verify`, `project_validate`, `device_list`).
- **Other tools** are refused unless you allow them with `--allow-tool` or `--allow-all`, and even then each call waits for you to confirm it in the terminal, showing the tool and its arguments. An unknown name in `--allow-tool` stops the command and lists the valid ones.
- The chat uses only the session the command started with; a call that names another Cloud endpoint or organization is refused.
- One web chat per session; later pairing requests are denied.

The chat can do nothing once the command exits. Press Ctrl+C to end the session.

Your organization must allow MCP and Hosted MCP. The CLI refuses to start when your session says MCP is not allowed.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud-grpc` | `""` | Cloud gRPC endpoint of the session to use. |
| `--allow-tool` | none | Also allow these tools by name (comma-separated). Calls that change state still need your confirmation. |
| `--allow-all` | `false` | Allow every tool. Calls that change state still need your confirmation. |

## Examples

Pair a web chat with read-only access:

```sh
wendy cloud mcp
```

Also let it deploy, confirming each deploy:

```sh
wendy cloud mcp --allow-tool run
```
