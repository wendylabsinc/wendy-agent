// OpenAI Realtime WebSocket session: connects, negotiates the session,
// pumps mic audio in, plays speaker audio out, and dispatches function-tool
// calls. Mirrors VoiceAssistant's app.py, adapted to FreeRTOS tasks instead
// of asyncio coroutines.

#include "realtime_client.h"

#include <stdbool.h>
#include <stdlib.h>
#include <string.h>

#include "board_audio.h"
#include "cJSON.h"
#include "esp_crt_bundle.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_websocket_client.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "mbedtls/base64.h"
#include "secrets.h"
#include "tools.h"

static const char *TAG = "realtime";

#define REALTIME_MODEL "gpt-realtime-2.1"
#define ASSISTANT_VOICE "marin"
#define ASSISTANT_INSTRUCTIONS                                               \
    "You are a friendly voice assistant running on an ESP32 microcontroller. "\
    "Be conversational, helpful, and concise. Reply in the language the "    \
    "user speaks. You can change your own speaker volume, look up the "      \
    "current weather and forecast anywhere, and search the web for "        \
    "up-to-date information with the provided tools."

#define MIC_CHUNK_MS 100
#define MIC_CHUNK_FRAMES (BOARD_AUDIO_SAMPLE_RATE * MIC_CHUNK_MS / 1000)
#define PLAYBACK_QUEUE_LEN 32
#define RX_MAX_BYTES (64 * 1024)

typedef struct {
    int16_t *pcm;
    size_t frames;
} playback_chunk_t;

static esp_websocket_client_handle_t s_client;
static SemaphoreHandle_t s_send_mutex;
static QueueHandle_t s_playback_queue;
static char s_ws_headers[32 + sizeof(OPENAI_API_KEY)];

// Incoming-message reassembly (esp_websocket_client delivers large text
// frames across multiple WEBSOCKET_EVENT_DATA callbacks).
static uint8_t *s_rx_buf;
static size_t s_rx_len;
static size_t s_rx_cap;

// Playback/barge-in bookkeeping. Only ever touched from the WebSocket
// client's own event-dispatch task, so no locking is needed here.
static bool s_assistant_speaking;
static bool s_user_speaking;
static char *s_current_item_id;
static int s_current_content_index;
static char *s_interrupted_item_id;
static int64_t s_playback_started_at_us;
static double s_playback_audio_seconds;
static int64_t s_playback_deadline_us;

// Accumulates response.output_audio_transcript.delta for one log line per
// reply, instead of interleaved partial logs.
static char *s_transcript_buf;
static size_t s_transcript_len;
static size_t s_transcript_cap;

static void set_str(char **dst, const char *src) {
    free(*dst);
    *dst = src ? strdup(src) : NULL;
}

// ---------------------------------------------------------------------------
// Outbound sends
// ---------------------------------------------------------------------------

// Takes ownership of `msg` (always deletes it). Safe to call concurrently
// from the mic task, tool-call tasks, and the WebSocket event callback.
static void send_json(cJSON *msg) {
    char *text = cJSON_PrintUnformatted(msg);
    cJSON_Delete(msg);
    if (!text) {
        return;
    }
    if (s_client && esp_websocket_client_is_connected(s_client)) {
        xSemaphoreTake(s_send_mutex, portMAX_DELAY);
        esp_websocket_client_send_text(s_client, text, (int)strlen(text),
                                        pdMS_TO_TICKS(2000));
        xSemaphoreGive(s_send_mutex);
    }
    free(text);
}

