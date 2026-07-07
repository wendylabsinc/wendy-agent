// ESP32-S31-Korvo-1 demo: branded screen demo (+ optional camera viewfinder).
//
// Phase 1 (this file): drive the 4.3" 800x480 RGB panel with stock ESP-IDF
// drivers. Two modes:
//   * DEMO_ENABLE_CAMERA=0 (default): a branded "WENDY" screen demo -- gradient
//     field, a geometric stroke-drawn wordmark, an animated EQ bar row (the
//     Korvo is an audio board), and a slow scan highlight. Proves the LCD/RGB
//     pipeline builds, links, and is flashable for esp32s31 today. The same
//     scene is rendered pixel-for-pixel by the browser preview shipped with
//     this example so it can be recorded/shared before hardware arrives.
//   * DEMO_ENABLE_CAMERA=1: OV3660 camera -> LCD viewfinder via esp32-camera.
//     !!! Does NOT link on esp32s31 yet: esp32-camera v2.1.7 compiles no driver
//     for this target. See inconveniences.md #8.
//
// Phase 2 (opt-in): DEMO_ENABLE_WENDY_CORE=1 boots the wendy-lite WASM runtime
// alongside the panel (see idf_component.yml / CMakeLists.txt).
//
// NOTE: LCD GPIOs/timings are configured by the official `espressif/esp32_s31_korvo_1` BSP.
// The camera GPIOs below are only used when DEMO_ENABLE_CAMERA=1 and may need revisiting.
// For esp32s31 the recommended camera path is `esp_video` via the BSP (see inconveniences.md).

#include <math.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include "esp_log.h"
#include "esp_check.h"
#include "esp_heap_caps.h"
#include "esp_lcd_panel_ops.h"
#include "bsp/esp32_s31_korvo_1.h" // official BSP: correct LCD bring-up
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

// ----- Display: 4.3" 800x480 RGB panel, brought up via the official BSP -----
#define LCD_H_RES BSP_LCD_H_RES
#define LCD_V_RES BSP_LCD_V_RES

static esp_lcd_panel_handle_t s_panel;

static esp_err_t display_init(void)
{
    // Let the BSP configure the RGB panel (correct pins, timings, PLL, fbs).
    esp_lcd_panel_io_handle_t io = NULL;
    bsp_display_config_t cfg = { 0 };
    ESP_RETURN_ON_ERROR(bsp_display_new(&cfg, &s_panel, &io), TAG, "bsp_display_new");
    // Enable the panel output (drives the DISP GPIO active).
    ESP_RETURN_ON_ERROR(esp_lcd_panel_disp_on_off(s_panel, true), TAG, "disp on");
    ESP_LOGI(TAG, "BSP display up: %dx%d (disp on)", LCD_H_RES, LCD_V_RES);
    return ESP_OK;
}

#if DEMO_ENABLE_CAMERA
// ----- Camera: OV3660 over LCD_CAM DVP -----------------------------------
// Pins from the ESP32-S31-Korvo-1 BSP. NOTE: the BSP itself drives this camera
// via `esp_video` (which supports esp32s31), NOT `esp32-camera` (which does
// not build for s31 — see inconveniences.md #8). These esp32-camera pins are
// kept for reference; the real path is esp_video / the BSP.
#define CAM_PIN_PWDN   -1
#define CAM_PIN_RESET  -1
#define CAM_PIN_XCLK   55
#define CAM_PIN_SIOD   0  // shared I2C bus (BSP_I2C_SDA)
#define CAM_PIN_SIOC   1  // shared I2C bus (BSP_I2C_SCL)
#define CAM_PIN_D7     53
#define CAM_PIN_D6     52
#define CAM_PIN_D5     51
#define CAM_PIN_D4     50
#define CAM_PIN_D3     49
#define CAM_PIN_D2     48
#define CAM_PIN_D1     47
#define CAM_PIN_D0     46
#define CAM_PIN_VSYNC  56
#define CAM_PIN_HREF   57
#define CAM_PIN_PCLK   54

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
#else // !DEMO_ENABLE_CAMERA -- branded "WENDY" screen demo

// ---- tiny RGB565 software renderer ---------------------------------------
static uint16_t *s_fb; // 800x480 RGB565, in PSRAM

