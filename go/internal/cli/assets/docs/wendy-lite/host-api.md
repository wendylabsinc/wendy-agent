# Host API Reference

All functions are imported from the `"wendy"` WASM module. The canonical declaration source is `Sources/CWendyLite/include/wendy.h`. This page documents every function by subsystem, including the Swift and Rust equivalents.

## Callback Model

Many APIs are asynchronous. They accept a `handler_id` (an `int`/`Int32` you choose) and fire `wendy_handle_callback` on the guest when the event arrives. Your guest must export this function:

```c
void wendy_handle_callback(int handler_id, int arg0, int arg1, int arg2);
```

Swift guests using `WendyLiteApp` get this export automatically (via `CallbackDispatch`). C and Rust guests must export it manually and call `sys_yield()` / `sys::yield_now()` periodically to receive dispatched events.

---

## GPIO

| C function | Swift | Rust (`gpio::`) |
|------------|-------|----------------|
| `gpio_configure(pin, mode, pull)` | `GPIO.configure(pin:mode:pull:)` | `configure(pin, mode, pull)` |
| `gpio_read(pin)` | `GPIO.read(pin:)` | `read(pin)` |
| `gpio_write(pin, level)` | `GPIO.write(pin:level:)` | `write(pin, level)` |
| `gpio_set_pwm(pin, freq_hz, duty_pct)` | `GPIO.setPWM(pin:frequencyHz:dutyPercent:)` | `set_pwm(pin, freq_hz, duty_pct)` |
| `gpio_analog_read(pin)` | `GPIO.analogRead(pin:)` | `analog_read(pin)` |
| `gpio_set_interrupt(pin, edge, handler_id)` | `GPIO.setInterrupt(pin:edge:handlerId:)` | `set_interrupt(pin, edge, handler_id)` |
| `gpio_clear_interrupt(pin)` | `GPIO.clearInterrupt(pin:)` | `clear_interrupt(pin)` |

**Mode constants:** `WENDY_GPIO_INPUT=0`, `WENDY_GPIO_OUTPUT=1`, `WENDY_GPIO_INPUT_OUTPUT=2`

**Pull constants:** `WENDY_GPIO_PULL_NONE=0`, `WENDY_GPIO_PULL_UP=1`, `WENDY_GPIO_PULL_DOWN=2`

**Interrupt edge constants:** `WENDY_GPIO_INTR_RISING=1`, `WENDY_GPIO_INTR_FALLING=2`, `WENDY_GPIO_INTR_ANYEDGE=3`

Swift enums: `GPIOMode`, `GPIOPull`, `GPIOInterruptEdge`.

---

## I2C

| C function | Swift (`I2C.`) | Rust (`i2c::`) |
|------------|----------------|----------------|
| `i2c_init(bus, sda, scl, freq_hz)` | `initialize(bus:sdaPin:sclPin:frequencyHz:)` | `init(bus, sda, scl, freq_hz)` |
| `i2c_scan(bus, addrs_out, max_addrs)` | `scan(bus:buffer:maxAddresses:)` | `scan(bus, addrs_out)` |
| `i2c_write(bus, addr, data, len)` | `write(bus:address:data:length:)` | `write(bus, addr, data)` |
| `i2c_read(bus, addr, buf, len)` | `read(bus:address:buffer:length:)` | `read(bus, addr, buf)` |
| `i2c_write_read(bus, addr, wr, wr_len, rd, rd_len)` | `writeRead(bus:address:writeData:writeLength:readBuffer:readLength:)` | `write_read(bus, addr, wr, rd)` |

---

## SPI

| C function | Swift (`SPI.`) | Rust (`spi::`) |
|------------|----------------|----------------|
| `spi_open(host, mosi, miso, sclk, cs, clock_hz)` | `open(host:mosiPin:misoPin:sclkPin:csPin:clockHz:)` | `open(host, mosi, miso, sclk, cs, clock_hz)` |
| `spi_close(dev_id)` | `close(deviceId:)` | `close(dev_id)` |
| `spi_transfer(dev_id, tx_buf, rx_buf, len)` | `transfer(deviceId:txBuffer:rxBuffer:length:)` | `transfer(dev_id, tx, rx)` / `transmit(dev_id, tx)` |

