# Wendy Lite WASM App API Reference

All host functions are imported from the `"wendy"` module. Include `wendy.h` for C apps.

## Building WASM Apps

### C
```bash
clang --target=wasm32 -O2 -nostdlib -I wasm_apps/include \
  -Wl,--no-entry -Wl,--export=_start -Wl,--allow-undefined \
  -o app.wasm app.c
```

### Rust
```bash
rustup target add wasm32-unknown-unknown
cd rust_blink && cargo build --release
# Output: target/wasm32-unknown-unknown/release/<name>.wasm
```

### Swift (6.0+ with Embedded WASM support)
```bash
cd swift_display && swift build --triple wasm32-none-none-wasm
```

### Zig (0.13+)
```bash
cd zig_blink && zig build
```

### WAT
```bash
wat2wasm blink.wat -o blink.wasm
```

## App Entry Point

Every WASM app must export `_start`:

```c
void _start(void) {
    // Application code here
}
```

## Callback System

For async events (GPIO interrupts, timers, BLE), export:

```c
void wendy_handle_callback(int handler_id, int arg0, int arg1, int arg2);
```

- `handler_id`: The ID registered when setting up the callback (e.g., via `gpio_set_interrupt`, `timer_set_timeout`)
- Up to 32 concurrent handler IDs
- Callbacks are dispatched when `sys_yield()` is called
- ISR-safe: interrupts post events to a queue, dispatched from WASM thread context

---

## GPIO

### Constants

| Constant | Value | Description |
|----------|-------|-------------|
| `WENDY_GPIO_INPUT` | 0 | Input mode |
| `WENDY_GPIO_OUTPUT` | 1 | Output mode |
| `WENDY_GPIO_INPUT_OUTPUT` | 2 | Bidirectional |
| `WENDY_GPIO_PULL_NONE` | 0 | No pull resistor |
| `WENDY_GPIO_PULL_UP` | 1 | Pull-up resistor |
| `WENDY_GPIO_PULL_DOWN` | 2 | Pull-down resistor |
| `WENDY_GPIO_INTR_RISING` | 1 | Rising edge interrupt |
| `WENDY_GPIO_INTR_FALLING` | 2 | Falling edge interrupt |
| `WENDY_GPIO_INTR_ANYEDGE` | 3 | Any edge interrupt |

### Functions

```c
int gpio_configure(int pin, int mode, int pull);
int gpio_read(int pin);                              // Returns 0 or 1
int gpio_write(int pin, int level);                  // level: 0 or 1
int gpio_set_pwm(int pin, int freq_hz, int duty_pct); // duty: 0-100
int gpio_analog_read(int pin);                       // Returns ADC value
int gpio_set_interrupt(int pin, int edge_type, int handler_id);
int gpio_clear_interrupt(int pin);
```

All return 0 on success, non-zero on error.

---

## I2C

```c
int i2c_init(int bus, int sda, int scl, int freq_hz);
int i2c_scan(int bus, unsigned char *addrs_out, int max_addrs); // Returns count found
int i2c_write(int bus, int addr, const unsigned char *data, int len);
int i2c_read(int bus, int addr, unsigned char *buf, int len);
int i2c_write_read(int bus, int addr,
                    const unsigned char *wr, int wr_len,
                    unsigned char *rd, int rd_len);
```

Bus 0 is typically pre-initialized by firmware. Use `i2c_scan` to discover devices.

### Example: Reading a BMP280 Sensor

```c
#include "wendy.h"

void _start(void) {
    unsigned char addrs[16];
    int found = i2c_scan(0, addrs, 16);

    unsigned char reg = 0xD0;  // Chip ID register
    unsigned char chip_id;
    i2c_write_read(0, 0x76, &reg, 1, &chip_id, 1);

    // Configure and read temperature...
    unsigned char ctrl_cmd[2] = { 0xF4, 0x27 };
    i2c_write(0, 0x76, ctrl_cmd, 2);
    timer_delay_ms(100);

    unsigned char temp_reg = 0xFA;
    unsigned char raw[3];
    i2c_write_read(0, 0x76, &temp_reg, 1, raw, 3);
}
```

---

## RMT (Timing Buffer)

Low-level timing-based peripheral control (used internally by NeoPixel).

```c
int rmt_configure(int pin, int resolution_hz);
int rmt_transmit(int channel_id, const unsigned char *buf, int len);
int rmt_release(int channel_id);
```

---

## NeoPixel (WS2812)

```c
int neopixel_init(int pin, int num_leds);
int neopixel_set(int index, int r, int g, int b);  // r,g,b: 0-255
int neopixel_clear(void);
```

### Example

```c
neopixel_init(8, 4);          // 4 LEDs on GPIO 8
neopixel_set(0, 255, 0, 0);   // First LED: red
neopixel_set(1, 0, 255, 0);   // Second LED: green
timer_delay_ms(1000);
neopixel_clear();
```

---

## Timer

```c
void timer_delay_ms(int ms);                  // Blocking delay
long long timer_millis(void);                 // Monotonic milliseconds
int timer_set_timeout(int ms, int handler_id);  // One-shot callback, returns timer_id
int timer_set_interval(int ms, int handler_id); // Repeating callback, returns timer_id
int timer_cancel(int timer_id);
```

Timeout/interval callbacks are dispatched via `wendy_handle_callback` when `sys_yield()` is called.

---

## Console Output

```c
int wendy_print(const char *buf, int len);
```

Output goes to UART console and USB CDC (if connected). There is no libc `printf` — use `wendy_print` with manual formatting.