static void send_session_update(void) {
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "session.update");

    cJSON *session = cJSON_CreateObject();
    cJSON_AddStringToObject(session, "type", "realtime");
    cJSON_AddStringToObject(session, "model", REALTIME_MODEL);
    cJSON *modalities = cJSON_CreateArray();
    cJSON_AddItemToArray(modalities, cJSON_CreateString("audio"));
    cJSON_AddItemToObject(session, "output_modalities", modalities);
    cJSON_AddStringToObject(session, "instructions", ASSISTANT_INSTRUCTIONS);
    cJSON_AddItemToObject(session, "tools", tools_build_specs());
    cJSON_AddStringToObject(session, "tool_choice", "auto");

    cJSON *audio = cJSON_CreateObject();

    cJSON *input = cJSON_CreateObject();
    cJSON *input_format = cJSON_CreateObject();
    cJSON_AddStringToObject(input_format, "type", "audio/pcm");
    cJSON_AddNumberToObject(input_format, "rate", BOARD_AUDIO_SAMPLE_RATE);
    cJSON_AddItemToObject(input, "format", input_format);
    cJSON *turn_detection = cJSON_CreateObject();
    cJSON_AddStringToObject(turn_detection, "type", "semantic_vad");
    cJSON_AddBoolToObject(turn_detection, "create_response", true);
    cJSON_AddBoolToObject(turn_detection, "interrupt_response", true);
    cJSON_AddItemToObject(input, "turn_detection", turn_detection);
    cJSON_AddItemToObject(audio, "input", input);

    cJSON *output = cJSON_CreateObject();
    cJSON *output_format = cJSON_CreateObject();
    cJSON_AddStringToObject(output_format, "type", "audio/pcm");
    cJSON_AddNumberToObject(output_format, "rate", BOARD_AUDIO_SAMPLE_RATE);
    cJSON_AddItemToObject(output, "format", output_format);
    cJSON_AddStringToObject(output, "voice", ASSISTANT_VOICE);
    cJSON_AddItemToObject(audio, "output", output);

    cJSON_AddItemToObject(session, "audio", audio);
    cJSON_AddItemToObject(root, "session", session);
    send_json(root);
}

// ---------------------------------------------------------------------------
// Transcript accumulation
// ---------------------------------------------------------------------------

static void transcript_append(const char *delta) {
    size_t add_len = strlen(delta);
    size_t need = s_transcript_len + add_len + 1;
    if (need > s_transcript_cap) {
        size_t new_cap = need + need / 2;
        char *grown = realloc(s_transcript_buf, new_cap);
        if (!grown) {
            return;
        }
        s_transcript_buf = grown;
        s_transcript_cap = new_cap;
    }
    memcpy(s_transcript_buf + s_transcript_len, delta, add_len);
    s_transcript_len += add_len;
    s_transcript_buf[s_transcript_len] = '\0';
}

static void transcript_reset(void) {
    s_transcript_len = 0;
    if (s_transcript_buf) {
        s_transcript_buf[0] = '\0';
    }
}

// ---------------------------------------------------------------------------
// Mic capture + speaker playback tasks
// ---------------------------------------------------------------------------

static void mic_task(void *arg) {
    (void)arg;
    int16_t pcm[MIC_CHUNK_FRAMES];
    size_t b64_cap = 4 * ((sizeof(pcm) + 2) / 3) + 1;
    char *b64 = malloc(b64_cap);

    for (;;) {
        size_t frames_read = 0;
        if (board_audio_read(pcm, MIC_CHUNK_FRAMES, &frames_read) != ESP_OK ||
            frames_read == 0) {
            vTaskDelay(pdMS_TO_TICKS(10));
            continue;
        }
        if (!s_client || !esp_websocket_client_is_connected(s_client)) {
            continue; // drop mic frames while disconnected
        }
        size_t olen = 0;
        mbedtls_base64_encode((unsigned char *)b64, b64_cap, &olen,
                               (const unsigned char *)pcm,
                               frames_read * sizeof(int16_t));
        b64[olen] = '\0';

        cJSON *msg = cJSON_CreateObject();
        cJSON_AddStringToObject(msg, "type", "input_audio_buffer.append");
        cJSON_AddStringToObject(msg, "audio", b64);
        send_json(msg);
    }
}

static void playback_task(void *arg) {
    (void)arg;
    playback_chunk_t chunk;
    for (;;) {
        if (xQueueReceive(s_playback_queue, &chunk, portMAX_DELAY) == pdTRUE) {
            board_audio_write(chunk.pcm, chunk.frames);
            free(chunk.pcm);
        }
    }
}

// ---------------------------------------------------------------------------
// Barge-in
// ---------------------------------------------------------------------------

