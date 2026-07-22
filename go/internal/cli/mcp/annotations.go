package mcp

import mcpgo "github.com/mark3labs/mcp-go/mcp"

// mcp-go's NewTool applies the MCP spec's pessimistic default annotations to
// every tool: ReadOnlyHint=false, DestructiveHint=true, IdempotentHint=false,
// OpenWorldHint=true. Relying on "absence" therefore advertises every tool as
// destructive and open-world. The helpers below set each axis EXPLICITLY so a
// tool's advertised behavior never depends on those hidden defaults.
//
// Convention: every registered tool applies exactly one behavior helper
// (readOnly / mutating / destructive) and exactly one world helper
// (localOnly / openWorld); idempotent() is an optional modifier for mutating
// tools (readOnly already implies idempotent).

// readOnly marks a tool that only reads state: not destructive, and idempotent
// by nature (repeated reads have no additional effect).
func readOnly() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{
		mcpgo.WithReadOnlyHintAnnotation(true),
		mcpgo.WithDestructiveHintAnnotation(false),
		mcpgo.WithIdempotentHintAnnotation(true),
	}
}

// mutating marks a tool that changes state but not destructively — it does not
// remove or irreversibly alter existing data (e.g. starting an app, connecting).
func mutating() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{
		mcpgo.WithReadOnlyHintAnnotation(false),
		mcpgo.WithDestructiveHintAnnotation(false),
	}
}

// destructive marks a tool that may perform irreversible or disruptive updates
// (e.g. deleting a container, updating the OS, dropping connectivity).
func destructive() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{
		mcpgo.WithReadOnlyHintAnnotation(false),
		mcpgo.WithDestructiveHintAnnotation(true),
	}
}

// idempotent marks a mutating tool where repeated identical calls have no extra
// effect beyond the first. (readOnly already sets this; do not stack the two.)
func idempotent() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{mcpgo.WithIdempotentHintAnnotation(true)}
}

// openWorld marks a tool that interacts with an open set of external entities
// the server does not own — the local network, nearby radios, or the cloud
// broker (e.g. mDNS scans, Wi-Fi/Bluetooth, cloud discovery).
func openWorld() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{mcpgo.WithOpenWorldHintAnnotation(true)}
}

// localOnly marks a tool whose domain of interaction is closed: it operates
// only on the connected device or the local host (e.g. device info, container
// operations, local config).
func localOnly() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{mcpgo.WithOpenWorldHintAnnotation(false)}
}
