# WDY-1509 CLI Surface Ledger

This ledger preserves the post-beta CLI surface snapshot for WDY-1509 and maps
each leaf command to its current Swift E2E reference suite. Treat it as the
umbrella handoff ledger for WDY-1511 through WDY-1517, not as a fully reviewed
source of truth.

## Surface dump

- Built CLI: `go/bin/wendy` from this worktree with `make build-cli`.
- `wendy --experimental-dump-help` is not present in the current binary; the command tree below was generated from `commands.NewRootCmd()` with a temporary Cobra walker.
- Commands found: 135 total non-internal commands including hidden deprecated compatibility commands; 106 leaf commands.
- The generated dump intentionally excludes the internal `wendy __ble-check` helper from the leaf command map.

## Hidden/internal commands and aliases

| Surface | Classification | Audit note |
| --- | --- | --- |
| `wendy __ble-check` | Internal hidden command | CoreBluetooth subprocess helper. Exclude from user-facing E2E reference coverage. |
| `wendy device version` | Hidden deprecated compatibility command | Alias-like predecessor for `wendy device info`. Human mode warns; JSON/non-interactive output should remain clean. Covered by WDY-1512 follow-up policy work. |
| `wendy cloud device version` | Hidden deprecated compatibility command | Cloud-routed predecessor for `wendy cloud device info`. Should warn in human mode and remain clean in JSON/non-interactive mode. Covered by WDY-1512 follow-up policy work. |
| `wendy cloud run` | Hidden deprecated command | Delegates to `wendy run` with cloud context. Covered by WDY-1512 follow-up policy work. |
| `wendy device ps` | Public alias command | Surfaced in `wendy device --help` as an alias for `wendy device apps list`. Covered by WDY-1512 follow-up policy work. |
| `wendy cloud device ps` | Public alias command | Surfaced in `wendy cloud device --help` as an alias for `wendy cloud device apps list`. Covered by WDY-1512 follow-up policy work. |
| `wendy device bluetooth` / `wendy device bt` | Cobra alias | `bt` is accepted and appears in the Bluetooth command's own help. Covered by WDY-1512 follow-up policy work. |
| `wendy cloud device bluetooth` / `wendy cloud device bt` | Cobra alias | Cloud-routed mirror of the Bluetooth `bt` alias. Covered by WDY-1512 follow-up policy work. |
| `wendy completion install --output-dir` | Hidden test seam | Misleading home-directory override used by tests. Cleanup tracked separately in WDY-1511. |

## Route/platform buckets

| Bucket | Commands | Current expectation |
| --- | --- | --- |
| Host-only CLI/config | `analytics`, `auth`, `cache`, `completion`, `info`, `init`, `json`, `mcp`, `project`, `tour`, `utils open-browser` | Runs on developer host; Mac/Linux differences are host filesystem, shell, browser, and tool-detection behavior only. |
| Host OS imaging | `os cache`, `os download`, `os install`, `os list-drives` | Runs on developer host; drive enumeration/install has platform-specific implementations. Unsupported host platforms should fail before destructive actions. |
| WendyOS OTA | `os update` | Requires a WendyOS OTA-capable target. Linux host agents and Darwin/macOS agents intentionally fail with unsupported-OS guidance. |
| Direct local agent | `device ...` | Direct gRPC/BLE route to selected target. Linux/WendyOS is the full feature target; Darwin/macOS agent is beta and only selected app/native-process paths are expected. |
| Cloud/tunnel agent | `cloud device ...`, `cloud tunnel`, hidden `cloud run` | Authenticated Wendy Cloud route. Device subcommands use the same agent semantics as direct routes after tunnel establishment. |
| Not practical for fully automated E2E yet | live dashboards, long streams, audio/video playback, OS flashing, Wi-Fi/Bluetooth mutation, cloud auth/browser flows | Kept as disabled Swift Testing reference specs unless the harness already has a deterministic seam. |

## Child issue handoff

