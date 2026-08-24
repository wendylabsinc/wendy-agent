#pragma once

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// Blocking WiFi station connect using WIFI_SSID/WIFI_PASSWORD from
// secrets.h. Retries with backoff; returns ESP_OK once an IP address is
// obtained, or ESP_FAIL after repeated failures.
esp_err_t wifi_connect(void);

#ifdef __cplusplus
}
#endif
