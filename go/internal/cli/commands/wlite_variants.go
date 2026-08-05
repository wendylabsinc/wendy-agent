package commands

import (
	"encoding/json"
	"fmt"
	"strings"
)

// wendyLiteCatalogJSON is copied verbatim from wendy-lite/catalog.json.
// To update the variant list, paste the current file content between the
// backticks — nothing else to touch.
const wendyLiteCatalogJSON = `{
    "version": 1,
    "boards": [
        {
            "name": "esp32c5_generic",
            "display_name": "Generic ESP32-C5 board, 4MB flash",
            "binary": "wendy_mcu_esp32c5"
        },
        {
            "name": "esp32c6_generic",
            "display_name": "Generic ESP32-C6 board, 4MB flash",
            "binary": "wendy_mcu_esp32c6"
        },
        {
            "name": "esp32c6_generic_native",
            "display_name": "Generic ESP32-C6 board, 4MB flash, native app support",
            "binary": "wendy_mcu_esp32c6_native"
        },
        {
            "name": "esp32p4_waveshare_lcd_4b",
            "display_name": "Waveshare ESP32-P4-WIFI6-Touch-LCD-4B, 32MB flash",
            "binary": "wendy_mcu_esp32p4_waveshare_lcd_4b"
        },
        {
            "name": "esp32p4_dfr1172_firebeetle",
            "display_name": "DFRobot FireBeetle 2 ESP32-P4 (DFR1172), 16MB flash",
            "binary": "wendy_mcu_esp32p4_dfr1172_firebeetle"
        },
        {
            "name": "esp32s3_seeed_xiao_native",
            "display_name": "Seeed Studio XIAO ESP32S3, 8MB flash, 8MB PSRAM, native app support",
            "binary": "wendy_mcu_esp32s3_seeed_xiao_native"
        },
        {
            "name": "esp32s3_m5_stamp_s3_native",
            "display_name": "M5Stack StampS3, 8MB flash, native app support",
            "binary": "wendy_mcu_esp32s3_m5_stamp_s3_native"
        }
    ]
}`

// WendyLiteVariant is one installable Wendy Lite flavor: a board paired with
// the firmware binary built for it.
type WendyLiteVariant struct {
	Board       string
	DisplayName string
	Version     string
}

// wendyLiteCatalogBoard is one entry of the embedded catalog's boards array.
type wendyLiteCatalogBoard struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Binary      string `json:"binary"`
}

// wendyLiteCatalogBoards parses the embedded catalog and returns its boards
// in catalog order.
func wendyLiteCatalogBoards() ([]wendyLiteCatalogBoard, error) {
	var catalog struct {
		Boards []wendyLiteCatalogBoard `json:"boards"`
	}
	if err := json.Unmarshal([]byte(wendyLiteCatalogJSON), &catalog); err != nil {
		return nil, fmt.Errorf("parsing embedded Wendy Lite catalog: %w", err)
	}
	return catalog.Boards, nil
}

// WendyLiteVariants returns the Wendy Lite variants from the embedded
// catalog, in catalog order. The catalog carries no version information yet,
// so Version is always "(latest)".
func WendyLiteVariants() ([]WendyLiteVariant, error) {
	boards, err := wendyLiteCatalogBoards()
	if err != nil {
		return nil, err
	}

	variants := make([]WendyLiteVariant, 0, len(boards))
	for _, b := range boards {
		variants = append(variants, WendyLiteVariant{
			Board:       b.Name,
			DisplayName: b.DisplayName,
			Version:     "(latest)",
		})
	}
	return variants, nil
}

// WendyLiteFirmwareID returns the firmware ID for a catalog board: its binary
// name without the "wendy_mcu_" prefix. Firmware IDs are the keys the GCS
// firmware manifests are published under.
func WendyLiteFirmwareID(board string) (string, error) {
	boards, err := wendyLiteCatalogBoards()
	if err != nil {
		return "", err
	}
	for _, b := range boards {
		if b.Name == board {
			return strings.TrimPrefix(b.Binary, "wendy_mcu_"), nil
		}
	}
	return "", fmt.Errorf("board %q not found in the Wendy Lite catalog", board)
}
