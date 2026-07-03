// ESP32-S31-Korvo-1 demo: live camera -> LCD viewfinder.
//
// Phase 1 (this file): drive the 4.3" 800x480 RGB panel with stock ESP-IDF
// drivers. Two modes:
//   * DEMO_ENABLE_CAMERA=0 (default): animated color-bar TEST PATTERN. Proves
//     the LCD/RGB pipeline builds, links, and is flashable for esp32s31 today.
//   * DEMO_ENABLE_CAMERA=1: OV3660 camera -> LCD viewfinder via esp32-camera.
//     !!! This does NOT link on esp32s31 yet: esp32-camera v2.1.7 only compiles
//     its driver for esp32/esp32s2/esp32s3 (empty archive otherwise). See
//     inconveniences.md #8. Kept here as the intended implementation.
//
// Phase 2 (opt-in): DEMO_ENABLE_WENDY_CORE=1 boots the wendy-lite WASM runtime
// alongside the panel (see idf_component.yml / CMakeLists.txt).
//
// !!! PIN MAP IS UNVERIFIED !!!
// The ESP32-S31-Korvo-1 schematic was not public at authoring time. The GPIOs
// below are PLACEHOLDERS from the ESP32-S3-LCD-EV-Board / Korvo-2 conventions
// (same LCD_CAM-class imaging path) and will need correcting against the real
// board schematic before anything appears on screen. Every such pin is marked
// TODO(schematic). See inconveniences.md.

#include <string.h>
#include "esp_log.h"
#include "esp_check.h"
#include "esp_heap_caps.h"
#include "esp_lcd_panel_ops.h"
#include "esp_lcd_panel_rgb.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#ifndef DEMO_ENABLE_CAMERA
#define DEMO_ENABLE_CAMERA 0
#endif

#if DEMO_ENABLE_CAMERA
#include "esp_camera.h"
#endif

#if defined(DEMO_ENABLE_WENDY_CORE) && DEMO_ENABLE_WENDY_CORE
#include "wendy_core.h"
#endif

static const char *TAG = "korvo-demo";

// ----- Display: 4.3" 800x480 RGB panel -----------------------------------
#define LCD_H_RES 800
#define LCD_V_RES 480
#define LCD_PIXEL_CLOCK_HZ (16 * 1000 * 1000)
#define LCD_DATA_WIDTH 16 // RGB565

// TODO(schematic): replace every pin below with the real S31-Korvo-1 mapping.
#define LCD_PIN_PCLK   21
#define LCD_PIN_VSYNC  22
#define LCD_PIN_HSYNC  23
#define LCD_PIN_DE     24
#define LCD_PIN_DISP   -1 // not connected on most EV boards
static const int LCD_DATA_PINS[LCD_DATA_WIDTH] = {
    // B0..B4, G0..G5, R0..R4  (RGB565 bit order per esp_lcd RGB convention)
    4, 5, 6, 7, 8,         // blue
    9, 10, 11, 12, 13, 14, // green
    15, 16, 17, 18, 19,    // red
};

static esp_lcd_panel_handle_t s_panel;

static esp_err_t display_init(void)
{
    esp_lcd_rgb_panel_config_t cfg = {
        .clk_src = LCD_CLK_SRC_DEFAULT,
        .data_width = LCD_DATA_WIDTH,
        // IDF v6.2 replaced bits_per_pixel with explicit in/out color formats
        // and psram_trans_align with dma_burst_size. (See inconveniences.md #7.)
        .in_color_format = LCD_COLOR_FMT_RGB565,
        .out_color_format = LCD_COLOR_FMT_RGB565,
        .num_fbs = 1,
        .dma_burst_size = 64,
        .disp_gpio_num = LCD_PIN_DISP,
        .pclk_gpio_num = LCD_PIN_PCLK,
        .vsync_gpio_num = LCD_PIN_VSYNC,
        .hsync_gpio_num = LCD_PIN_HSYNC,
        .de_gpio_num = LCD_PIN_DE,
        .flags.fb_in_psram = true,
        .timings = {
            .pclk_hz = LCD_PIXEL_CLOCK_HZ,
            .h_res = LCD_H_RES,
            .v_res = LCD_V_RES,
            // Generic 800x480 4.3" RGB timings. TODO(panel): confirm porches
            // against the exact panel fitted to the Korvo-1.
            .hsync_back_porch = 40,
            .hsync_front_porch = 20,
            .hsync_pulse_width = 48,
            .vsync_back_porch = 13,
            .vsync_front_porch = 1,
            .vsync_pulse_width = 31,
            .flags.pclk_active_neg = true,
        },
    };
    for (int i = 0; i < LCD_DATA_WIDTH; i++) {
        cfg.data_gpio_nums[i] = LCD_DATA_PINS[i];
    }

    ESP_RETURN_ON_ERROR(esp_lcd_new_rgb_panel(&cfg, &s_panel), TAG, "new rgb panel");
    ESP_RETURN_ON_ERROR(esp_lcd_panel_reset(s_panel), TAG, "reset");
    ESP_RETURN_ON_ERROR(esp_lcd_panel_init(s_panel), TAG, "init");
    ESP_LOGI(TAG, "RGB panel up: %dx%d", LCD_H_RES, LCD_V_RES);
    return ESP_OK;
}

