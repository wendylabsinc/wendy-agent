#pragma once

#include "cJSON.h"

#ifdef __cplusplus
extern "C" {
#endif

// Builds the "tools" array for the Realtime session.update payload:
// set_volume, adjust_volume, get_volume, get_weather, web_search. Mirrors
// VoiceAssistant's tool_specs() in app.py. Caller owns the returned array.
cJSON *tools_build_specs(void);

// Executes one tool call by name with its JSON-encoded arguments string.
// Always returns a JSON object (never NULL) — failures are encoded as
// {"error": "..."} rather than propagated, since a Realtime function tool
// must answer, not crash the session. Caller owns the returned object.
cJSON *tools_execute(const char *name, const char *arguments_json);

#ifdef __cplusplus
}
#endif
