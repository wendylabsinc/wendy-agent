// Realtime function-tool implementations: volume control (local, via the
// ES8311 codec), weather (Open-Meteo, keyless), and web search (OpenAI
// Responses API). Mirrors VoiceAssistant's tool_specs()/execute_tool() in
// app.py.

#include "tools.h"

#include <ctype.h>
#include <math.h>
#include <stdbool.h>
#include <stdlib.h>
#include <string.h>

#include "board_audio.h"
#include "esp_crt_bundle.h"
#include "esp_http_client.h"
#include "esp_log.h"
#include "secrets.h"
#include "text_utils.h"
#include "weather_codes.h"

static const char *TAG = "tools";

#define GEOCODING_URL "https://geocoding-api.open-meteo.com/v1/search"
#define FORECAST_URL "https://api.open-meteo.com/v1/forecast"
#define RESPONSES_URL "https://api.openai.com/v1/responses"
#define WEB_SEARCH_MODEL "gpt-5-mini"

// ---------------------------------------------------------------------------
// Tool specs (session.update "tools" array)
// ---------------------------------------------------------------------------

cJSON *tools_build_specs(void) {
    cJSON *specs = cJSON_CreateArray();

    cJSON *set_volume = cJSON_CreateObject();
    cJSON_AddStringToObject(set_volume, "type", "function");
    cJSON_AddStringToObject(set_volume, "name", "set_volume");
    cJSON_AddStringToObject(set_volume, "description",
                             "Set the speaker output volume to an absolute "
                             "percentage. Use for requests like 'set the "
                             "volume to 30 percent'.");
    {
        cJSON *params = cJSON_CreateObject();
        cJSON_AddStringToObject(params, "type", "object");
        cJSON *props = cJSON_CreateObject();
        cJSON *percent = cJSON_CreateObject();
        cJSON_AddStringToObject(percent, "type", "integer");
        cJSON_AddNumberToObject(percent, "minimum", 0);
        cJSON_AddNumberToObject(percent, "maximum", 100);
        cJSON_AddItemToObject(props, "percent", percent);
        cJSON_AddItemToObject(params, "properties", props);
        cJSON *required = cJSON_CreateArray();
        cJSON_AddItemToArray(required, cJSON_CreateString("percent"));
        cJSON_AddItemToObject(params, "required", required);
        cJSON_AddItemToObject(set_volume, "parameters", params);
    }
    cJSON_AddItemToArray(specs, set_volume);

    cJSON *adjust_volume = cJSON_CreateObject();
    cJSON_AddStringToObject(adjust_volume, "type", "function");
    cJSON_AddStringToObject(adjust_volume, "name", "adjust_volume");
    cJSON_AddStringToObject(adjust_volume, "description",
                             "Nudge the speaker volume up or down. Use for "
                             "relative requests like 'turn it up a bit' or "
                             "'quieter please'.");
    {
        cJSON *params = cJSON_CreateObject();
        cJSON_AddStringToObject(params, "type", "object");
        cJSON *props = cJSON_CreateObject();
        cJSON *direction = cJSON_CreateObject();
        cJSON_AddStringToObject(direction, "type", "string");
        cJSON *direction_enum = cJSON_CreateArray();
        cJSON_AddItemToArray(direction_enum, cJSON_CreateString("up"));
        cJSON_AddItemToArray(direction_enum, cJSON_CreateString("down"));
        cJSON_AddItemToObject(direction, "enum", direction_enum);
        cJSON_AddItemToObject(props, "direction", direction);
        cJSON *step = cJSON_CreateObject();
        cJSON_AddStringToObject(step, "type", "integer");
        cJSON_AddNumberToObject(step, "minimum", 1);
        cJSON_AddNumberToObject(step, "maximum", 100);
        cJSON_AddNumberToObject(step, "default", 10);
        cJSON_AddItemToObject(props, "step", step);
        cJSON_AddItemToObject(params, "properties", props);
        cJSON *required = cJSON_CreateArray();
        cJSON_AddItemToArray(required, cJSON_CreateString("direction"));
        cJSON_AddItemToObject(params, "required", required);
        cJSON_AddItemToObject(adjust_volume, "parameters", params);
    }
    cJSON_AddItemToArray(specs, adjust_volume);

    cJSON *get_volume = cJSON_CreateObject();
    cJSON_AddStringToObject(get_volume, "type", "function");
    cJSON_AddStringToObject(get_volume, "name", "get_volume");
    cJSON_AddStringToObject(get_volume, "description",
                             "Report the current speaker output volume "
                             "percentage.");
    {
        cJSON *params = cJSON_CreateObject();
        cJSON_AddStringToObject(params, "type", "object");
        cJSON_AddItemToObject(params, "properties", cJSON_CreateObject());
        cJSON_AddItemToObject(get_volume, "parameters", params);
    }
    cJSON_AddItemToArray(specs, get_volume);

    cJSON *get_weather = cJSON_CreateObject();
    cJSON_AddStringToObject(get_weather, "type", "function");
    cJSON_AddStringToObject(get_weather, "name", "get_weather");
    cJSON_AddStringToObject(get_weather, "description",
                             "Get the current weather and a 3-day forecast "
                             "for a named place. Fast and free — always use "
                             "this for weather questions instead of "
                             "web_search.");
    {
        cJSON *params = cJSON_CreateObject();
        cJSON_AddStringToObject(params, "type", "object");
        cJSON *props = cJSON_CreateObject();
        cJSON *location = cJSON_CreateObject();
        cJSON_AddStringToObject(location, "type", "string");
        cJSON_AddStringToObject(location, "description",
                                 "City or place name, e.g. 'Berlin' or "
                                 "'Portland, Oregon'");
        cJSON_AddItemToObject(props, "location", location);
        cJSON *units = cJSON_CreateObject();
        cJSON_AddStringToObject(units, "type", "string");
        cJSON *units_enum = cJSON_CreateArray();
        cJSON_AddItemToArray(units_enum, cJSON_CreateString("celsius"));
        cJSON_AddItemToArray(units_enum, cJSON_CreateString("fahrenheit"));
        cJSON_AddItemToObject(units, "enum", units_enum);
        cJSON_AddItemToObject(props, "units", units);
        cJSON_AddItemToObject(params, "properties", props);
        cJSON *required = cJSON_CreateArray();
        cJSON_AddItemToArray(required, cJSON_CreateString("location"));
        cJSON_AddItemToObject(params, "required", required);
        cJSON_AddItemToObject(get_weather, "parameters", params);
    }
    cJSON_AddItemToArray(specs, get_weather);

    cJSON *web_search = cJSON_CreateObject();
    cJSON_AddStringToObject(web_search, "type", "function");
    cJSON_AddStringToObject(web_search, "name", "web_search");
    cJSON_AddStringToObject(web_search, "description",
                             "Search the live web for current information: "
                             "news, sports scores, prices, or facts you are "
                             "not sure about. Slower and costs money — "
                             "never use it for weather (use get_weather) or "
                             "for things you already know.");
    {
        cJSON *params = cJSON_CreateObject();
        cJSON_AddStringToObject(params, "type", "object");
        cJSON *props = cJSON_CreateObject();
        cJSON *query = cJSON_CreateObject();
        cJSON_AddStringToObject(query, "type", "string");
        cJSON_AddStringToObject(query, "description", "A concise search query");
        cJSON_AddItemToObject(props, "query", query);
        cJSON_AddItemToObject(params, "properties", props);
        cJSON *required = cJSON_CreateArray();
        cJSON_AddItemToArray(required, cJSON_CreateString("query"));
        cJSON_AddItemToObject(params, "required", required);
        cJSON_AddItemToObject(web_search, "parameters", params);
    }
    cJSON_AddItemToArray(specs, web_search);

    return specs;
}