static inline uint16_t rgb565(int r, int g, int b)
{
    if (r < 0) r = 0; else if (r > 255) r = 255;
    if (g < 0) g = 0; else if (g > 255) g = 255;
    if (b < 0) b = 0; else if (b > 255) b = 255;
    return (uint16_t)(((r & 0xF8) << 8) | ((g & 0xFC) << 3) | (b >> 3));
}

static inline void put_px(int x, int y, uint16_t c)
{
    if ((unsigned)x < LCD_H_RES && (unsigned)y < LCD_V_RES) {
        s_fb[(size_t)y * LCD_H_RES + x] = c;
    }
}

// Filled square centered at (x,y) with half-size r -- the "pen" for strokes.
static void pen(int x, int y, int r, uint16_t c)
{
    for (int dy = -r; dy <= r; dy++) {
        for (int dx = -r; dx <= r; dx++) {
            put_px(x + dx, y + dy, c);
        }
    }
}

// Thick line via Bresenham; identical geometry to the browser preview.
static void line_thick(int x0, int y0, int x1, int y1, int thick, uint16_t c)
{
    int dx = abs(x1 - x0), sx = x0 < x1 ? 1 : -1;
    int dy = -abs(y1 - y0), sy = y0 < y1 ? 1 : -1;
    int err = dx + dy;
    int r = thick / 2;
    for (;;) {
        pen(x0, y0, r, c);
        if (x0 == x1 && y0 == y1) break;
        int e2 = 2 * err;
        if (e2 >= dy) { err += dy; x0 += sx; }
        if (e2 <= dx) { err += dx; y0 += sy; }
    }
}

// A wordmark glyph = a set of stroke polylines in a normalized [0,1] cell.
typedef struct { int n; const float *xy; } glyph_t; // xy: n*2 floats, one polyline
typedef struct { int strokes; const glyph_t *g; } letter_t;

// Each letter is drawn as one or more polylines (diagonals included).
static const float W_a[] = {0.00f,0.00f, 0.22f,1.00f, 0.50f,0.34f, 0.78f,1.00f, 1.00f,0.00f};
static const float E_a[] = {1.00f,0.00f, 0.00f,0.00f, 0.00f,1.00f, 1.00f,1.00f};
static const float E_b[] = {0.00f,0.50f, 0.80f,0.50f};
static const float N_a[] = {0.00f,1.00f, 0.00f,0.00f, 1.00f,1.00f, 1.00f,0.00f};
static const float D_a[] = {0.00f,1.00f, 0.00f,0.00f, 0.55f,0.00f, 1.00f,0.32f, 1.00f,0.68f, 0.55f,1.00f, 0.00f,1.00f};
static const float Y_a[] = {0.00f,0.00f, 0.50f,0.52f, 1.00f,0.00f};
static const float Y_b[] = {0.50f,0.52f, 0.50f,1.00f};

static const glyph_t W_g[] = {{5, W_a}};
static const glyph_t E_g[] = {{4, E_a}, {2, E_b}};
static const glyph_t N_g[] = {{4, N_a}};
static const glyph_t D_g[] = {{7, D_a}};
static const glyph_t Y_g[] = {{3, Y_a}, {2, Y_b}};
static const letter_t WENDY[] = {
    {1, W_g}, {2, E_g}, {1, N_g}, {1, D_g}, {2, Y_g},
};

static void draw_letter(const letter_t *L, int ox, int oy, int w, int h, int thick, uint16_t c)
{
    for (int s = 0; s < L->strokes; s++) {
        const glyph_t *g = &L->g[s];
        for (int i = 0; i + 1 < g->n; i++) {
            int x0 = ox + (int)(g->xy[i * 2 + 0] * w);
            int y0 = oy + (int)(g->xy[i * 2 + 1] * h);
            int x1 = ox + (int)(g->xy[i * 2 + 2] * w);
            int y1 = oy + (int)(g->xy[i * 2 + 3] * h);
            line_thick(x0, y0, x1, y1, thick, c);
        }
    }
}

