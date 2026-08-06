#include "weather_codes.h"

#include <stddef.h>

typedef struct {
    int code;
    const char *description;
} weather_code_entry_t;

// Mirrors VoiceAssistant's _WMO_WEATHER_CODES table (app.py) exactly.
static const weather_code_entry_t kWeatherCodes[] = {
    {0, "clear sky"},
    {1, "mainly clear"},
    {2, "partly cloudy"},
    {3, "overcast"},
    {45, "fog"},
    {48, "depositing rime fog"},
    {51, "light drizzle"},
    {53, "moderate drizzle"},
    {55, "dense drizzle"},
    {56, "light freezing drizzle"},
    {57, "dense freezing drizzle"},
    {61, "light rain"},
    {63, "moderate rain"},
    {65, "heavy rain"},
    {66, "light freezing rain"},
    {67, "heavy freezing rain"},
    {71, "light snow"},
    {73, "moderate snow"},
    {75, "heavy snow"},
    {77, "snow grains"},
    {80, "light rain showers"},
    {81, "moderate rain showers"},
    {82, "violent rain showers"},
    {85, "light snow showers"},
    {86, "heavy snow showers"},
    {95, "thunderstorm"},
    {96, "thunderstorm with light hail"},
    {99, "thunderstorm with heavy hail"},
};
#define kWeatherCodesCount (sizeof(kWeatherCodes) / sizeof(kWeatherCodes[0]))

const char *describe_weather_code(int code, bool has_code) {
    if (has_code) {
        for (size_t i = 0; i < kWeatherCodesCount; i++) {
            if (kWeatherCodes[i].code == code) {
                return kWeatherCodes[i].description;
            }
        }
    }
    return "unknown conditions";
}
