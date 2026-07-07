# wendy-agent logging conventions

The agent logs through `zap` and bridges every entry to an OTel `LogRecord`
(`internal/agent/services/telemetry_core.go`). Follow these rules so exported
logs stay consistent and queryable.

- **Keys are snake_case.** e.g. `app_id`, `container_id`, `artifact_url`.
- **Use the `logfields` constants** for the common, reused fields instead of
  string literals. Add new constants there rather than inventing keys inline.
- **Errors:** log with `zap.Error(err)` — this produces the `error` key.
- **Canonical names for shared concepts:**
  - container identifier → `container_id` (not `container`)
  - container human name → `container_name`
  - app/compose service name → `service_name` (not `service`)
- **Timestamps:** `zap.Time(key, t)` exports as an RFC3339Nano string.