static void render_frame(int t)
{
    // Background: vertical gradient, deep indigo -> near black.
    for (int y = 0; y < LCD_V_RES; y++) {
        float k = (float)y / (LCD_V_RES - 1);
        int r = (int)(11 + (5 - 11) * k);
        int g = (int)(14 + (6 - 14) * k);
        int b = (int)(23 + (11 - 23) * k);
        uint16_t col = rgb565(r, g, b);
        uint16_t *row = s_fb + (size_t)y * LCD_H_RES;
        for (int x = 0; x < LCD_H_RES; x++) row[x] = col;
    }

    // Slow scan highlight sweeping down the panel.
    int scan = (int)((0.5f + 0.5f * sinf(t * 0.03f)) * (LCD_V_RES - 1));
    for (int dy = -18; dy <= 18; dy++) {
        int y = scan + dy;
        if ((unsigned)y >= LCD_V_RES) continue;
        int a = 22 - abs(dy);
        if (a < 0) a = 0;
        uint16_t *row = s_fb + (size_t)y * LCD_H_RES;
        for (int x = 0; x < LCD_H_RES; x++) {
            int r = (row[x] >> 11) & 0x1F, g = (row[x] >> 5) & 0x3F, b = row[x] & 0x1F;
            row[x] = (uint16_t)(((r + a) > 31 ? 31 : r + a) << 11) |
                     (((g + a) > 63 ? 63 : g + a) << 5) | ((b + a) > 31 ? 31 : b + a);
        }
    }

    // Wordmark "WENDY", centered, shimmering amber<->magenta per letter.
    const int cellW = 96, cellH = 150, gap = 34, thick = 14;
    const int total = 5 * cellW + 4 * gap;
    int ox = (LCD_H_RES - total) / 2;
    int oy = 120;
    for (int i = 0; i < 5; i++) {
        float s = 0.5f + 0.5f * sinf(t * 0.05f + i * 0.7f);
        uint16_t c = rgb565((int)(255),
                            (int)(92 + (194 - 92) * s),
                            (int)(138 + (75 - 138) * s));
        draw_letter(&WENDY[i], ox + i * (cellW + gap), oy, cellW, cellH, thick, c);
    }

    // Animated EQ bars along the bottom -- Korvo is an audio board.
    const int bars = 24, bw = 20, bgap = 12;
    const int btotal = bars * bw + (bars - 1) * bgap;
    int bx = (LCD_H_RES - btotal) / 2;
    int baseY = 430, maxH = 120;
    for (int i = 0; i < bars; i++) {
        float m = 0.5f + 0.5f * sinf(t * 0.11f + i * 0.55f);
        m *= 0.35f + 0.65f * (0.5f + 0.5f * sinf(t * 0.017f + i));
        int h = (int)(12 + m * maxH);
        float s = (float)i / (bars - 1);
        uint16_t c = rgb565((int)(255), (int)(194 + (92 - 194) * s), (int)(75 + (138 - 75) * s));
        for (int y = baseY - h; y < baseY; y++) {
            uint16_t *row = s_fb + (size_t)y * LCD_H_RES;
            for (int x = bx + i * (bw + bgap); x < bx + i * (bw + bgap) + bw; x++) {
                if ((unsigned)x < LCD_H_RES) row[x] = c;
            }
        }
    }
}

static void screen_demo_task(void *arg)
{
    const size_t px = (size_t)LCD_H_RES * LCD_V_RES;
    s_fb = heap_caps_malloc(px * sizeof(uint16_t), MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    ESP_LOGI(TAG, "screen_demo_task: s_fb=%p (%u bytes), panel=%p", s_fb, (unsigned)(px * 2), s_panel);
    if (!s_fb) {
        ESP_LOGE(TAG, "no PSRAM for the %ux%u frame buffer", (unsigned)LCD_H_RES, (unsigned)LCD_V_RES);
        vTaskDelete(NULL);
        return;
    }
    for (int t = 0;; t++) {
        render_frame(t);
        esp_err_t dr = esp_lcd_panel_draw_bitmap(s_panel, 0, 0, LCD_H_RES, LCD_V_RES, s_fb);
        if (t % 30 == 0) { // ~once per second
            ESP_LOGI(TAG, "frame %d drawn, draw_bitmap=%s", t, esp_err_to_name(dr));
        }
        vTaskDelay(pdMS_TO_TICKS(33)); // ~30 fps
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
    xTaskCreatePinnedToCore(viewfinder_task, "viewfinder", 4096, NULL, 5, NULL, 1);
    ESP_LOGI(TAG, "camera viewfinder running");
#else
    xTaskCreatePinnedToCore(screen_demo_task, "screen_demo", 8192, NULL, 5, NULL, 1);
    ESP_LOGI(TAG, "WENDY screen demo running");
#endif
}