The Rust crate adds a `transmit` helper for TX-only transfers (passes `null` as `rx_buf`).

---

## UART

| C function | Swift (`UART.`) | Rust (`uart::`) |
|------------|-----------------|-----------------|
| `uart_open(port, tx_pin, rx_pin, baud)` | `open(port:txPin:rxPin:baud:)` | `open(port, tx_pin, rx_pin, baud)` |
| `uart_close(port)` | `close(port:)` | `close(port)` |
| `uart_write(port, data, len)` | `write(port:data:length:)` | `write(port, data)` |
| `uart_read(port, buf, len)` | `read(port:buffer:length:)` | `read(port, buf)` |
| `uart_available(port)` | `available(port:)` | `available(port)` |
| `uart_flush(port)` | `flush(port:)` | `flush(port)` |
| `uart_set_on_receive(port, handler_id)` | `setOnReceive(port:handlerId:)` | `set_on_receive(port, handler_id)` |

`setOnReceive` registers a callback handler ID; the host fires `wendy_handle_callback` when data arrives.

---

## RMT (Remote Control / Timing Buffer)

| C function | Swift (`RMT.`) | Rust (`rmt::`) |
|------------|----------------|----------------|
| `rmt_configure(pin, resolution_hz)` | `configure(pin:resolutionHz:)` | `configure(pin, resolution_hz)` |
| `rmt_transmit(channel_id, buf, len)` | `transmit(channelId:buffer:length:)` | `transmit(channel_id, buf)` |
| `rmt_release(channel_id)` | `release(channelId:)` | `release(channel_id)` |

Used for WS2812 protocols, IR, and other timing-sensitive serial protocols.

---

## NeoPixel (WS2812)

High-level API layered on RMT.

| C function | Swift (`NeoPixel.`) | Rust (`neopixel::`) |
|------------|---------------------|---------------------|
| `neopixel_init(pin, num_leds)` | `initialize(pin:numLeds:)` | `init(pin, num_leds)` |
| `neopixel_set(index, r, g, b)` | `set(index:r:g:b:)` | `set(index, r, g, b)` |
| `neopixel_clear()` | `clear()` | `clear()` |

---

## Timer

| C function | Swift (`Timer.`) | Rust (`timer::`) |
|------------|-----------------|-----------------|
| `timer_delay_ms(ms)` | `delayMs(_:)` | `delay_ms(ms)` |
| `timer_millis()` | `millis()` | `millis()` |
| `timer_set_timeout(ms, handler_id)` | `setTimeout(ms:handlerId:)` | `set_timeout(ms, handler_id)` |
| `timer_set_interval(ms, handler_id)` | `setInterval(ms:handlerId:)` | `set_interval(ms, handler_id)` |
| `timer_cancel(timer_id)` | `cancel(timerId:)` | `cancel(timer_id)` |

`setTimeout` and `setInterval` return a timer ID. Pass it to `cancel` to stop the timer before it fires.

Swift apps should prefer `WendyClock.sleep` over `Timer.delayMs` — the former is async-safe, the latter is a blocking spin.

---

## System

| C function | Swift (`System.` / `Console.`) | Rust (`sys::`) |
|------------|-------------------------------|----------------|
| `sys_uptime_ms()` | `System.uptimeMs()` | `sys::uptime_ms()` |
| `sys_reboot()` | `System.reboot()` | `sys::reboot()` |
| `sys_firmware_version(buf, len)` | `System.firmwareVersion(buffer:length:)` | `sys::firmware_version(buf)` |
| `sys_device_id(buf, len)` | `System.deviceId(buffer:length:)` | `sys::device_id(buf)` |
| `sys_sleep_ms(ms)` | `System.sleepMs(_:)` | `sys::sleep_ms(ms)` |
| `sys_yield()` | `System.yield()` | `sys::yield_now()` |
| `sys_wait_for_event(timeout_ms)` | `System.waitForEvent(timeoutMs:)` | — |
| `wendy_print(buf, len)` | `Console.print(_:length:)` | `console::print(buf)` |

`sys_wait_for_event` blocks until the host has a pending callback or the timeout elapses. Swift apps call this indirectly via `pumpAsyncRuntimeOnce`. C/Rust apps should call `sys_yield` periodically to allow callbacks to be dispatched.