static void handle_interrupt(void) {
    int64_t now = esp_timer_get_time();
    double played_s = s_playback_audio_seconds;
    if (s_playback_started_at_us > 0) {
        double elapsed_s = (double)(now - s_playback_started_at_us) / 1e6;
        if (elapsed_s < played_s) {
            played_s = elapsed_s;
        }
    }

    // Drain queued-but-not-yet-played chunks. Whatever's already in the I2S
    // DMA buffer still plays out — a small, bounded residual (there's no
    // AEC chip on this board, so this is the tradeoff of enabling barge-in).
    playback_chunk_t chunk;
    while (xQueueReceive(s_playback_queue, &chunk, 0) == pdTRUE) {
        free(chunk.pcm);
    }
    s_assistant_speaking = false;

    if (s_current_item_id && s_client && esp_websocket_client_is_connected(s_client)) {
        cJSON *msg = cJSON_CreateObject();
        cJSON_AddStringToObject(msg, "type", "conversation.item.truncate");
        cJSON_AddStringToObject(msg, "item_id", s_current_item_id);
        cJSON_AddNumberToObject(msg, "content_index", s_current_content_index);
        cJSON_AddNumberToObject(msg, "audio_end_ms", (int)(played_s * 1000));
        send_json(msg);
    }
    ESP_LOGI(TAG, "Interrupted - listening...");

    set_str(&s_interrupted_item_id, s_current_item_id);
    set_str(&s_current_item_id, NULL);
    s_current_content_index = 0;
    s_playback_deadline_us = 0;
    s_playback_started_at_us = 0;
    s_playback_audio_seconds = 0;
}

// ---------------------------------------------------------------------------
// Tool calls
// ---------------------------------------------------------------------------

typedef struct {
    char *name;
    char *arguments_json;
    char *call_id;
} tool_call_ctx_t;

static void tool_call_task(void *arg) {
    tool_call_ctx_t *ctx = (tool_call_ctx_t *)arg;

    cJSON *result = tools_execute(ctx->name, ctx->arguments_json);
    char *result_text = cJSON_PrintUnformatted(result);
    cJSON_Delete(result);
    ESP_LOGI(TAG, "Tool %s(%s) -> %s", ctx->name,
             ctx->arguments_json ? ctx->arguments_json : "",
             result_text ? result_text : "(no result)");

    cJSON *msg = cJSON_CreateObject();
    cJSON_AddStringToObject(msg, "type", "conversation.item.create");
    cJSON *item = cJSON_CreateObject();
    cJSON_AddStringToObject(item, "type", "function_call_output");
    cJSON_AddStringToObject(item, "call_id", ctx->call_id);
    cJSON_AddStringToObject(item, "output", result_text ? result_text : "{}");
    cJSON_AddItemToObject(msg, "item", item);
    send_json(msg);
    free(result_text);

    // The output is in the conversation; the model's next turn will see it.
    // Forcing a response now would race semantic VAD's turn taking.
    if (!s_user_speaking) {
        cJSON *resp = cJSON_CreateObject();
        cJSON_AddStringToObject(resp, "type", "response.create");
        send_json(resp);
    } else {
        ESP_LOGI(TAG, "User is speaking; deferring the spoken tool result");
    }

    free(ctx->name);
    free(ctx->arguments_json);
    free(ctx->call_id);
    free(ctx);
    vTaskDelete(NULL);
}

// Runs on its own task so a slow tool call (weather/search HTTP requests)
// never blocks the WebSocket receive loop or barge-in detection.
static void dispatch_tool_call(cJSON *item) {
    cJSON *name_item = cJSON_GetObjectItem(item, "name");
    cJSON *call_id_item = cJSON_GetObjectItem(item, "call_id");
    cJSON *args_item = cJSON_GetObjectItem(item, "arguments");
    if (!cJSON_IsString(name_item) || !cJSON_IsString(call_id_item)) {
        return;
    }

    tool_call_ctx_t *ctx = calloc(1, sizeof(tool_call_ctx_t));
    if (!ctx) {
        return;
    }
    ctx->name = strdup(name_item->valuestring);
    ctx->arguments_json =
        strdup(cJSON_IsString(args_item) ? args_item->valuestring : "");
    ctx->call_id = strdup(call_id_item->valuestring);
    xTaskCreate(tool_call_task, "tool_call", 8192, ctx, 5, NULL);
}

// ---------------------------------------------------------------------------
// Realtime event dispatch
// ---------------------------------------------------------------------------

