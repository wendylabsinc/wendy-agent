# Notifications API

The `NotificationService` gRPC service creates, lists, and manages
operator-facing Wendy Notifications in a Wendy Cloud organization.

## Proto package

`wendycloud.v1` — defined in `Proto/cloud/notifications.proto`.

## App-facing API (`wendy.system.v1`)

Apps with the `notifications` entitlement call
`wendy.system.v1.NotificationService` over the Unix socket at
`$WENDY_SYSTEM_SOCKET` (`/run/wendy/system/system.sock`). WendyKit exposes this
as `WendyNotification.send(_:)`, so apps normally do not call gRPC directly.

The private socket binds every call to trusted app identity. The request cannot
supply an app ID, device ID, or organization ID; the agent adds app identity and
Cloud derives device and organization identity from device mTLS.

### `Send`

```
Send(SendRequest) → SendResponse
```

The agent forwards the request to Cloud with a 15-second deadline.

#### `SendRequest`

| Field | Type | Description |
|---|---|---|
| `audience` | `NotificationAudience` | Union of the user, organization team, and organization role selectors below. |
| `title` | `string` | Notification title. |
| `body` | `string` | Notification body. |
| `severity` | `NotificationSeverity` | `INFO`, `WARNING`, `ERROR`, or `CRITICAL`. |
| `deep_link` | `string` | Absolute `wendy://` URI. |
| `source_id` | `string` | App-generated idempotency key scoped to this app and device. |
| `metadata` | `optional Struct` | Structured JSON-compatible metadata. |

#### `NotificationAudience`

The app-facing and Cloud messages use the same plural selector shape. All three
fields have union semantics. Selectors are normalized and deduplicated before
delivery, with at most 100 unique selectors total. Cloud is authoritative and
resolves at most 10,000 recipients.

| Field | Type | Description |
|---|---|---|
| `user_ids` | `repeated string` | User IDs to include. |
| `team_ids` | `repeated int32` | Organization team IDs to include. |
| `roles` | `repeated OrganizationRole` | Organization roles to include. |

At least one selector is required. A user selected through more than one field
receives one Notification.

#### `SendResponse`

| Field | Type | Description |
|---|---|---|
| `duplicate` | `bool` | Whether the `source_id` was already accepted without another delivery. |
| `recipient_count` | `int32` | Number of resolved recipients. |

## Cloud API methods

### `CreateNotification` *(legacy)*

```
CreateNotification(CreateNotificationRequest) → Notification
```

Creates a Notification for one user. This method remains available for existing
Dashboard and MCP clients.

---

### `CreateNotificationV2`

```
CreateNotificationV2(CreateNotificationV2Request) → CreateNotificationV2Response
```

Creates one canonical Notification per resolved recipient. User-authenticated
callers provide `organization_id`. Provisioned-device callers omit it because
Cloud derives the organization and device from their certificate; the Wendy
agent supplies `source_app_id` from trusted container state.

#### `CreateNotificationV2Request`

| Field | Type | Description |
|---|---|---|
| `organization_id` | `optional int32` | Required for user-authenticated callers; omitted by provisioned devices. |
| `audience` | `NotificationAudience` | Union of repeated `user_ids`, `team_ids`, and `roles`; see above. |
| `title` | `string` | Notification title. |
| `body` | `string` | Notification body. |
| `severity` | `NotificationSeverity` | `INFO`, `WARNING`, `ERROR`, or `CRITICAL`. |
| `deep_link` | `string` | Absolute `wendy://` URI. |
| `source_id` | `string` | Caller-generated idempotency key within the authenticated source namespace. |
| `metadata` | `optional Struct` | Structured JSON-compatible metadata. |
| `source_app_id` | `optional string` | Required for provisioned-device calls and stamped by the Wendy agent. |

#### `CreateNotificationV2Response`

| Field | Type | Description |
|---|---|---|
| `notifications` | `repeated Notification` | One canonical Notification per resolved recipient. |
| `duplicate` | `bool` | Whether the `source_id` was already accepted without another delivery. |
| `recipient_count` | `int32` | Number of resolved recipients, capped by Cloud at 10,000. |

---

### `ListNotifications` *(server-streaming)*

```
ListNotifications(ListNotificationsRequest) → stream ListNotificationsResponse
```

Returns one Notification per streamed message.

#### `ListNotificationsRequest`

| Field | Type | Description |
|---|---|---|
| `organization_id` | `int32` | Organization to query. |
| `user_id` | `string` | User whose Notifications are requested. |
| `offset` | `optional int32` | Number of matching Notifications to skip. |
| `limit` | `optional int32` | Maximum number of Notifications to return. |
| `severity_filter` | `optional NotificationSeverity` | Restrict results by severity. |
| `include_deleted` | `bool` | Include soft-deleted Notifications. |

#### `ListNotificationsResponse`

| Field | Type | Description |
|---|---|---|
| `notification` | `Notification` | Notification carried by this stream message. |
| `total` | `int32` | Total number of matching Notifications. |

> **Migration note:** `ListNotifications` previously returned one unary response
> containing `repeated notifications`, `next_page_token`, and `total_count`.
> Clients must now receive the server stream until it closes and use
> `offset`/`limit` instead of `page_size`/`page_token`.

---

### `GetNotification`

```
GetNotification(GetNotificationRequest) → Notification
```

Returns one Notification by ID.

### `DeleteNotification`

```
DeleteNotification(DeleteNotificationRequest) → DeleteNotificationResponse
```

Deletes one Notification by ID.

### `MarkAsRead`

```
MarkAsRead(MarkAsReadRequest) → MarkAsReadResponse
```

Marks the requested Notification IDs as read and returns the number marked.
