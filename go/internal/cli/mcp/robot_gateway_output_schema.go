package mcp

import "encoding/json"

// Schemas describe the structured results exposed by the standalone Cloud
// gateway. Private/local tools retain their independently defined contracts.
func gatewayOutputSchema(name string) json.RawMessage {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	stringsArray := map[string]any{"type": "array", "items": str}
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	array := func(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
	bounded := func(schema map[string]any) map[string]any {
		return map[string]any{"type": "object", "anyOf": []any{schema, object(map[string]any{"truncated": map[string]any{"const": true}, "max_bytes": integer, "bytes": integer, "note": str}, "truncated", "max_bytes", "bytes", "note")}}
	}
	var schema map[string]any
	switch name {
	case "list_robots", "open_robot", "open_devices":
		robot := object(map[string]any{"id": str, "name": str, "connection": str, "cloud_presence": str, "source": str, "device_type": str, "model": str, "can_read_events": boolean, "can_deploy_detector": boolean, "can_capture": boolean, "can_control_apps": boolean}, "id", "name", "connection", "source", "can_capture", "can_control_apps")
		schema = object(map[string]any{"robots": array(robot), "total_count": integer, "simulator_count": integer, "next_offset": map[string]any{"type": []string{"integer", "null"}}, "include_offline": boolean, "discovery_complete": boolean, "warnings": map[string]any{"type": []string{"array", "null"}, "items": str}, "can_manage_simulators": boolean, "note": str, "selected_robot_id": str}, "robots", "total_count", "simulator_count", "next_offset", "include_offline", "discovery_complete", "warnings", "can_manage_simulators", "note")
	case "identify_device":
		schema = object(map[string]any{"robot_id": str, "device_type": str, "model": str, "agent_version": str}, "robot_id", "device_type", "model", "agent_version")
	case "inspect_robot":
		app := object(map[string]any{"name": str, "version": str, "state": str, "failure_count": integer, "readiness": str, "http_port": integer, "can_control": boolean}, "name", "version", "state", "failure_count", "readiness", "http_port", "can_control")
		camera := object(map[string]any{"id": integer, "name": str}, "id", "name")
		schema = bounded(object(map[string]any{"robot_id": str, "name": str, "connected": boolean, "agent_version": str, "device_type": str, "model": str, "observed_at": str, "apps": array(app), "cameras": array(camera), "warnings": stringsArray, "can_open_apps": boolean}, "robot_id", "name", "connected", "agent_version", "device_type", "model", "observed_at", "cameras", "warnings", "can_open_apps"))
	case "start_robot_app", "stop_robot_app":
		schema = object(map[string]any{"robot_id": str, "app_name": str, "action": map[string]any{"enum": []string{"start", "stop"}}, "state": str, "readiness": str, "observed_at": str, "failure_count": integer, "state_verified": boolean, "note": str}, "robot_id", "app_name", "action", "state", "readiness", "observed_at", "state_verified")
	case "capture_robot_image":
		schema = object(map[string]any{"robot_id": str, "camera_id": integer, "kind": map[string]any{"const": "camera_snapshot"}, "live": map[string]any{"const": false}, "device_id": integer, "width": integer, "height": integer, "mime_type": map[string]any{"const": "image/jpeg"}, "received_at": str, "note": str, "stream_timestamp_ns": integer}, "robot_id", "camera_id", "kind", "live", "device_id", "width", "height", "mime_type", "received_at", "note")
	case "start_camera_preview":
		schema = object(map[string]any{"preview_id": str, "robot_id": str, "camera_id": integer, "live": map[string]any{"const": true}, "expires_in_seconds": integer}, "preview_id", "robot_id", "camera_id", "live", "expires_in_seconds")
	case "read_camera_preview":
		schema = object(map[string]any{"sequence": integer, "received_at": str, "width": integer, "height": integer, "live": map[string]any{"const": true}}, "sequence", "received_at", "width", "height", "live")
	case "stop_camera_preview":
		schema = object(map[string]any{"stopped": map[string]any{"const": true}}, "stopped")
	case "update_device_settings":
		schema = object(map[string]any{"values": object(map[string]any{"show_3d": boolean, "include_offline": boolean})}, "values")
	case "read_device_logs", "read_device_metrics":
		key := "logs"
		if name == "read_device_metrics" {
			key = "metrics"
		}
		// Compact OTLP rows deliberately preserve vendor measurement fields,
		// resource attributes and instrumentation scope. Their keys are open.
		properties := map[string]any{key: array(map[string]any{"type": "object", "additionalProperties": true}), "batches_collected": integer, "collection_limited": boolean, "truncated": boolean, "returned": integer, "omitted": integer}
		required := []string{key, "batches_collected", "collection_limited"}
		if name == "read_device_metrics" {
			properties["last_n"], properties["history_batches"], properties["newest_first"] = integer, integer, boolean
			required = append(required, "last_n", "history_batches", "newest_first")
		}
		schema = bounded(object(properties, required...))
	default:
		return nil
	}
	encoded, _ := json.Marshal(schema)
	return encoded
}