// ---------------------------------------------------------------------------
// HTTPS helpers
// ---------------------------------------------------------------------------

typedef struct {
    char *buf;
    size_t len;
    size_t cap;
} http_body_t;

static esp_err_t http_event_handler(esp_http_client_event_t *evt) {
    if (evt->event_id != HTTP_EVENT_ON_DATA) {
        return ESP_OK;
    }
    http_body_t *body = (http_body_t *)evt->user_data;
    size_t need = body->len + evt->data_len + 1;
    if (need > body->cap) {
        size_t new_cap = need + need / 2;
        char *grown = realloc(body->buf, new_cap);
        if (!grown) {
            return ESP_FAIL;
        }
        body->buf = grown;
        body->cap = new_cap;
    }
    memcpy(body->buf + body->len, evt->data, evt->data_len);
    body->len += evt->data_len;
    body->buf[body->len] = '\0';
    return ESP_OK;
}

// GET when post_body is NULL, POST with a JSON body (and Authorization
// header) otherwise. Returns a malloc'd response body the caller frees, or
// NULL on any transport/HTTP-status failure.
static char *http_request(const char *url, const char *auth_header,
                           const char *post_body) {
    http_body_t body = {0};
    esp_http_client_config_t config = {
        .url = url,
        .method = post_body ? HTTP_METHOD_POST : HTTP_METHOD_GET,
        .event_handler = http_event_handler,
        .user_data = &body,
        .crt_bundle_attach = esp_crt_bundle_attach,
        .timeout_ms = post_body ? 45000 : 15000,
    };
    esp_http_client_handle_t client = esp_http_client_init(&config);
    if (!client) {
        return NULL;
    }
    if (auth_header) {
        esp_http_client_set_header(client, "Authorization", auth_header);
    }
    if (post_body) {
        esp_http_client_set_header(client, "Content-Type", "application/json");
        esp_http_client_set_post_field(client, post_body, strlen(post_body));
    }

    esp_err_t err = esp_http_client_perform(client);
    int status = esp_http_client_get_status_code(client);
    esp_http_client_cleanup(client);

    if (err != ESP_OK || status < 200 || status >= 300) {
        ESP_LOGW(TAG, "HTTP request to %s failed: err=%s status=%d", url,
                 esp_err_to_name(err), status);
        free(body.buf);
        return NULL;
    }
    return body.buf ? body.buf : strdup("");
}