---

## Storage (NVS)

Non-Volatile Storage key-value API. Keys and values are byte buffers.

| C function | Swift (`Storage.`) | Rust (`storage::`) |
|------------|--------------------|--------------------|
| `storage_get(key, key_len, val, val_len)` | `get(key:keyLength:value:valueLength:)` | `get(key, val)` |
| `storage_set(key, key_len, val, val_len)` | `set(key:keyLength:value:valueLength:)` | `set(key, val)` |
| `storage_delete(key, key_len)` | `delete(key:keyLength:)` | `delete(key)` |
| `storage_exists(key, key_len)` | `exists(key:keyLength:)` | `exists(key)` |

---

## BLE

### Core BLE

| C function | Swift (`BLE.`) | Rust (`ble::`) |
|------------|----------------|----------------|
| `ble_init()` | `initialize()` | `init()` |
| `ble_advertise_start(name, name_len)` | `startAdvertising(name:nameLength:)` | `advertise_start(name)` |
| `ble_advertise_stop()` | `stopAdvertising()` | `advertise_stop()` |
| `ble_scan_start(duration_ms, handler_id)` | `startScan(durationMs:handlerId:)` | `scan_start(duration_ms, handler_id)` |
| `ble_scan_stop()` | `stopScan()` | `scan_stop()` |
| `ble_connect(addr_type, addr, addr_len, handler_id)` | `connect(addressType:address:addressLength:handlerId:)` | `connect(addr_type, addr, handler_id)` |
| `ble_disconnect(conn_handle)` | `disconnect(connHandle:)` | `disconnect(conn_handle)` |

### GATT Server (`GATTS` / `ble::gatts`)

| C function | Swift (`GATTS.`) | Rust (`ble::gatts::`) |
|------------|------------------|-----------------------|
| `ble_gatts_add_service(uuid, uuid_len)` | `addService(uuid:uuidLength:)` → service ID | `add_service(uuid)` |
| `ble_gatts_add_characteristic(svc_id, uuid, uuid_len, flags)` | `addCharacteristic(serviceId:uuid:uuidLength:flags:)` → char ID | `add_characteristic(svc_id, uuid, flags)` |
| `ble_gatts_set_value(chr_id, data, data_len)` | `setValue(characteristicId:data:dataLength:)` | `set_value(chr_id, data)` |
| `ble_gatts_notify(chr_id, conn_handle)` | `notify(characteristicId:connHandle:)` | `notify(chr_id, conn_handle)` |
| `ble_gatts_on_write(chr_id, handler_id)` | `onWrite(characteristicId:handlerId:)` | `on_write(chr_id, handler_id)` |

### GATT Client (`GATTC` / `ble::gattc`)

| C function | Swift (`GATTC.`) | Rust (`ble::gattc::`) |
|------------|------------------|-----------------------|
| `ble_gattc_discover(conn_handle, handler_id)` | `discover(connHandle:handlerId:)` | `discover(conn_handle, handler_id)` |
| `ble_gattc_read(conn_handle, attr_handle, handler_id)` | `read(connHandle:attrHandle:handlerId:)` | `read(conn_handle, attr_handle, handler_id)` |
| `ble_gattc_write(conn_handle, attr_handle, data, data_len)` | `write(connHandle:attrHandle:data:dataLength:)` | `write(conn_handle, attr_handle, data)` |

---

## WiFi

| C function | Swift (`WiFi.`) | Rust (`wifi::`) |
|------------|-----------------|-----------------|
| `wifi_connect(ssid, ssid_len, pass, pass_len)` | `connect(ssid:ssidLength:password:passwordLength:)` | `connect(ssid, pass)` |
| `wifi_disconnect()` | `disconnect()` | `disconnect()` |
| `wifi_status()` | `status()` | `status()` |
| `wifi_get_ip(buf, len)` | `getIP(buffer:length:)` | `get_ip(buf)` |
| `wifi_rssi()` | `rssi()` | `rssi()` |
| `wifi_ap_start(ssid, ssid_len, pass, pass_len, channel)` | `startAP(ssid:ssidLength:password:passwordLength:channel:)` | `ap_start(ssid, pass, channel)` |
| `wifi_ap_stop()` | `stopAP()` | `ap_stop()` |

