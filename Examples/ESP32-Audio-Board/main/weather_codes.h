#pragma once

#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

// Spoken-friendly text for a WMO weather interpretation code, as reported by
// Open-Meteo. Pass has_code=false when the code is missing/unknown so it
// doesn't get mistaken for the (also valid) code 0 ("clear sky").
// Pure C, no ESP-IDF dependency — host-testable, mirrors VoiceAssistant's
// describe_weather_code() in app.py.
const char *describe_weather_code(int code, bool has_code);

#ifdef __cplusplus
}
#endif
