#pragma once

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// Starts the realtime voice assistant: opens the WebSocket connection to
// OpenAI's Realtime API and spawns the mic-capture and speaker-playback
// tasks. Returns once everything is running — the spawned tasks and the
// WebSocket client's own internal task keep the assistant alive for the
// rest of the program, reconnecting automatically on drops.
esp_err_t realtime_client_start(void);

#ifdef __cplusplus
}
#endif
