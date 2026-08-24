#pragma once

#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// Pin map verified against Waveshare's own ESP32-S3-AUDIO-Board demo firmware
// (bsp_board.h in ESP32-S3-AUDIO-Board-Demo.zip) — the product docs page does
// not list GPIOs, so this is the authoritative source.
#define BOARD_AUDIO_I2C_SDA GPIO_NUM_11
#define BOARD_AUDIO_I2C_SCL GPIO_NUM_10
#define BOARD_AUDIO_I2S_MCLK GPIO_NUM_12
#define BOARD_AUDIO_I2S_BCLK GPIO_NUM_13
#define BOARD_AUDIO_I2S_WS GPIO_NUM_14
#define BOARD_AUDIO_I2S_DIN GPIO_NUM_15  // from ES7210 mic ADC
#define BOARD_AUDIO_I2S_DOUT GPIO_NUM_16 // to ES8311 speaker DAC

#define BOARD_AUDIO_SAMPLE_RATE 16000

// Brings up the I2C control bus, the shared full-duplex I2S bus, and the
// ES7210 (mic-in) / ES8311 (speaker-out) codecs as mono 16-bit PCM at
// BOARD_AUDIO_SAMPLE_RATE. Fatal on failure — audio is this app's whole job.
esp_err_t board_audio_init(void);

// Blocking read of `frames` mono PCM16 samples from the mic. Returns the
// number of samples actually read in *frames_read (may be less on error).
esp_err_t board_audio_read(int16_t *buffer, size_t frames, size_t *frames_read);

// Blocking write of `frames` mono PCM16 samples to the speaker.
esp_err_t board_audio_write(const int16_t *buffer, size_t frames);

// Speaker volume as a 0-100 percentage (ES8311's own volume register).
esp_err_t board_audio_set_volume(int percent);
esp_err_t board_audio_get_volume(int *percent);

#ifdef __cplusplus
}
#endif