static void handle_audio_delta(cJSON *event) {
    cJSON *item_id_item = cJSON_GetObjectItem(event, "item_id");
    const char *item_id =
        cJSON_IsString(item_id_item) ? item_id_item->valuestring : NULL;

    if (item_id && s_interrupted_item_id &&
        strcmp(item_id, s_interrupted_item_id) == 0) {
        return; // stale audio for an item we already truncated
    }
    if (item_id && (!s_current_item_id || strcmp(item_id, s_current_item_id) != 0)) {
        set_str(&s_current_item_id, item_id);
        cJSON *content_index_item = cJSON_GetObjectItem(event, "content_index");
        s_current_content_index =
            cJSON_IsNumber(content_index_item) ? content_index_item->valueint : 0;
        s_playback_started_at_us = 0;
        s_playback_audio_seconds = 0;
        s_playback_deadline_us = 0;
        set_str(&s_interrupted_item_id, NULL);
    }

    cJSON *delta_item = cJSON_GetObjectItem(event, "delta");
    if (!cJSON_IsString(delta_item)) {
        return;
    }
    const char *b64 = delta_item->valuestring;
    size_t b64_len = strlen(b64);
    size_t max_decoded = b64_len * 3 / 4 + 4;
    unsigned char *pcm_bytes = malloc(max_decoded);
    if (!pcm_bytes) {
        return;
    }
    size_t decoded_len = 0;
    if (mbedtls_base64_decode(pcm_bytes, max_decoded, &decoded_len,
                               (const unsigned char *)b64, b64_len) != 0) {
        free(pcm_bytes);
        return;
    }

    s_assistant_speaking = true;
    int64_t now = esp_timer_get_time();
    if (s_playback_started_at_us == 0) {
        s_playback_started_at_us = now;
    }
    double duration_s = (double)decoded_len / 2.0 / BOARD_AUDIO_SAMPLE_RATE;
    s_playback_audio_seconds += duration_s;
    int64_t duration_us = (int64_t)(duration_s * 1e6);
    s_playback_deadline_us =
        (now > s_playback_deadline_us ? now : s_playback_deadline_us) + duration_us;

    playback_chunk_t chunk = {.pcm = (int16_t *)pcm_bytes, .frames = decoded_len / 2};
    if (xQueueSend(s_playback_queue, &chunk, 0) != pdTRUE) {
        ESP_LOGW(TAG, "Playback queue full; dropping a chunk");
        free(pcm_bytes);
    }
}

static void handle_realtime_event(cJSON *event) {
    cJSON *type_item = cJSON_GetObjectItem(event, "type");
    const char *type = cJSON_IsString(type_item) ? type_item->valuestring : "";

    if (strcmp(type, "session.updated") == 0) {
        ESP_LOGI(TAG, "Ready - listening for your voice.");
    } else if (strcmp(type, "input_audio_buffer.speech_started") == 0) {
        s_user_speaking = true;
        if (s_assistant_speaking) {
            handle_interrupt();
        } else {
            ESP_LOGI(TAG, "Listening...");
        }
    } else if (strcmp(type, "input_audio_buffer.speech_stopped") == 0) {
        s_user_speaking = false;
        ESP_LOGI(TAG, "Thinking...");
    } else if (strcmp(type, "response.output_item.done") == 0) {
        cJSON *item = cJSON_GetObjectItem(event, "item");
        cJSON *item_type = cJSON_GetObjectItem(item, "type");
        if (cJSON_IsString(item_type) && strcmp(item_type->valuestring, "function_call") == 0) {
            dispatch_tool_call(item);
        }
    } else if (strcmp(type, "response.output_audio.delta") == 0) {
        handle_audio_delta(event);
    } else if (strcmp(type, "response.output_audio_transcript.delta") == 0) {
        cJSON *delta = cJSON_GetObjectItem(event, "delta");
        if (cJSON_IsString(delta)) {
            transcript_append(delta->valuestring);
        }
    } else if (strcmp(type, "response.done") == 0) {
        if (s_transcript_len > 0) {
            ESP_LOGI(TAG, "Assistant: %s", s_transcript_buf);
        }
        transcript_reset();
        // Simplification vs. VoiceAssistant: that app delays clearing
        // "assistant speaking" until the last queued chunk's playback
        // deadline, so a very-late barge-in still gets a truncate message.
        // Here we clear immediately — the only cost is that speech starting
        // in the last ~100-300ms tail of a reply won't send a truncate (the
        // tail just finishes playing), which is inaudible in practice.
        s_assistant_speaking = false;
    } else if (strcmp(type, "error") == 0) {
        cJSON *error = cJSON_GetObjectItem(event, "error");
        cJSON *message = cJSON_GetObjectItem(error, "message");
        ESP_LOGW(TAG, "Realtime API error: %s",
                 cJSON_IsString(message) ? message->valuestring : "unknown");
    }
}

