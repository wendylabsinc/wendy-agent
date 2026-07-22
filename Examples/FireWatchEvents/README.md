# FireWatch Events

This minimal workload emits an operator alert through the supported local Wendy
Agent Event API. WendyOS attributes it to `dev.wendy.firewatch` and the enrolled
WendyBox; Cloud stores one notification per organization member and delivers it
to Companion through APNs. Tapping opens that device's **Live** view with the
stable `libcamera:front` camera selected.

```bash
wendy run --device <wendybox>
```

Override the sample detection data at deploy/runtime as appropriate:

- `FIREWATCH_EVENT_ID`: stable detection ID. Reuse the same value for retries;
  Cloud deduplicates by organization, device, app, and this ID.
- `FIREWATCH_CAMERA_ID`: stable `libcamera_id` reported by Wendy Agent.
- `FIREWATCH_MESSAGE`: operator-facing alert body.

The `{ "type": "events" }` entitlement mounts only an app-specific unix socket
and sets `WENDY_EVENT_SOCKET`. The workload does not provide `app_id`, device ID,
organization ID, or a URL: Agent and Cloud derive identity from authenticated
boundaries, and the only supported destination is the structured Live/camera
target.

`emit_event.py` retries transient `Unavailable`/`Aborted` responses with the same
ID. Other errors are surfaced to the workload; malformed text, source IDs, or
camera targets are rejected rather than silently altered. A successful response
means the event and recipient notifications are persisted. APNs delivery remains
best-effort, while the persisted notification is still available to Companion.

## See also

- [Events entitlement](https://docs.wendy.dev/apps/wendy-json#events)
- [App entitlements](https://docs.wendy.dev/device/entitlements)
