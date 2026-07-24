# Notifications API

The `NotificationService` gRPC service creates, lists, and manages
operator-facing Wendy Notifications in a Wendy Cloud organization.

## Proto package

`wendycloud.v1` — defined in `Proto/cloud/notifications.proto`.

## Methods

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
| `audience` | `NotificationAudience` | Exactly one user ID, organization team ID, or organization role. |
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
| `recipient_count` | `int32` | Number of resolved recipients. |

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