// Percent-encodes a query-string value (RFC 3986 unreserved chars pass
// through; everything else, including spaces, becomes %XX).
static char *percent_encode(const char *s) {
    size_t len = strlen(s);
    char *out = malloc(len * 3 + 1);
    if (!out) {
        return NULL;
    }
    size_t oi = 0;
    for (size_t i = 0; i < len; i++) {
        unsigned char c = (unsigned char)s[i];
        if (isalnum(c) || c == '-' || c == '_' || c == '.' || c == '~') {
            out[oi++] = (char)c;
        } else {
            oi += snprintf(out + oi, 4, "%%%02X", c);
        }
    }
    out[oi] = '\0';
    return out;
}

// ---------------------------------------------------------------------------
// Volume tools (local, no network)
// ---------------------------------------------------------------------------

static cJSON *do_set_volume(cJSON *args) {
    cJSON *percent_item = cJSON_GetObjectItem(args, "percent");
    if (!cJSON_IsNumber(percent_item)) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "percent is required");
        return err;
    }
    int percent = percent_item->valueint;
    if (percent < 0) percent = 0;
    if (percent > 100) percent = 100;
    if (board_audio_set_volume(percent) != ESP_OK) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "failed to set volume");
        return err;
    }
    cJSON *result = cJSON_CreateObject();
    cJSON_AddNumberToObject(result, "volume_percent", percent);
    return result;
}

static cJSON *do_get_volume(void) {
    int percent = 0;
    if (board_audio_get_volume(&percent) != ESP_OK) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "failed to read volume");
        return err;
    }
    cJSON *result = cJSON_CreateObject();
    cJSON_AddNumberToObject(result, "volume_percent", percent);
    return result;
}

static cJSON *do_adjust_volume(cJSON *args) {
    cJSON *direction_item = cJSON_GetObjectItem(args, "direction");
    const char *direction =
        cJSON_IsString(direction_item) ? direction_item->valuestring : NULL;
    if (!direction || (strcmp(direction, "up") != 0 && strcmp(direction, "down") != 0)) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error",
                                 "direction must be 'up' or 'down'");
        return err;
    }
    cJSON *step_item = cJSON_GetObjectItem(args, "step");
    int step = cJSON_IsNumber(step_item) ? step_item->valueint : 10;

    int current = 0;
    if (board_audio_get_volume(&current) != ESP_OK) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "failed to read volume");
        return err;
    }
    int target = current + (strcmp(direction, "up") == 0 ? step : -step);
    if (target < 0) target = 0;
    if (target > 100) target = 100;
    if (board_audio_set_volume(target) != ESP_OK) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "failed to set volume");
        return err;
    }
    cJSON *result = cJSON_CreateObject();
    cJSON_AddNumberToObject(result, "volume_percent", target);
    return result;
}