| Issue | Owns | Ledger rows / observations to start from |
| --- | --- | --- |
| WDY-1511 | Completion install hidden test seam | `wendy completion install --output-dir`; confirm the test-only home-directory override is removed or renamed without changing public completion behavior. |
| WDY-1512 | Hidden/deprecated/public alias policy | `wendy device version`, `wendy cloud device version`, hidden `wendy cloud run`, public `device ps` / `cloud device ps`, and Bluetooth `bt` aliases. |
| WDY-1513 | Host-only CLI E2E references | `analytics`, `auth`, `cache`, `completion`, `info`, `init`, `json`, `mcp`, `project`, `tour`, `utils open-browser`, plus host discovery semantics where relevant. |
| WDY-1514 | OS imaging and update E2E references | `os cache`, `os download`, `os install`, `os list-drives`, and WendyOS-only `os update` behavior against non-OTA Linux or Darwin agents. |
| WDY-1515 | Direct device command E2E references | Public `device ...` routes, including app/native-process beta paths and macOS unsupported diagnostics for hardware, camera, Wi-Fi, Bluetooth, audio, volumes, streams, and dashboards. |
| WDY-1516 | Cloud-routed device E2E references | `cloud discover`, `cloud enroll-device`, `cloud tunnel`, and public `cloud device ...` routes, with auth/tunnel errors distinguished from agent errors. |
| WDY-1517 | Build and run E2E references | `wendy build`, `wendy run`, and hidden `wendy cloud run`; separate host build behavior from target deployment behavior, and keep Darwin target support limited to native macOS apps. |

## Leaf command map

