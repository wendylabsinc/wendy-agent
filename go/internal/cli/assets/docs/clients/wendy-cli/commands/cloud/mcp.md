# `wendy cloud mcp`

Lets a web chat (ChatGPT, Claude) use Wendy through your own CLI session.

## Usage

```sh
wendy cloud mcp [flags]
```

## Description

`wendy cloud mcp` opens a session with the hosted Wendy MCP relay. Add the Wendy connector in your web chat and sign in. When the chat first calls a tool, the relay shows a pairing code in the chat and the CLI asks you, with the client's name, client ID and the same code, whether to accept it. Accept only if the codes match. Approval covers that signed-in client connection across chats until the CLI session ends. Calls that need confirmation still ask in the terminal.

The CLI supports its current tool catalog and bundled, compatible older relay catalogs. Adding tools or optional arguments to the CLI does not require upgrading a relay whose published catalog it still supports. The chat sees that relay's tools and argument schemas throughout the session; newer tools and arguments become available when the relay advertises them. Unknown or incompatible catalogs produce a warning and pairing is refused.

Once allowed, the chat uses the supported hosted tools from [`wendy mcp serve`](/docs/integrations/mcp). Every call runs on your machine and is signed with your operator key; the relay holds no Wendy credential. Each call is shown as one log line with the client name, tool and outcome.

- Built-in read-only device and Cloud tools that do not access the host are available by default. Every permitted call waits for confirmation in the terminal, showing the tool and its arguments.
- `--allow-read` skips confirmation for those built-in read-only tools only. Host access, local discovery, and proxied tools do not qualify, even if their annotations say they are read-only.
- Other tools are refused unless you allow them with `--allow-tool` or `--allow-all`. With `--allow-tool`, each call still needs confirmation unless it already qualifies for `--allow-read`. An unknown tool name stops the command and lists the valid ones.
- `--allow-all` allows every supported relay tool without per-call confirmation, including state changes and host access. To allow specific tools while keeping their confirmations, use `--allow-tool` instead.
- `--auto-accept` automatically pairs ChatGPT's known OAuth client. It does not change tool permissions or confirmations. Other clients still require the matching-code prompt.
- The chat uses only the session the command started with; a call that names another Cloud endpoint or organization is refused.
- One signed-in client connection per CLI session. The relay can reuse its pairing when that connection opens another MCP session. Different connections require their own CLI sessions.

The chat can do nothing once the command exits. Press Ctrl+C to end the session.

Your organization must allow MCP and Hosted MCP. The CLI refuses to start when your session says MCP is not allowed.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud-grpc` | `""` | Cloud gRPC endpoint of the session to use. |
| `--allow-tool` | none | Also allow these tools by name, comma-separated. Each call requires confirmation unless it qualifies for `--allow-read`. |
| `--allow-read` | `false` | Skip confirmation for built-in read-only device and Cloud tools that do not access the host. |
| `--allow-all` | `false` | Allow every supported relay tool without per-call confirmation. |
| `--auto-accept` | `false` | Automatically accept pairing from ChatGPT's known OAuth client. Tool permissions and confirmations are separate. |

## Examples

Pair a web chat with read-only access, confirming each call:

```sh
wendy cloud mcp
```

Allow built-in device and Cloud reads without confirmation:

```sh
wendy cloud mcp --allow-read
```

Also let it deploy, confirming each deploy:

```sh
wendy cloud mcp --allow-read --allow-tool run
```

Allow every supported tool without further tool confirmations after pairing:

```sh
wendy cloud mcp --allow-all
```

Automatically pair ChatGPT and allow built-in device and Cloud reads without confirmation:

```sh
wendy cloud mcp --auto-accept --allow-read
```
