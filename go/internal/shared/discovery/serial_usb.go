package discovery

import (
	"strconv"
	"strings"
)

type serialUSBID struct {
	vendorID  uint16
	productID uint16
	transport SerialTransport
}

// supportedESP32SerialUSBIDs includes both the ESP32's native USB
// Serial/JTAG interface and the USB-to-UART bridge used on many Espressif
// development boards. The bridge ID is not unique to ESP32 hardware, but the
// installer treats it only as an unverified candidate after the user selects a
// Wendy Lite target. Runtime WendyCom discovery filters bridges out entirely.
var supportedESP32SerialUSBIDs = [...]serialUSBID{
	{vendorID: 0x303a, productID: 0x1001, transport: SerialTransportNativeUSB},  // Espressif native USB Serial/JTAG
	{vendorID: 0x10c4, productID: 0xea60, transport: SerialTransportUARTBridge}, // Silicon Labs CP210x USB-to-UART
}

func supportedESP32SerialUSBID(vendorID, productID uint16) (serialUSBID, bool) {
	for _, id := range supportedESP32SerialUSBIDs {
		if vendorID == id.vendorID && productID == id.productID {
			return id, true
		}
	}
	return serialUSBID{}, false
}

func parseUSBID(value string) (uint16, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.ToLower(value), "0x")
	parsed, err := strconv.ParseUint(value, 16, 16)
	return uint16(parsed), err
}