#if DEMO_ENABLE_CAMERA
// ----- Camera: OV3660 over LCD_CAM DVP -----------------------------------
// TODO(schematic): replace every pin below with the real S31-Korvo-1 mapping.
#define CAM_PIN_PWDN   -1
#define CAM_PIN_RESET  -1
#define CAM_PIN_XCLK   40
#define CAM_PIN_SIOD   41 // SCCB/I2C data
#define CAM_PIN_SIOC   42 // SCCB/I2C clock
#define CAM_PIN_D7     39
#define CAM_PIN_D6     38
#define CAM_PIN_D5     37
#define CAM_PIN_D4     36
#define CAM_PIN_D3     35
#define CAM_PIN_D2     34
#define CAM_PIN_D1     33
#define CAM_PIN_D0     32
#define CAM_PIN_VSYNC  43
#define CAM_PIN_HREF   44
#define CAM_PIN_PCLK   45

static esp_err_t camera_init(void)
{
    camera_config_t cfg = {
        .pin_pwdn = CAM_PIN_PWDN,
        .pin_reset = CAM_PIN_RESET,
        .pin_xclk = CAM_PIN_XCLK,
        .pin_sccb_sda = CAM_PIN_SIOD,
        .pin_sccb_scl = CAM_PIN_SIOC,
        .pin_d7 = CAM_PIN_D7, .pin_d6 = CAM_PIN_D6,
        .pin_d5 = CAM_PIN_D5, .pin_d4 = CAM_PIN_D4,
        .pin_d3 = CAM_PIN_D3, .pin_d2 = CAM_PIN_D2,
        .pin_d1 = CAM_PIN_D1, .pin_d0 = CAM_PIN_D0,
        .pin_vsync = CAM_PIN_VSYNC,
        .pin_href = CAM_PIN_HREF,
        .pin_pclk = CAM_PIN_PCLK,
        .xclk_freq_hz = 20 * 1000 * 1000,
        .ledc_timer = LEDC_TIMER_0,
        .ledc_channel = LEDC_CHANNEL_0,
        .pixel_format = PIXFORMAT_RGB565,
        .frame_size = FRAMESIZE_VGA,
        .fb_count = 2,
        .fb_location = CAMERA_FB_IN_PSRAM,
        .grab_mode = CAMERA_GRAB_LATEST,
    };
    ESP_RETURN_ON_ERROR(esp_camera_init(&cfg), TAG, "camera init");
    ESP_LOGI(TAG, "camera up: OV3660 expected, RGB565 VGA");
    return ESP_OK;
}

static void viewfinder_task(void *arg)
{
    const int x0 = (LCD_H_RES - 640) / 2; // center VGA on the panel
    for (;;) {
        camera_fb_t *fb = esp_camera_fb_get();
        if (!fb) {
            ESP_LOGW(TAG, "frame grab failed");
            vTaskDelay(pdMS_TO_TICKS(10));
            continue;
        }
        esp_lcd_panel_draw_bitmap(s_panel, x0, 0, x0 + fb->width, fb->height, fb->buf);
        esp_camera_fb_return(fb);
    }
}
#else // !DEMO_ENABLE_CAMERA -- animated test pattern so the screen path is provable today

static inline uint16_t rgb565(uint8_t r, uint8_t g, uint8_t b)
{
    return (uint16_t)(((r & 0xF8) << 8) | ((g & 0xFC) << 3) | (b >> 3));
}

static void viewfinder_task(void *arg)
{
    const size_t px = (size_t)LCD_H_RES * LCD_V_RES;
    uint16_t *frame = heap_caps_malloc(px * sizeof(uint16_t), MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    if (!frame) {
        ESP_LOGE(TAG, "no PSRAM for test-pattern frame buffer");
        vTaskDelete(NULL);
        return;
    }
    // 8 vertical SMPTE-ish color bars that scroll horizontally over time.
    const uint16_t bar[8] = {
        rgb565(255, 255, 255), rgb565(255, 255, 0),
        rgb565(0, 255, 255),   rgb565(0, 255, 0),
        rgb565(255, 0, 255),   rgb565(255, 0, 0),
        rgb565(0, 0, 255),     rgb565(0, 0, 0),
    };

    int shift = 0;
    for (;;) {
        for (int y = 0; y < LCD_V_RES; y++) {
            uint16_t *row = frame + (size_t)y * LCD_H_RES;
            for (int x = 0; x < LCD_H_RES; x++) {
                int b = ((x + shift) / (LCD_H_RES / 8)) & 7;
                row[x] = bar[b];
            }
        }
        esp_lcd_panel_draw_bitmap(s_panel, 0, 0, LCD_H_RES, LCD_V_RES, frame);
        shift = (shift + 8) % LCD_H_RES;
        vTaskDelay(pdMS_TO_TICKS(33));
    }
}
#endif // DEMO_ENABLE_CAMERA

void app_main(void)
{
#if defined(DEMO_ENABLE_WENDY_CORE) && DEMO_ENABLE_WENDY_CORE
    // Phase 2: boot the wendy-lite WASM runtime alongside the panel.
    // NOTE(unverified): coexistence/ordering with the LCD pipeline on esp32s31
    // has not been validated on hardware.
    ESP_ERROR_CHECK(wendy_core_init());
#endif

    ESP_ERROR_CHECK(display_init());
#if DEMO_ENABLE_CAMERA
    ESP_ERROR_CHECK(camera_init());
#endif
    xTaskCreatePinnedToCore(viewfinder_task, "viewfinder", 4096, NULL, 5, NULL, 1);
    ESP_LOGI(TAG, "%s running", DEMO_ENABLE_CAMERA ? "camera viewfinder" : "test pattern");
}