---

## System

```c
long long sys_uptime_ms(void);
void sys_reboot(void);
int sys_firmware_version(char *buf, int len);  // Returns bytes written
int sys_device_id(char *buf, int len);         // Returns bytes written
void sys_sleep_ms(int ms);
void sys_yield(void);  // IMPORTANT: dispatches pending callbacks
```

**`sys_yield()`** is critical for callback-based apps. Call it in your main loop to process pending GPIO interrupts, timer expirations, and BLE events.

---

## Storage (NVS)

Persistent key-value storage across reboots.

```c
int storage_get(const char *key, int key_len, char *val, int val_len);
int storage_set(const char *key, int key_len, const char *val, int val_len);
int storage_delete(const char *key, int key_len);
int storage_exists(const char *key, int key_len);  // Returns 1 if exists
```

---

## UART

```c
int uart_open(int port, int tx_pin, int rx_pin, int baud);
int uart_close(int port);
int uart_write(int port, const char *data, int len);
int uart_read(int port, char *buf, int len);     // Returns bytes read
int uart_available(int port);                     // Returns bytes available
int uart_flush(int port);
int uart_set_on_receive(int port, int handler_id); // Callback on data arrival
```

---

## SPI

```c
int spi_open(int host, int mosi, int miso, int sclk, int cs, int clock_hz);
int spi_close(int dev_id);
int spi_transfer(int dev_id, unsigned char *tx_buf, unsigned char *rx_buf, int len);
```

Full-duplex: `tx_buf` sent while `rx_buf` filled simultaneously.

---

## OpenTelemetry

### Log Levels

| Constant | Value |
|----------|-------|
| `WENDY_OTEL_ERROR` | 1 |
| `WENDY_OTEL_WARN` | 2 |
| `WENDY_OTEL_INFO` | 3 |
| `WENDY_OTEL_DEBUG` | 4 |

### Functions

```c
// Logging
int otel_log(int level, const char *msg, int msg_len);

// Metrics
int otel_metric_counter_add(const char *name, int name_len, long long value);
int otel_metric_gauge_set(const char *name, int name_len, double value);
int otel_metric_histogram_record(const char *name, int name_len, double value);

// Tracing
int otel_span_start(const char *name, int name_len);       // Returns span_id
int otel_span_set_attribute(int span_id, const char *key, int key_len,
                             const char *val, int val_len);
int otel_span_set_status(int span_id, int status);
int otel_span_end(int span_id);
```

---

## BLE (Optional, disabled by default)

Enable with `CONFIG_WENDY_BLE=y` in sdkconfig.

### Peripheral (GATT Server)

```c
int ble_init(void);
int ble_advertise_start(const char *name, int name_len);
int ble_advertise_stop(void);
int ble_gatts_add_service(const char *uuid, int uuid_len);
int ble_gatts_add_characteristic(int svc_id, const char *uuid, int uuid_len, int flags);
int ble_gatts_set_value(int chr_id, const char *data, int data_len);
int ble_gatts_notify(int chr_id, int conn_handle);
int ble_gatts_on_write(int chr_id, int handler_id);  // Callback on client write
```

### Central (GATT Client)

```c
int ble_scan_start(int duration_ms, int handler_id);
int ble_scan_stop(void);
int ble_connect(int addr_type, const char *addr, int addr_len, int handler_id);
int ble_disconnect(int conn_handle);
int ble_gattc_discover(int conn_handle, int handler_id);
int ble_gattc_read(int conn_handle, int attr_handle, int handler_id);
int ble_gattc_write(int conn_handle, int attr_handle, const char *data, int data_len);
```

---

## WiFi (App-facing)

```c
int wifi_connect(const char *ssid, int ssid_len, const char *pass, int pass_len);
int wifi_disconnect(void);
int wifi_status(void);                          // Returns connection state
int wifi_get_ip(char *buf, int len);            // Returns IP string length
int wifi_rssi(void);                            // Returns signal strength
int wifi_ap_start(const char *ssid, int ssid_len,
                   const char *pass, int pass_len, int channel);
int wifi_ap_stop(void);
```

---

## Sockets (Optional, disabled by default)

Enable with `CONFIG_WENDY_NET=y` in sdkconfig.

### Constants

| Constant | Value |
|----------|-------|
| `WENDY_AF_INET` | 2 |
| `WENDY_SOCK_STREAM` | 1 (TCP) |
| `WENDY_SOCK_DGRAM` | 2 (UDP) |

### Functions

```c
int net_socket(int domain, int type, int protocol);
int net_connect(int fd, const char *ip, int ip_len, int port);
int net_bind(int fd, int port);
int net_listen(int fd, int backlog);
int net_accept(int fd);
int net_send(int fd, const char *data, int len);
int net_recv(int fd, char *buf, int len);
int net_close(int fd);
```

---

## DNS

```c
int dns_resolve(const char *hostname, int hostname_len,
                 char *result_buf, int result_len);
```

---

## TLS

```c
int tls_connect(const char *host, int host_len, int port);
int tls_send(int fd, const char *data, int len);
int tls_recv(int fd, char *buf, int len);
int tls_close(int fd);
```

---

## USB (App-facing, optional)

Enable with `CONFIG_WENDY_APP_USB=y`. Not available on ESP32-C6 (no native USB OTG).

```c
int usb_cdc_write(const char *data, int len);
int usb_cdc_read(char *buf, int len);
int usb_hid_send_report(int report_id, const char *data, int len);
```
