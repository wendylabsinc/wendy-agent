# `wendy cloud mcp`

Lets a web chat (ChatGPT, Claude) use Wendy through your own CLI session.

## Usage

```sh
wendy cloud mcp [flags]
```

## Description

`wendy cloud mcp` opens a session with the hosted Wendy MCP relay and prints a pairing code. Add the Wendy connector in your web chat, sign in, and give it the code. The CLI then shows the client's name, client ID and code and asks you to allow it.

Once allowed, the chat gets the same tools as [`wendy mcp serve`](/docs/integrations/mcp). Every call runs on your machine and is signed with your operator key; the relay holds no Wendy credential. Each call is shown as one log line with the client name, tool and outcome.

- **Read-only tools** run without asking.
- **Other tools** are refused unless you allow them with `--allow-tool` or `--allow-all`, and even then each call waits for you to confirm it in the terminal, showing the tool and its arguments.
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
