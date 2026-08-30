# `wendy tour`

An interactive setup wizard that guides you through connecting your first Wendy device and configuring Wi-Fi.

## Usage

```sh
wendy tour
```

## Description

`wendy tour` walks through device discovery, Wi-Fi configuration, and initial provisioning in a step-by-step interactive flow. Where possible it prefills values automatically to reduce manual entry.

### SSID prefill

At the Wi-Fi configuration step, `wendy tour` attempts to detect the host machine's current Wi-Fi network and prefill the SSID field automatically.

If the SSID cannot be detected (e.g., the host is not connected to Wi-Fi, or the command fails), the field is left blank for manual entry.
