// I2C/I2S/codec bring-up for the ESP32-S3-AUDIO-Board's ES7210 (mic) and
// ES8311 (speaker) codecs. Ported from Waveshare's own demo firmware
// (ESP32-S3-AUDIO-Board-Demo.zip, main/hardeware_driver/bsp_board.c) for the
// verified pin map, then simplified to a single mono 16 kHz PCM16 stream in
// each direction (the demo drives a raw 4-mic array for on-chip wake-word
// detection, which this app doesn't need) and rewritten against the current
// esp_codec_dev 1.6.x API, which reshaped the ES7210/ES8311 config structs
// since the demo (mic selection moved from a `mic_selected` codec field to
// `esp_codec_dev_sample_info_t.channel_mask`; PA/clock config moved into
// shared `audio_hw_*_cfg_t` sub-structs).

#include "board_audio.h"

#include "driver/gpio.h"
#include "driver/i2c_master.h"
#include "driver/i2s_std.h"
#include "esp_codec_dev.h"
#include "esp_codec_dev_defaults.h"
#include "esp_log.h"

static const char *TAG = "board_audio";

static i2c_master_bus_handle_t s_i2c_bus;
static i2s_chan_handle_t s_i2s_tx;
static i2s_chan_handle_t s_i2s_rx;
static esp_codec_dev_handle_t s_mic_dev;
static esp_codec_dev_handle_t s_speaker_dev;

static esp_err_t init_i2c(void) {
    i2c_master_bus_config_t bus_cfg = {
        .i2c_port = I2C_NUM_0,
        .sda_io_num = BOARD_AUDIO_I2C_SDA,
        .scl_io_num = BOARD_AUDIO_I2C_SCL,
        .clk_source = I2C_CLK_SRC_DEFAULT,
        .glitch_ignore_cnt = 7,
        .flags.enable_internal_pullup = true,
    };
    return i2c_new_master_bus(&bus_cfg, &s_i2c_bus);
}

static esp_err_t init_i2s(void) {
    i2s_chan_config_t chan_cfg =
        I2S_CHANNEL_DEFAULT_CONFIG(I2S_NUM_1, I2S_ROLE_MASTER);
    esp_err_t err = i2s_new_channel(&chan_cfg, &s_i2s_tx, &s_i2s_rx);
    if (err != ESP_OK) {
        return err;
    }

    // Physical bus: 32-bit stereo Philips slots at the board's native
    // 16 kHz — this is the verified working configuration from Waveshare's
    // demo. esp_codec_dev handles the conversion down to the mono 16-bit
    // application format we request per-codec below.
    i2s_std_config_t std_cfg = {
        .clk_cfg = I2S_STD_CLK_DEFAULT_CONFIG(BOARD_AUDIO_SAMPLE_RATE),
        .slot_cfg =
            I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(I2S_DATA_BIT_WIDTH_32BIT,
                                                 I2S_SLOT_MODE_STEREO),
        .gpio_cfg =
            {
                .mclk = BOARD_AUDIO_I2S_MCLK,
                .bclk = BOARD_AUDIO_I2S_BCLK,
                .ws = BOARD_AUDIO_I2S_WS,
                .dout = BOARD_AUDIO_I2S_DOUT,
                .din = BOARD_AUDIO_I2S_DIN,
            },
    };

    err = i2s_channel_init_std_mode(s_i2s_tx, &std_cfg);
    if (err != ESP_OK) {
        return err;
    }
    err = i2s_channel_init_std_mode(s_i2s_rx, &std_cfg);
    if (err != ESP_OK) {
        return err;
    }
    err = i2s_channel_enable(s_i2s_tx);
    if (err != ESP_OK) {
        return err;
    }
    return i2s_channel_enable(s_i2s_rx);
}

static esp_err_t init_mic(void) {
    audio_codec_i2s_cfg_t i2s_cfg = {
        .port = I2S_NUM_1,
        .rx_handle = s_i2s_rx,
        .tx_handle = NULL,
    };
    const audio_codec_data_if_t *data_if = audio_codec_new_i2s_data(&i2s_cfg);
    if (!data_if) {
        ESP_LOGE(TAG, "audio_codec_new_i2s_data (mic) failed");
        return ESP_FAIL;
    }

    audio_codec_i2c_cfg_t i2c_cfg = {
        .port = I2C_NUM_0,
        .addr = ES7210_CODEC_DEFAULT_ADDR,
        .bus_handle = s_i2c_bus,
    };
    const audio_codec_ctrl_if_t *ctrl_if = audio_codec_new_i2c_ctrl(&i2c_cfg);
    if (!ctrl_if) {
        ESP_LOGE(TAG, "audio_codec_new_i2c_ctrl (ES7210) failed");
        return ESP_FAIL;
    }

    es7210_codec_cfg_t es7210_cfg = {
        .ctrl_if = ctrl_if,
        .master_mode = false, // ESP32 I2S is the bus master
        .mic_selected = ES7210_SEL_MIC1, // single mono mic; no beamforming needed
        .mclk_src = ES7210_MCLK_FROM_PAD, // ES7210 uses the shared external MCLK
    };
    const audio_codec_if_t *codec_if = es7210_codec_new(&es7210_cfg);
    if (!codec_if) {
        ESP_LOGE(TAG, "es7210_codec_new failed");
        return ESP_FAIL;
    }

    esp_codec_dev_cfg_t dev_cfg = {
        .dev_type = ESP_CODEC_DEV_TYPE_IN,
        .codec_if = codec_if,
        .data_if = data_if,
    };
    s_mic_dev = esp_codec_dev_new(&dev_cfg);
    if (!s_mic_dev) {
        ESP_LOGE(TAG, "esp_codec_dev_new (mic) failed");
        return ESP_FAIL;
    }

    esp_codec_dev_sample_info_t fs = {
        .bits_per_sample = 16,
        .channel = 1,
        .sample_rate = BOARD_AUDIO_SAMPLE_RATE,
    };
    int ret = esp_codec_dev_open(s_mic_dev, &fs);
    if (ret != ESP_CODEC_DEV_OK) {
        ESP_LOGE(TAG, "esp_codec_dev_open (mic) failed: %d", ret);
        return ESP_FAIL;
    }
    return ESP_OK;
}

