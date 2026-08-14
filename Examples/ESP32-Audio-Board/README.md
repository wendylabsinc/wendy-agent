# ESP32-S3-AUDIO-Board — realtime voice assistant

A native ESP-IDF firmware for Waveshare's [ESP32-S3-AUDIO-Board](https://docs.waveshare.com/ESP32-S3-AUDIO-Board)
that streams microphone audio to OpenAI's Realtime API and plays the spoken
response through the on-board speaker — the same speech-to-speech experience
as the [`VoiceAssistant`](../VoiceAssistant) WendyOS example, running
standalone on the ESP32 instead of on a Linux device. It uses the GA Realtime
WebSocket interface, `gpt-realtime-2.1`, semantic voice activity detection,
and 16 kHz mono PCM16 audio in both directions.

Interruption/barge-in is enabled: speak over the assistant and it stops and
truncates the unheard part of its reply. **This board has no dedicated echo-
cancellation chip**, so with barge-in enabled the speaker's own output can
leak into the mic and occasionally trigger a false interruption — there's no
way around that on this hardware without adding an external AEC front end.
The assistant can also change its own speaker volume, look up the weather
anywhere via the free Open-Meteo API, and answer questions that need current
information (news, scores, prices) via OpenAI web search.

## Hardware

- Waveshare ESP32-S3-AUDIO-Board (ESP32-S3R8: octal 8 MB PSRAM, 16 MB flash)
- On-board ES7210 digital mic array + ES8311 codec + speaker amp — no extra
  wiring needed
- A speaker connected to the board's speaker header
- WiFi with internet access

The I2C/I2S pin map used here (`main/board_audio.h`) comes from Waveshare's
own demo firmware, not the product docs page (which doesn't list GPIOs) —
see `main/board_audio.c` for details and the reasoning behind each codec
config field.

## 1. Install ESP-IDF

This targets `esp32s3` on ESP-IDF 5.1+ (developed and verified against
5.5.1, matching Waveshare's own demo). Any full (non-preview) ESP-IDF
install works:

```sh
. /path/to/esp-idf/export.sh
```

## 2. Supply WiFi and OpenAI credentials

```sh
cp main/secrets.h.example main/secrets.h
```

Edit `main/secrets.h` with your WiFi SSID/password and an OpenAI API key
from https://platform.openai.com/api-keys. `secrets.h` is gitignored — it
never gets committed or flashed anywhere but this one device.

## 3. Build and flash

```sh
idf.py set-target esp32s3
idf.py build
idf.py -p /dev/cu.usbserial-XXXX flash monitor
```

When the log says `Ready - listening for your voice.`, speak normally.
Semantic VAD ends the turn automatically and the reply plays through the
speaker. Press `Ctrl-]` to exit the monitor.

## Volume and other tools

Just ask it: "set the volume to 30 percent", "turn it up", "how loud are
you?", "what's the weather in Tokyo?", "what's in the news today?". These
are Realtime function tools — volume goes straight to the ES8311's own
volume register (no network involved), weather calls the free, keyless
[Open-Meteo](https://open-meteo.com) API, and anything else that needs live
information calls OpenAI's Responses API (`gpt-5-mini` by default) with its
built-in web-search tool enabled, using the same `OPENAI_API_KEY`. Each web
search bills one Responses API call on top of the Realtime session.

## Configuration

There's no environment-variable injection on a flashed device, so
model/voice/instructions live as `#define`s at the top of
`main/realtime_client.c`:

| Constant | Default | Purpose |
| --- | --- | --- |
| `REALTIME_MODEL` | `gpt-realtime-2.1` | Realtime model |
| `ASSISTANT_VOICE` | `marin` | Assistant voice |
| `ASSISTANT_INSTRUCTIONS` | concise voice assistant | System instructions |
| `BOARD_AUDIO_SAMPLE_RATE` (`board_audio.h`) | `16000` | PCM sample rate in Hz for both directions |
| `WEB_SEARCH_MODEL` (`tools.c`) | `gpt-5-mini` | Responses API model used for web searches |

## How it works

`board_audio_read()` pulls 100 ms mono PCM16 chunks straight off the ES7210
mic (`main/board_audio.c`, built on `esp_codec_dev`). A dedicated FreeRTOS
task base64-encodes each chunk into `input_audio_buffer.append` events over
one authenticated WebSocket (`esp_websocket_client`, TLS via ESP-IDF's cert
bundle). OpenAI's semantic VAD creates each response; returned
`response.output_audio.delta` chunks are base64-decoded and queued to a
playback task that writes them straight to the ES8311 speaker — no temporary
files, no resampling (mic, speaker, and the Realtime session all run at the
board's native 16 kHz).

Barge-in works by tracking how much of the current reply has actually played
(via `esp_timer_get_time()`) so that when `input_audio_buffer.speech_started`
arrives mid-reply, the firmware can drop the queued-but-unplayed audio and
send `conversation.item.truncate` with an accurate `audio_end_ms`, mirroring
`VoiceAssistant`'s approach. One simplification versus that app: playback is
considered "done" immediately at `response.done` rather than delayed until
the last queued chunk's exact deadline — the only user-visible effect is
that speech starting in the last ~100-300 ms tail of a reply won't trigger an
explicit truncate (the tail just finishes playing, which is inaudible).

Tool calling drives everything beyond conversation (`main/tools.c`): the
session registers `set_volume`, `adjust_volume`, `get_volume`, `get_weather`,
and `web_search` function tools. Each `function_call` item is handled on its
own short-lived FreeRTOS task so a slow HTTP request (weather/search) never
blocks the WebSocket receive loop or barge-in detection. The result goes back
as `function_call_output` followed by `response.create` — unless the user is
already talking again.

This follows OpenAI's current [Realtime WebSocket
guide](https://developers.openai.com/api/docs/guides/realtime-websocket) and
[Realtime conversation event
flow](https://developers.openai.com/api/docs/guides/realtime-conversations).

The API key is baked into the flashed firmware, which is appropriate here
because the device is a trusted, single-purpose client you control — not a
browser or mobile app. Don't reuse this pattern in code that ships to
end-user devices you don't control.

## Local checks

A few pure-C helpers (WMO weather-code lookup, citation stripping) have no
ESP-IDF dependency and can be tested off-target:

```sh
cd test
./run_tests.sh
```

Everything else — audio, WiFi, the actual Realtime session, barge-in — is
hardware-in-the-loop and hasn't been verified against real silicon yet.

## Files

| File | Purpose |
| --- | --- |
| `main/board_audio.c/.h` | I2C/I2S/ES7210/ES8311 bring-up, mono 16 kHz read/write, volume |
| `main/wifi_connect.c/.h` | Blocking WiFi station connect |
| `main/realtime_client.c/.h` | WebSocket session, mic/playback tasks, barge-in, tool-call dispatch |
| `main/tools.c/.h` | Function-tool specs + execution (volume, weather, web search) |
| `main/weather_codes.c/.h` | WMO weather-code → spoken text (host-testable) |
| `main/text_utils.c/.h` | Web-search citation stripping (host-testable) |
| `main/secrets.h.example` | Copy to `secrets.h` (gitignored) and fill in credentials |
| `test/` | Host-buildable unit tests for the pure-C helpers |
