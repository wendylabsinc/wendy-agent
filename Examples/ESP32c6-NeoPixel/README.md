# ESP32-C6 NeoPixel

A minimal native ESP-IDF sample that cycles a WS2812/NeoPixel through red,
green, blue, and off. It defaults to the onboard addressable LED found on
common ESP32-C6-DevKitC-1 boards:

- Data pin: GPIO 8
- Pixel count: 1
- Brightness: 32/255 per active color channel
- Step time: 750 ms

## Build and flash with ESP-IDF

ESP-IDF 5.1 or newer is required. From this directory:

```sh
idf.py set-target esp32c6
idf.py build
idf.py -p /dev/cu.usbmodemXXXX flash monitor
```

Replace the serial port with the one used by your board. Exit the monitor with
`Ctrl-]`.

## Configure another board or strip

Run:

```sh
idf.py menuconfig
```

Open **NeoPixel Configuration** to change the GPIO, pixel count, brightness,
or animation delay, then rebuild. For an external strip, connect its data input
to the configured GPIO and connect the strip ground to the ESP32-C6 ground.
Use a suitable external supply for more than a few pixels; do not power a large
strip from the development board.

## Deploy with Wendy

Once a native-app-capable Wendy Lite device is available:

```sh
wendy run --device <name>
```

Wendy selects the ESP32-C6 target from the connected device, builds the
ESP-IDF project, and flashes the resulting firmware.