static esp_err_t init_speaker(void) {
    audio_codec_i2s_cfg_t i2s_cfg = {
        .port = I2S_NUM_1,
        .rx_handle = NULL,
        .tx_handle = s_i2s_tx,
    };
    const audio_codec_data_if_t *data_if = audio_codec_new_i2s_data(&i2s_cfg);
    if (!data_if) {
        ESP_LOGE(TAG, "audio_codec_new_i2s_data (speaker) failed");
        return ESP_FAIL;
    }

    audio_codec_i2c_cfg_t i2c_cfg = {
        .port = I2C_NUM_0,
        .addr = ES8311_CODEC_DEFAULT_ADDR,
        .bus_handle = s_i2c_bus,
    };
    const audio_codec_ctrl_if_t *ctrl_if = audio_codec_new_i2c_ctrl(&i2c_cfg);
    if (!ctrl_if) {
        ESP_LOGE(TAG, "audio_codec_new_i2c_ctrl (ES8311) failed");
        return ESP_FAIL;
    }

    const audio_codec_gpio_if_t *gpio_if = audio_codec_new_gpio();
    if (!gpio_if) {
        ESP_LOGE(TAG, "audio_codec_new_gpio failed");
        return ESP_FAIL;
    }

    es8311_codec_cfg_t es8311_cfg = {
        .ctrl_if = ctrl_if,
        .gpio_if = gpio_if,
        .codec_mode = ESP_CODEC_DEV_WORK_MODE_DAC, // speaker output only
        .pa_pin = -1, // no ESP32 GPIO gates the amp; ES8311 drives its own
                      // PA-enable pin internally over I2C
        .master_mode = false,
        // Waveshare's verified demo runs ES8311 with use_mclk=false (clock
        // derived from BCLK) even though MCLK is physically wired to it.
        .use_mclk = false,
    };
    const audio_codec_if_t *codec_if = es8311_codec_new(&es8311_cfg);
    if (!codec_if) {
        ESP_LOGE(TAG, "es8311_codec_new failed");
        return ESP_FAIL;
    }

    esp_codec_dev_cfg_t dev_cfg = {
        .dev_type = ESP_CODEC_DEV_TYPE_OUT,
        .codec_if = codec_if,
        .data_if = data_if,
    };
    s_speaker_dev = esp_codec_dev_new(&dev_cfg);
    if (!s_speaker_dev) {
        ESP_LOGE(TAG, "esp_codec_dev_new (speaker) failed");
        return ESP_FAIL;
    }

    esp_codec_dev_sample_info_t fs = {
        .bits_per_sample = 16,
        .channel = 1,
        .sample_rate = BOARD_AUDIO_SAMPLE_RATE,
    };
    int ret = esp_codec_dev_open(s_speaker_dev, &fs);
    if (ret != ESP_CODEC_DEV_OK) {
        ESP_LOGE(TAG, "esp_codec_dev_open (speaker) failed: %d", ret);
        return ESP_FAIL;
    }
    return ESP_OK;
}

esp_err_t board_audio_init(void) {
    esp_err_t err = init_i2c();
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "I2C init failed: %s", esp_err_to_name(err));
        return err;
    }
    err = init_i2s();
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "I2S init failed: %s", esp_err_to_name(err));
        return err;
    }
    err = init_mic();
    if (err != ESP_OK) {
        return err;
    }
    err = init_speaker();
    if (err != ESP_OK) {
        return err;
    }
    ESP_LOGI(TAG, "Audio ready: mono PCM16 @ %d Hz", BOARD_AUDIO_SAMPLE_RATE);
    return ESP_OK;
}

esp_err_t board_audio_read(int16_t *buffer, size_t frames, size_t *frames_read) {
    int ret = esp_codec_dev_read(s_mic_dev, buffer, (int)(frames * sizeof(int16_t)));
    if (ret != ESP_CODEC_DEV_OK) {
        *frames_read = 0;
        return ESP_FAIL;
    }
    *frames_read = frames;
    return ESP_OK;
}

esp_err_t board_audio_write(const int16_t *buffer, size_t frames) {
    int ret = esp_codec_dev_write(s_speaker_dev, (void *)buffer,
                                   (int)(frames * sizeof(int16_t)));
    return ret == ESP_CODEC_DEV_OK ? ESP_OK : ESP_FAIL;
}

esp_err_t board_audio_set_volume(int percent) {
    if (percent < 0 || percent > 100) {
        return ESP_ERR_INVALID_ARG;
    }
    int ret = esp_codec_dev_set_out_vol(s_speaker_dev, percent);
    return ret == ESP_CODEC_DEV_OK ? ESP_OK : ESP_FAIL;
}

esp_err_t board_audio_get_volume(int *percent) {
    int ret = esp_codec_dev_get_out_vol(s_speaker_dev, percent);
    return ret == ESP_CODEC_DEV_OK ? ESP_OK : ESP_FAIL;
}