// ---------------------------------------------------------------------------
// get_weather (Open-Meteo, keyless)
// ---------------------------------------------------------------------------

static double num_or(cJSON *obj, const char *key, double fallback) {
    cJSON *item = cJSON_GetObjectItem(obj, key);
    return cJSON_IsNumber(item) ? item->valuedouble : fallback;
}

static cJSON *do_get_weather(cJSON *args) {
    cJSON *location_item = cJSON_GetObjectItem(args, "location");
    const char *location =
        cJSON_IsString(location_item) ? location_item->valuestring : NULL;
    if (!location || strlen(location) == 0) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "location is required");
        return err;
    }
    cJSON *units_item = cJSON_GetObjectItem(args, "units");
    const char *units =
        cJSON_IsString(units_item) ? units_item->valuestring : "celsius";
    bool fahrenheit = strcmp(units, "fahrenheit") == 0;

    char *encoded_location = percent_encode(location);
    if (!encoded_location) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "out of memory");
        return err;
    }
    char geocode_url[512];
    snprintf(geocode_url, sizeof(geocode_url),
              "%s?name=%s&count=1&language=en&format=json", GEOCODING_URL,
              encoded_location);
    free(encoded_location);

    char *geocode_body = http_request(geocode_url, NULL, NULL);
    if (!geocode_body) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "weather lookup failed (network)");
        return err;
    }
    cJSON *geocode_json = cJSON_Parse(geocode_body);
    free(geocode_body);
    if (!geocode_json) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "weather lookup returned invalid JSON");
        return err;
    }
    cJSON *results = cJSON_GetObjectItem(geocode_json, "results");
    cJSON *place = (cJSON_IsArray(results) && cJSON_GetArraySize(results) > 0)
                       ? cJSON_GetArrayItem(results, 0)
                       : NULL;
    if (!place) {
        cJSON *err = cJSON_CreateObject();
        char msg[160];
        snprintf(msg, sizeof(msg), "no location found matching '%s'", location);
        cJSON_AddStringToObject(err, "error", msg);
        cJSON_Delete(geocode_json);
        return err;
    }

    double lat = num_or(place, "latitude", 0);
    double lon = num_or(place, "longitude", 0);
    const char *name_parts[3];
    int name_part_count = 0;
    const char *candidates[3] = {
        cJSON_GetStringValue(cJSON_GetObjectItem(place, "name")),
        cJSON_GetStringValue(cJSON_GetObjectItem(place, "admin1")),
        cJSON_GetStringValue(cJSON_GetObjectItem(place, "country")),
    };
    for (int i = 0; i < 3; i++) {
        if (!candidates[i] || strlen(candidates[i]) == 0) {
            continue;
        }
        bool dup = false;
        for (int j = 0; j < name_part_count; j++) {
            if (strcmp(name_parts[j], candidates[i]) == 0) {
                dup = true;
                break;
            }
        }
        if (!dup) {
            name_parts[name_part_count++] = candidates[i];
        }
    }
    char location_label[192] = {0};
    for (int i = 0; i < name_part_count; i++) {
        if (i > 0) strlcat(location_label, ", ", sizeof(location_label));
        strlcat(location_label, name_parts[i], sizeof(location_label));
    }
    cJSON_Delete(geocode_json);

    char forecast_url[512];
    snprintf(forecast_url, sizeof(forecast_url),
              "%s?latitude=%.6f&longitude=%.6f"
              "&current=temperature_2m,apparent_temperature,relative_humidity_2m,"
              "weather_code,wind_speed_10m,is_day"
              "&daily=weather_code,temperature_2m_max,temperature_2m_min,"
              "precipitation_probability_max"
              "&forecast_days=3&timezone=auto%s",
              FORECAST_URL, lat, lon,
              fahrenheit ? "&temperature_unit=fahrenheit&wind_speed_unit=mph" : "");

    char *forecast_body = http_request(forecast_url, NULL, NULL);
    if (!forecast_body) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "forecast lookup failed (network)");
        return err;
    }
    cJSON *forecast_json = cJSON_Parse(forecast_body);
    free(forecast_body);
    if (!forecast_json) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "forecast lookup returned invalid JSON");
        return err;
    }

    cJSON *current = cJSON_GetObjectItem(forecast_json, "current");
    cJSON *daily = cJSON_GetObjectItem(forecast_json, "daily");

    cJSON *result = cJSON_CreateObject();
    cJSON_AddStringToObject(result, "location", location_label);
    cJSON_AddStringToObject(result, "units", units);

    cJSON *current_out = cJSON_CreateObject();
    cJSON_AddNumberToObject(current_out, "temperature", num_or(current, "temperature_2m", NAN));
    cJSON_AddNumberToObject(current_out, "feels_like", num_or(current, "apparent_temperature", NAN));
    cJSON_AddNumberToObject(current_out, "humidity_percent", num_or(current, "relative_humidity_2m", NAN));
    cJSON_AddNumberToObject(current_out, "wind_speed", num_or(current, "wind_speed_10m", NAN));
    cJSON *wcode_item = cJSON_GetObjectItem(current, "weather_code");
    cJSON_AddStringToObject(
        current_out, "conditions",
        describe_weather_code(cJSON_IsNumber(wcode_item) ? wcode_item->valueint : 0,
                               cJSON_IsNumber(wcode_item)));
    cJSON_AddItemToObject(result, "current", current_out);

    cJSON *daily_out = cJSON_CreateArray();
    cJSON *times = cJSON_GetObjectItem(daily, "time");
    int day_count = cJSON_IsArray(times) ? cJSON_GetArraySize(times) : 0;
    cJSON *daily_codes = cJSON_GetObjectItem(daily, "weather_code");
    cJSON *daily_highs = cJSON_GetObjectItem(daily, "temperature_2m_max");
    cJSON *daily_lows = cJSON_GetObjectItem(daily, "temperature_2m_min");
    cJSON *daily_precip = cJSON_GetObjectItem(daily, "precipitation_probability_max");
    for (int i = 0; i < day_count; i++) {
        cJSON *day = cJSON_CreateObject();
        cJSON *time_item = cJSON_GetArrayItem(times, i);
        cJSON_AddStringToObject(day, "date",
                                 cJSON_IsString(time_item) ? time_item->valuestring : "");
        cJSON *high_item = cJSON_GetArrayItem(daily_highs, i);
        cJSON *low_item = cJSON_GetArrayItem(daily_lows, i);
        cJSON_AddNumberToObject(day, "high", cJSON_IsNumber(high_item) ? high_item->valuedouble : NAN);
        cJSON_AddNumberToObject(day, "low", cJSON_IsNumber(low_item) ? low_item->valuedouble : NAN);
        cJSON *code_item = cJSON_GetArrayItem(daily_codes, i);
        cJSON_AddStringToObject(
            day, "conditions",
            describe_weather_code(cJSON_IsNumber(code_item) ? code_item->valueint : 0,
                                   cJSON_IsNumber(code_item)));
        cJSON *precip_item = cJSON_GetArrayItem(daily_precip, i);
        cJSON_AddNumberToObject(day, "precipitation_chance_percent",
                                 cJSON_IsNumber(precip_item) ? precip_item->valuedouble : NAN);
        cJSON_AddItemToArray(daily_out, day);
    }
    cJSON_AddItemToObject(result, "daily", daily_out);

    cJSON_Delete(forecast_json);
    return result;
}

