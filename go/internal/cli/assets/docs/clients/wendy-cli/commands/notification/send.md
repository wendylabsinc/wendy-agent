# `wendy notification send`

Sends an operator-facing Wendy Notification through Wendy Cloud. The alias
`wendy notification create` runs the same command.

## Usage

```sh
wendy notification send [flags]
```

Sign in first with `wendy cloud login`. The command uses the selected Cloud
session and its organization unless `--cloud-grpc` or `--organization` selects
another one.

Exactly one audience flag is required:

| Flag | Description |
|---|---|
| `--user <id>` | Send to one user. |
| `--team <id>` | Send to an organization team by numeric ID. |
| `--role <role>` | Send to `owner`, `admin`, `billing_manager`, `member`, or `viewer`. |

## Content flags

| Flag | Required | Description |
|---|---|---|
| `--title <text>` | Yes | Notification title. |
| `--body <text>` | Yes | Notification body. |
| `--deep-link <uri>` | Yes | Absolute `wendy://` destination. |
| `--severity <level>` | No | `info` (default), `warning`, `error`, or `critical`. |
| `--source-id <id>` | No | Idempotency key; a random key is generated when omitted. |
| `--metadata <json>` | No | JSON object containing structured metadata. |
| `--organization, -o <id>` | No | Organization ID; defaults to the selected session. |
| `--cloud-grpc <address>` | No | Cloud gRPC endpoint; defaults to the selected session. |

The Cloud call has a 15-second deadline. Reusing a `source-id` in the same
authenticated source namespace returns a duplicate response without delivering
the Notification again. Use the global `--json` flag for the complete response.

## Examples

```sh
# Notify one user
wendy notification send \
  --user alice \
  --title "Deploy complete" \
  --body "v1.2 is live" \
  --deep-link "wendy://devices/current"

# Notify all organization admins with structured metadata
wendy notification send \
  --role admin \
  --title "Disk alert" \
  --body "Disk usage reached 90%" \
  --severity warning \
  --deep-link "wendy://devices/current/storage" \
  --source-id disk-alert-90 \
  --metadata '{"usagePercent":90}'
```