| Command | Surface | E2E suite | Route / platform expectation |
| --- | --- | --- | --- |
| `wendy analytics disable` | public | `WendyAnalyticsDisableTests.swift` | Host-only CLI/config command. |
| `wendy analytics enable` | public | `WendyAnalyticsEnableTests.swift` | Host-only CLI/config command. |
| `wendy analytics status` | public | `WendyAnalyticsStatusTests.swift` | Host-only CLI/config command. |
| `wendy auth login` | public | `WendyAuthLoginTests.swift` | Host-only CLI/config command. |
| `wendy auth logout` | public | `WendyAuthLogoutTests.swift` | Host-only CLI/config command. |
| `wendy auth refresh-certs` | public | `WendyAuthRefreshCertsTests.swift` | Host-only CLI/config command. |
| `wendy auth status` | public | `WendyAuthStatusTests.swift` | Host-only CLI/config command. |
| `wendy build` | public | `WendyBuildTests.swift` | Host build route; macOS/Linux hosts supported for Swift image builds, Docker/provider availability controls other paths. |
| `wendy cache clear` | public | `WendyCacheClearTests.swift` | Host-only CLI/config command. |
| `wendy cache list` | public | `WendyCacheListTests.swift` | Host-only CLI/config command. |
| `wendy cloud device apps list` | public | `WendyCloudDeviceAppsListTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device apps remove` | public | `WendyCloudDeviceAppsRemoveTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device apps start` | public | `WendyCloudDeviceAppsStartTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device apps stop` | public | `WendyCloudDeviceAppsStopTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device audio list` | public | `WendyCloudDeviceAudioListTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device audio listen` | public | `WendyCloudDeviceAudioListenTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device audio monitor` | public | `WendyCloudDeviceAudioMonitorTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device audio set-default` | public | `WendyCloudDeviceAudioSetDefaultTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device bluetooth connect` | public | `WendyCloudDeviceBluetoothConnectTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device bluetooth disconnect` | public | `WendyCloudDeviceBluetoothDisconnectTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device bluetooth forget` | public | `WendyCloudDeviceBluetoothForgetTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device bluetooth list` | public | `WendyCloudDeviceBluetoothListTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device camera list` | public | `WendyCloudDeviceCameraListTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device camera view` | public | `WendyCloudDeviceCameraViewTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device dashboard` | public | `WendyCloudDeviceDashboardTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device enroll` | public | `WendyCloudDeviceEnrollTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device hardware list` | public | `WendyCloudDeviceHardwareListTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device info` | public | `MISSING` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device logs` | public | `WendyCloudDeviceLogsTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device ps` | public | `MISSING` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device set-default` | public | `WendyCloudDeviceSetDefaultTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device setup` | public | `WendyCloudDeviceSetupTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device telemetry-stream` | public | `WendyCloudDeviceTelemetryStreamTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device unset-default` | public | `WendyCloudDeviceUnsetDefaultTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device update` | public | `WendyCloudDeviceUpdateTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device version` | hidden | `WendyCloudDeviceVersionTests.swift` | Cloud tunnel to agent; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy cloud device volumes list` | public | `WendyCloudDeviceVolumesListTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device volumes remove` | public | `WendyCloudDeviceVolumesRemoveTests.swift` | Cloud tunnel to agent. Linux/WendyOS target expected unless command documents macOS beta support/unsupported behavior. |
| `wendy cloud device wifi connect` | public | `WendyCloudDeviceWifiConnectTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device wifi disconnect` | public | `WendyCloudDeviceWifiDisconnectTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device wifi forget` | public | `WendyCloudDeviceWifiForgetTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device wifi list` | public | `WendyCloudDeviceWifiListTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device wifi rank` | public | `WendyCloudDeviceWifiRankTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud device wifi status` | public | `WendyCloudDeviceWifiStatusTests.swift` | Cloud tunnel to agent; Linux/WendyOS feature path, with macOS beta unsupported diagnostics expected for hardware/camera/Wi-Fi/Bluetooth probe paths. |
| `wendy cloud discover` | public | `WendyCloudDiscoverTests.swift` | Host-to-Wendy Cloud route; no local agent required unless command opens a tunnel. |
| `wendy cloud enroll-device` | public | `WendyCloudEnrollDeviceTests.swift` | Host-to-Wendy Cloud route; no local agent required unless command opens a tunnel. |
| `wendy cloud run` | hidden | `WendyCloudRunTests.swift` | Hidden deprecated cloud alias for wendy run; cloud/tunnel route. |
| `wendy cloud tunnel` | public | `WendyCloudTunnelTests.swift` | Host-to-Wendy Cloud route; no local agent required unless command opens a tunnel. |
| `wendy completion bash` | public | `WendyCompletionBashTests.swift` | Host-only CLI/config command. |
| `wendy completion fish` | public | `WendyCompletionFishTests.swift` | Host-only CLI/config command. |
| `wendy completion install` | public | `WendyCompletionInstallTests.swift` | Host-only CLI/config command. |
| `wendy completion powershell` | public | `WendyCompletionPowershellTests.swift` | Host-only CLI/config command. |
| `wendy completion zsh` | public | `WendyCompletionZshTests.swift` | Host-only CLI/config command. |
| `wendy device apps list` | public | `WendyDeviceAppsListTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device apps remove` | public | `WendyDeviceAppsRemoveTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device apps start` | public | `WendyDeviceAppsStartTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device apps stop` | public | `WendyDeviceAppsStopTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device audio list` | public | `WendyDeviceAudioListTests.swift` | Direct local agent route. |
| `wendy device audio listen` | public | `WendyDeviceAudioListenTests.swift` | Direct local agent route. |
| `wendy device audio monitor` | public | `WendyDeviceAudioMonitorTests.swift` | Direct local agent route. |
| `wendy device audio set-default` | public | `WendyDeviceAudioSetDefaultTests.swift` | Direct device-management route; host config/cloud enrollment plus target-specific agent behavior. |
| `wendy device bluetooth connect` | public | `WendyDeviceBluetoothConnectTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device bluetooth disconnect` | public | `WendyDeviceBluetoothDisconnectTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device bluetooth forget` | public | `WendyDeviceBluetoothForgetTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device bluetooth list` | public | `WendyDeviceBluetoothListTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device camera list` | public | `WendyDeviceCameraListTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device camera view` | public | `WendyDeviceCameraViewTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device dashboard` | public | `WendyDeviceDashboardTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device enroll` | public | `WendyDeviceEnrollTests.swift` | Direct device-management route; host config/cloud enrollment plus target-specific agent behavior. |
| `wendy device hardware list` | public | `WendyDeviceHardwareListTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device info` | public | `WendyDeviceInfoTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device logs` | public | `WendyDeviceLogsTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device ps` | public | `MISSING` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device set-default` | public | `WendyDeviceSetDefaultTests.swift` | Direct device-management route; host config/cloud enrollment plus target-specific agent behavior. |
| `wendy device setup` | public | `WendyDeviceSetupTests.swift` | Direct device-management route; host config/cloud enrollment plus target-specific agent behavior. |
| `wendy device telemetry-stream` | public | `WendyDeviceTelemetryStreamTests.swift` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device unset-default` | public | `WendyDeviceUnsetDefaultTests.swift` | Direct device-management route; host config/cloud enrollment plus target-specific agent behavior. |
| `wendy device update` | public | `WendyDeviceUpdateTests.swift` | Direct device-management route; host config/cloud enrollment plus target-specific agent behavior. |
| `wendy device version` | hidden | `MISSING` | Direct local agent route; Linux/WendyOS app/runtime path, Darwin agent route is native-app beta where applicable. |
| `wendy device volumes list` | public | `WendyDeviceVolumesListTests.swift` | Direct local agent storage route; Linux/WendyOS container volume semantics, Darwin coverage not promised beyond clear failures. |
| `wendy device volumes remove` | public | `WendyDeviceVolumesRemoveTests.swift` | Direct local agent storage route; Linux/WendyOS container volume semantics, Darwin coverage not promised beyond clear failures. |
| `wendy device wifi connect` | public | `WendyDeviceWifiConnectTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device wifi disconnect` | public | `WendyDeviceWifiDisconnectTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device wifi forget` | public | `WendyDeviceWifiForgetTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device wifi list` | public | `WendyDeviceWifiListTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device wifi rank` | public | `WendyDeviceWifiRankTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy device wifi status` | public | `WendyDeviceWifiStatusTests.swift` | Direct local agent route; Linux/WendyOS feature path, macOS beta unsupported diagnostics expected where the agent returns Unimplemented. |
| `wendy discover` | public | `WendyDiscoverTests.swift` | Host discovery route; LAN/BLE/provider discovery, platform-specific clipboard/BLE behavior. |
| `wendy info` | public | `WendyInfoTests.swift` | Host-only CLI/config command. |
| `wendy init` | public | `WendyInitTests.swift` | Host-only CLI/config command. |
| `wendy json schema` | public | `WendyJsonSchemaTests.swift` | Host-only CLI/config command. |
| `wendy json validate` | public | `WendyJsonValidateTests.swift` | Host-only CLI/config command. |
| `wendy mcp serve` | public | `WendyMcpServeTests.swift` | Host-only CLI/config command. |
| `wendy mcp setup` | public | `WendyMcpSetupTests.swift` | Host-only CLI/config command. |
| `wendy os cache clear` | public | `WendyOsCacheClearTests.swift` | Host OS image-management route; platform behavior differs for drive listing/install provisioning on macOS/Linux/Windows. |
| `wendy os cache list` | public | `WendyOsCacheListTests.swift` | Host OS image-management route; platform behavior differs for drive listing/install provisioning on macOS/Linux/Windows. |
| `wendy os download` | public | `WendyOsDownloadTests.swift` | Host OS image-management route; platform behavior differs for drive listing/install provisioning on macOS/Linux/Windows. |
| `wendy os install` | public | `WendyOsInstallTests.swift` | Host OS image-management route; platform behavior differs for drive listing/install provisioning on macOS/Linux/Windows. |
| `wendy os list-drives` | public | `WendyOsListDrivesTests.swift` | Host OS image-management route; platform behavior differs for drive listing/install provisioning on macOS/Linux/Windows. |
| `wendy os update` | public | `WendyOsUpdateTests.swift` | Direct agent route; WendyOS OTA only. Linux host agents and Darwin agents must fail with unsupported-OS guidance. |
| `wendy project entitlements add` | public | `WendyProjectEntitlementsAddTests.swift` | Host-only CLI/config command. |
| `wendy project entitlements list` | public | `WendyProjectEntitlementsListTests.swift` | Host-only CLI/config command. |
| `wendy project entitlements remove` | public | `WendyProjectEntitlementsRemoveTests.swift` | Host-only CLI/config command. |
| `wendy run` | public | `WendyRunTests.swift` | Host build/deploy route; direct device or internally managed cloud tunnel. Darwin agents accept native macOS projects only; Linux containers on Macs are intentionally unsupported. |
| `wendy tour` | public | `WendyTourTests.swift` | Host-only CLI/config command. |
| `wendy utils open-browser` | public | `WendyUtilsOpenBrowserTests.swift` | Host-only CLI/config command. |
