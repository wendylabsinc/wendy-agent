#include <stddef.h>
#include <stdbool.h>
#include <stdint.h>

#include "esp_err.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "led_strip.h"
#include "led_strip_rmt.h"
#include "sdkconfig.h"

static const char *TAG = "neopixel";

typedef struct {
    const char *name;
    uint8_t red;
    uint8_t green;
    uint8_t blue;
} color_step_t;

static const color_step_t COLOR_STEPS[] = {
    {"red", CONFIG_NEOPIXEL_BRIGHTNESS, 0, 0},
    {"green", 0, CONFIG_NEOPIXEL_BRIGHTNESS, 0},
    {"blue", 0, 0, CONFIG_NEOPIXEL_BRIGHTNESS},
    {"off", 0, 0, 0},
};

static led_strip_handle_t configure_strip(void)
{
    const led_strip_config_t strip_config = {
        .strip_gpio_num = CONFIG_NEOPIXEL_GPIO,
        .max_leds = CONFIG_NEOPIXEL_COUNT,
        .led_model = LED_MODEL_WS2812,
        .color_component_format = LED_STRIP_COLOR_COMPONENT_FMT_GRB,
        .flags.invert_out = false,
    };

    const led_strip_rmt_config_t rmt_config = {
        .clk_src = RMT_CLK_SRC_DEFAULT,
        .resolution_hz = 10 * 1000 * 1000,
        .mem_block_symbols = 64,
        .flags.with_dma = false,
    };

    led_strip_handle_t strip = NULL;
    ESP_ERROR_CHECK(led_strip_new_rmt_device(&strip_config, &rmt_config, &strip));
    ESP_ERROR_CHECK(led_strip_clear(strip));
    return strip;
}

static void show_color(led_strip_handle_t strip, const color_step_t *color)
{
    for (size_t pixel = 0; pixel < CONFIG_NEOPIXEL_COUNT; ++pixel) {
        ESP_ERROR_CHECK(led_strip_set_pixel(
            strip, pixel, color->red, color->green, color->blue));
    }
    ESP_ERROR_CHECK(led_strip_refresh(strip));
}

void app_main(void)
{
    led_strip_handle_t strip = configure_strip();

    ESP_LOGI(TAG, "Driving %d NeoPixel(s) on GPIO %d",
             CONFIG_NEOPIXEL_COUNT, CONFIG_NEOPIXEL_GPIO);

    while (true) {
        const size_t color_count = sizeof(COLOR_STEPS) / sizeof(COLOR_STEPS[0]);
        for (size_t step = 0; step < color_count; ++step) {
            ESP_LOGI(TAG, "Color: %s", COLOR_STEPS[step].name);
            show_color(strip, &COLOR_STEPS[step]);
            vTaskDelay(pdMS_TO_TICKS(CONFIG_NEOPIXEL_STEP_DELAY_MS));
        }
    }
}