// ---------------------------------------------------------------------------
// web_search (OpenAI Responses API, built-in web_search tool)
// ---------------------------------------------------------------------------

static cJSON *do_web_search(cJSON *args) {
    cJSON *query_item = cJSON_GetObjectItem(args, "query");
    const char *query = cJSON_IsString(query_item) ? query_item->valuestring : NULL;
    if (!query || strlen(query) == 0) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "query is required");
        return err;
    }

    cJSON *body_json = cJSON_CreateObject();
    cJSON_AddStringToObject(body_json, "model", WEB_SEARCH_MODEL);
    cJSON_AddStringToObject(body_json, "input", query);
    cJSON_AddStringToObject(
        body_json, "instructions",
        "You are a silent search backend for a voice assistant; the "
        "assistant has already acknowledged the user. Reply with 1-3 short "
        "spoken-style sentences of concrete facts, current as of today. "
        "Start directly with the facts — no greetings, acknowledgements, or "
        "lead-ins like 'Got it' or 'Here's what's going on'. No markdown or "
        "URLs. Never ask clarifying questions — this is a one-shot search, "
        "so pick the most likely interpretation of the query, search, and "
        "answer.");
    cJSON *tools = cJSON_CreateArray();
    cJSON *web_search_tool = cJSON_CreateObject();
    cJSON_AddStringToObject(web_search_tool, "type", "web_search");
    cJSON_AddItemToArray(tools, web_search_tool);
    cJSON_AddItemToObject(body_json, "tools", tools);
    cJSON *reasoning = cJSON_CreateObject();
    cJSON_AddStringToObject(reasoning, "effort", "low");
    cJSON_AddItemToObject(body_json, "reasoning", reasoning);

    char *body_text = cJSON_PrintUnformatted(body_json);
    cJSON_Delete(body_json);
    if (!body_text) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "out of memory");
        return err;
    }

    char auth_header[8 + sizeof(OPENAI_API_KEY)];
    snprintf(auth_header, sizeof(auth_header), "Bearer %s", OPENAI_API_KEY);
    char *response_body = http_request(RESPONSES_URL, auth_header, body_text);
    free(body_text);
    if (!response_body) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "web search failed (network)");
        return err;
    }

    cJSON *response_json = cJSON_Parse(response_body);
    free(response_body);
    if (!response_json) {
        cJSON *err = cJSON_CreateObject();
        cJSON_AddStringToObject(err, "error", "web search returned invalid JSON");
        return err;
    }

    // Concatenate output_text content, mirroring VoiceAssistant's
    // extract_output_text().
    char answer[2048] = {0};
    cJSON *output = cJSON_GetObjectItem(response_json, "output");
    if (cJSON_IsArray(output)) {
        cJSON *item;
        cJSON_ArrayForEach(item, output) {
            cJSON *type_item = cJSON_GetObjectItem(item, "type");
            if (!cJSON_IsString(type_item) || strcmp(type_item->valuestring, "message") != 0) {
                continue;
            }
            cJSON *content = cJSON_GetObjectItem(item, "content");
            cJSON *content_item;
            cJSON_ArrayForEach(content_item, content) {
                cJSON *ctype = cJSON_GetObjectItem(content_item, "type");
                cJSON *text = cJSON_GetObjectItem(content_item, "text");
                if (cJSON_IsString(ctype) && strcmp(ctype->valuestring, "output_text") == 0 &&
                    cJSON_IsString(text)) {
                    strlcat(answer, text->valuestring, sizeof(answer));
                }
            }
        }
    }
    cJSON_Delete(response_json);

    char *clean_answer = strip_citations(answer);
    cJSON *result = cJSON_CreateObject();
    if (!clean_answer || strlen(clean_answer) == 0) {
        cJSON_AddStringToObject(result, "error", "web search returned no answer");
    } else {
        cJSON_AddStringToObject(result, "answer", clean_answer);
    }
    free(clean_answer);
    return result;
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

cJSON *tools_execute(const char *name, const char *arguments_json) {
    cJSON *args = NULL;
    if (arguments_json && strlen(arguments_json) > 0) {
        args = cJSON_Parse(arguments_json);
    }
    if (!args) {
        args = cJSON_CreateObject();
    }

    cJSON *result;
    if (strcmp(name, "set_volume") == 0) {
        result = do_set_volume(args);
    } else if (strcmp(name, "adjust_volume") == 0) {
        result = do_adjust_volume(args);
    } else if (strcmp(name, "get_volume") == 0) {
        result = do_get_volume();
    } else if (strcmp(name, "get_weather") == 0) {
        result = do_get_weather(args);
    } else if (strcmp(name, "web_search") == 0) {
        result = do_web_search(args);
    } else {
        result = cJSON_CreateObject();
        char msg[64];
        snprintf(msg, sizeof(msg), "unknown tool: %s", name);
        cJSON_AddStringToObject(result, "error", msg);
    }
    cJSON_Delete(args);
    return result;
}