---

## Sockets

| C function | Swift (`Net.`) | Rust (`net::`) |
|------------|----------------|----------------|
| `net_socket(domain, type, protocol)` | `socket(domain:type:protocol:)` | `socket(domain, sock_type, protocol)` |
| `net_connect(fd, ip, ip_len, port)` | `connect(fd:ip:ipLength:port:)` | `connect(fd, ip, port)` |
| `net_bind(fd, port)` | `bind(fd:port:)` | `bind(fd, port)` |
| `net_listen(fd, backlog)` | `listen(fd:backlog:)` | `listen(fd, backlog)` |
| `net_accept(fd)` | `accept(fd:)` | `accept(fd)` |
| `net_send(fd, data, len)` | `send(fd:data:length:)` | `send(fd, data)` |
| `net_recv(fd, buf, len)` | `recv(fd:buffer:length:)` | `recv(fd, buf)` |
| `net_close(fd)` | `close(fd:)` | `close(fd)` |

Swift enums: `SocketDomain` (`.inet = 2`), `SocketType` (`.stream = 1`, `.dgram = 2`).

C constants: `WENDY_AF_INET=2`, `WENDY_SOCK_STREAM=1`, `WENDY_SOCK_DGRAM=2`.

---

## DNS

| C function | Swift (`DNS.`) | Rust (`dns::`) |
|------------|----------------|----------------|
| `dns_resolve(hostname, hostname_len, result_buf, result_len)` | `resolve(hostname:hostnameLength:resultBuffer:resultLength:)` | `resolve(hostname, result_buf)` |

---

## TLS

| C function | Swift (`TLS.`) | Rust (`tls::`) |
|------------|----------------|----------------|
| `tls_connect(host, host_len, port)` | `connect(host:hostLength:port:)` → fd | `connect(host, port)` |
| `tls_send(fd, data, len)` | `send(fd:data:length:)` | `send(fd, data)` |
| `tls_recv(fd, buf, len)` | `recv(fd:buffer:length:)` | `recv(fd, buf)` |
| `tls_close(fd)` | `close(fd:)` | `close(fd)` |

---

## OpenTelemetry

| C function | Swift (`OTel.`) | Rust (`otel::`) |
|------------|-----------------|-----------------|
| `otel_log(level, msg, msg_len)` | `log(level:message:messageLength:)` | `log(level, msg)` |
| `otel_metric_counter_add(name, name_len, value)` | `counterAdd(name:nameLength:value:)` | `counter_add(name, value)` |
| `otel_metric_gauge_set(name, name_len, value)` | `gaugeSet(name:nameLength:value:)` | `gauge_set(name, value)` |
| `otel_metric_histogram_record(name, name_len, value)` | `histogramRecord(name:nameLength:value:)` | `histogram_record(name, value)` |
| `otel_span_start(name, name_len)` | `spanStart(name:nameLength:)` → span ID | `span_start(name)` |
| `otel_span_set_attribute(span_id, key, key_len, val, val_len)` | `spanSetAttribute(spanId:key:keyLength:value:valueLength:)` | `span_set_attribute(span_id, key, val)` |
| `otel_span_set_status(span_id, status)` | `spanSetStatus(spanId:status:)` | `span_set_status(span_id, status)` |
| `otel_span_end(span_id)` | `spanEnd(spanId:)` | `span_end(span_id)` |

Log level constants: `WENDY_OTEL_ERROR=1`, `WENDY_OTEL_WARN=2`, `WENDY_OTEL_INFO=3`, `WENDY_OTEL_DEBUG=4`.

Swift enum: `OTelLogLevel`.

---

## USB

| C function | Swift (`USB.`) | Rust (`usb::`) |
|------------|----------------|----------------|
| `usb_cdc_write(data, len)` | `cdcWrite(_:length:)` | `cdc_write(data)` |
| `usb_cdc_read(buf, len)` | `cdcRead(_:length:)` | `cdc_read(buf)` |
| `usb_hid_send_report(report_id, data, len)` | `hidSendReport(reportId:data:length:)` | `hid_send_report(report_id, data)` |
