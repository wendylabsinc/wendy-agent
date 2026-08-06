#include "esp_log.h"
#include "nvs_flash.h"

#include "board_audio.h"
#include "realtime_client.h"
#include "wifi_connect.h"

static const char *TAG = "main";

void app_main(void) {
    esp_err_t ret = nvs_flash_init();
    if (ret == ESP_ERR_NVS_NO_FREE_PAGES || ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ret = nvs_flash_init();
    }
    ESP_ERROR_CHECK(ret);

    ESP_ERROR_CHECK(board_audio_init());
    ESP_ERROR_CHECK(wifi_connect());
    ESP_ERROR_CHECK(realtime_client_start());

    ESP_LOGI(TAG, "Voice assistant running. Speak naturally.");
}