// ---------------------------------------------------------------------------
// Incoming-frame reassembly
// ---------------------------------------------------------------------------

// Appends one fragment at `offset` and grows the buffer to always have room
// for a trailing NUL. Returns false (and drops the in-progress message) on
// allocation failure or if the assembled message would exceed RX_MAX_BYTES.
static bool rx_append(const char *data, size_t len, size_t offset) {
    size_t need = offset + len;
    if (need + 1 > RX_MAX_BYTES) {
        ESP_LOGW(TAG, "Incoming message too large (%u bytes); dropping",
                 (unsigned)need);
        s_rx_len = 0;
        return false;
    }
    if (need + 1 > s_rx_cap) {
        size_t new_cap = (need + 1) + (need + 1) / 2;
        uint8_t *grown = realloc(s_rx_buf, new_cap);
        if (!grown) {
            s_rx_len = 0;
            return false;
        }
        s_rx_buf = grown;
        s_rx_cap = new_cap;
    }
    memcpy(s_rx_buf + offset, data, len);
    s_rx_len = need;
    return true;
}

static void ws_event_handler(void *handler_args, esp_event_base_t base,
                              int32_t event_id, void *event_data) {
    (void)handler_args;
    (void)base;
    esp_websocket_event_data_t *data = (esp_websocket_event_data_t *)event_data;

    switch (event_id) {
    case WEBSOCKET_EVENT_CONNECTED:
        ESP_LOGI(TAG, "WebSocket connected; sending session.update");
        send_session_update();
        break;
    case WEBSOCKET_EVENT_DISCONNECTED:
        ESP_LOGW(TAG, "WebSocket disconnected; will auto-reconnect");
        s_assistant_speaking = false;
        s_user_speaking = false;
        set_str(&s_current_item_id, NULL);
        set_str(&s_interrupted_item_id, NULL);
        s_rx_len = 0;
        break;
    case WEBSOCKET_EVENT_DATA:
        if (data->data_len <= 0 || data->payload_len <= 0) {
            break;
        }
        if (!rx_append(data->data_ptr, data->data_len, data->payload_offset)) {
            break;
        }
        if ((int)s_rx_len == data->payload_len) {
            s_rx_buf[s_rx_len] = '\0';
            cJSON *event = cJSON_Parse((const char *)s_rx_buf);
            if (event) {
                handle_realtime_event(event);
                cJSON_Delete(event);
            } else {
                ESP_LOGW(TAG, "Failed to parse incoming Realtime event JSON");
            }
            s_rx_len = 0;
        }
        break;
    case WEBSOCKET_EVENT_ERROR:
        ESP_LOGW(TAG, "WebSocket transport error");
        break;
    default:
        break;
    }
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

esp_err_t realtime_client_start(void) {
    s_send_mutex = xSemaphoreCreateMutex();
    s_playback_queue = xQueueCreate(PLAYBACK_QUEUE_LEN, sizeof(playback_chunk_t));
    if (!s_send_mutex || !s_playback_queue) {
        return ESP_ERR_NO_MEM;
    }

    xTaskCreate(playback_task, "audio_playback", 4096, NULL, 6, NULL);
    xTaskCreate(mic_task, "audio_mic", 4096, NULL, 6, NULL);

    snprintf(s_ws_headers, sizeof(s_ws_headers), "Authorization: Bearer %s\r\n",
             OPENAI_API_KEY);

    esp_websocket_client_config_t config = {
        .uri = "wss://api.openai.com/v1/realtime?model=" REALTIME_MODEL,
        .headers = s_ws_headers,
        .buffer_size = 16384,
        .reconnect_timeout_ms = 2000,
        .network_timeout_ms = 15000,
        .crt_bundle_attach = esp_crt_bundle_attach,
    };
    s_client = esp_websocket_client_init(&config);
    if (!s_client) {
        ESP_LOGE(TAG, "Failed to initialize WebSocket client");
        return ESP_FAIL;
    }
    esp_websocket_register_events(s_client, WEBSOCKET_EVENT_ANY, ws_event_handler, NULL);
    return esp_websocket_client_start(s_client);
}
