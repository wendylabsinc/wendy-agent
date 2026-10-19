package interceptor

import "github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"

const (
	viewer   = trustedkeys.RoleViewer
	operator = trustedkeys.RoleOperator
	owner    = trustedkeys.RoleOwner
)

// BLEMethodPrefix names the BLE dispatcher's commands in the role table:
// "/wendy.agent.ble/<BluetoothCommand oneof field name>".
const BLEMethodPrefix = "/wendy.agent.ble/"

// methodRoles is the minimum role per method. A method that is not listed is
// denied to every caller (default deny); TestEveryAgentMethodHasARole fails
// on an unlisted agent method and the agent refuses to start with one.
//
// owner: key management, enrollment and unprovisioning.
// operator: apps/containers, builds, shell, tunnel, drivers, device settings,
// agent/OS update, data campaigns, robot control, live camera and microphone
// streams. Device settings sit here, not at owner, because shell already
// reaches them.
// viewer: reads — info, lists, status, logs, telemetry, audio levels.
var methodRoles = map[string]trustedkeys.Role{
	// v1 WendyAgentService
	"/wendy.agent.services.v1.WendyAgentService/RunContainer":                  operator,
	"/wendy.agent.services.v1.WendyAgentService/UpdateAgent":                   operator,
	"/wendy.agent.services.v1.WendyAgentService/GetAgentVersion":               viewer,
	"/wendy.agent.services.v1.WendyAgentService/ListWiFiNetworks":              viewer,
	"/wendy.agent.services.v1.WendyAgentService/ConnectToWiFi":                 operator,
	"/wendy.agent.services.v1.WendyAgentService/GetWiFiStatus":                 viewer,
	"/wendy.agent.services.v1.WendyAgentService/DisconnectWiFi":                operator,
	"/wendy.agent.services.v1.WendyAgentService/ListKnownWiFiNetworks":         viewer,
	"/wendy.agent.services.v1.WendyAgentService/SetWiFiNetworkPriority":        operator,
	"/wendy.agent.services.v1.WendyAgentService/ReorderKnownWiFiNetworks":      operator,
	"/wendy.agent.services.v1.WendyAgentService/ForgetWiFiNetwork":             operator,
	"/wendy.agent.services.v1.WendyAgentService/ListHardwareCapabilities":      viewer,
	"/wendy.agent.services.v1.WendyAgentService/ScanBluetoothPeripherals":      viewer,
	"/wendy.agent.services.v1.WendyAgentService/ConnectBluetoothPeripheral":    operator,
	"/wendy.agent.services.v1.WendyAgentService/DisconnectBluetoothPeripheral": operator,
	"/wendy.agent.services.v1.WendyAgentService/ForgetBluetoothPeripheral":     operator,
	"/wendy.agent.services.v1.WendyAgentService/UpdateOS":                      operator,
	"/wendy.agent.services.v1.WendyAgentService/DumpKernelLog":                 viewer,
	"/wendy.agent.services.v1.WendyAgentService/GetOSUpdateStatus":             viewer,
	"/wendy.agent.services.v1.WendyAgentService/SetHostname":                   operator,
	"/wendy.agent.services.v1.WendyAgentService/GetPowerMode":                  viewer,
	"/wendy.agent.services.v1.WendyAgentService/SetPowerMode":                  operator,

	// v1 WendyAudioService
	"/wendy.agent.services.v1.WendyAudioService/ListAudioDevices":      viewer,
	"/wendy.agent.services.v1.WendyAudioService/SetDefaultAudioDevice": operator,
	"/wendy.agent.services.v1.WendyAudioService/StreamAudioLevels":     viewer,
	"/wendy.agent.services.v1.WendyAudioService/StreamAudio":           operator,

	// v1 WendyContainerService
	"/wendy.agent.services.v1.WendyContainerService/ListLayers":                  viewer,
	"/wendy.agent.services.v1.WendyContainerService/WriteLayer":                  operator,
	"/wendy.agent.services.v1.WendyContainerService/CreateContainer":             operator,
	"/wendy.agent.services.v1.WendyContainerService/CreateContainerWithProgress": operator,
	"/wendy.agent.services.v1.WendyContainerService/RunContainer":                operator,
	"/wendy.agent.services.v1.WendyContainerService/StartContainer":              operator,
	"/wendy.agent.services.v1.WendyContainerService/AttachContainer":             operator,
	"/wendy.agent.services.v1.WendyContainerService/ExecContainer":               operator,
	"/wendy.agent.services.v1.WendyContainerService/StopContainer":               operator,
	"/wendy.agent.services.v1.WendyContainerService/DeleteContainer":             operator,
	"/wendy.agent.services.v1.WendyContainerService/ListContainers":              viewer,
	"/wendy.agent.services.v1.WendyContainerService/ListVolumes":                 viewer,
	"/wendy.agent.services.v1.WendyContainerService/RemoveVolume":                operator,
	"/wendy.agent.services.v1.WendyContainerService/ListContainerStats":          viewer,
	"/wendy.agent.services.v1.WendyContainerService/GetResourceStats":            viewer,
	"/wendy.agent.services.v1.WendyContainerService/GetContainerPorts":           viewer,
	"/wendy.agent.services.v1.WendyContainerService/StreamMCP":                   operator,
	"/wendy.agent.services.v1.WendyContainerService/QueryChunks":                 operator,
	"/wendy.agent.services.v1.WendyContainerService/WriteChunks":                 operator,
	"/wendy.agent.services.v1.WendyContainerService/QueryLayers":                 operator,
	"/wendy.agent.services.v1.WendyContainerService/PrepareImage":                operator,

	// v1 WendyProvisioningService
	"/wendy.agent.services.v1.WendyProvisioningService/StartProvisioning": owner,
	"/wendy.agent.services.v1.WendyProvisioningService/IsProvisioned":     viewer,
	"/wendy.agent.services.v1.WendyProvisioningService/Unprovision":       owner,

	// v1 WendyShellService
	"/wendy.agent.services.v1.WendyShellService/HostShell": operator,

	// v1 WendyTelemetryService
	"/wendy.agent.services.v1.WendyTelemetryService/StreamLogs":    viewer,
	"/wendy.agent.services.v1.WendyTelemetryService/StreamMetrics": viewer,
	"/wendy.agent.services.v1.WendyTelemetryService/StreamTraces":  viewer,

	// v1 WendyVideoService
	"/wendy.agent.services.v1.WendyVideoService/ListVideoDevices":      viewer,
	"/wendy.agent.services.v1.WendyVideoService/StreamVideo":           operator,
	"/wendy.agent.services.v1.WendyVideoService/SetCameraCredentials":  operator,
	"/wendy.agent.services.v1.WendyVideoService/ForgetCamera":          operator,
	"/wendy.agent.services.v1.WendyVideoService/RefreshCameras":        operator,
	"/wendy.agent.services.v1.WendyVideoService/TestCameraCredentials": operator,
	"/wendy.agent.services.v1.WendyVideoService/GetCameraControls":     viewer,
	"/wendy.agent.services.v1.WendyVideoService/SetCameraControls":     operator,
	"/wendy.agent.services.v1.WendyVideoService/ResetCameraControls":   operator,

	// v2 DataService
	"/wendy.agent.services.v2.DataService/Events":                     viewer,
	"/wendy.agent.services.v2.DataService/ExportRecording":            operator,
	"/wendy.agent.services.v2.DataService/AcknowledgeRecordingExport": operator,
	"/wendy.agent.services.v2.DataService/Sources":                    viewer,
	"/wendy.agent.services.v2.DataService/Start":                      operator,
	"/wendy.agent.services.v2.DataService/Stop":                       operator,
	"/wendy.agent.services.v2.DataService/Status":                     viewer,
	"/wendy.agent.services.v2.DataService/Episodes":                   viewer,
	"/wendy.agent.services.v2.DataService/Inspect":                    viewer,
	"/wendy.agent.services.v2.DataService/Download":                   viewer,
	"/wendy.agent.services.v2.DataService/CampaignDeploy":             operator,
	"/wendy.agent.services.v2.DataService/Campaigns":                  viewer,
	"/wendy.agent.services.v2.DataService/CampaignInspect":            viewer,
	"/wendy.agent.services.v2.DataService/CampaignTrigger":            operator,

	// v2 ROS2Service
	"/wendy.agent.services.v2.ROS2Service/ListNodes":                viewer,
	"/wendy.agent.services.v2.ROS2Service/ListTopics":               viewer,
	"/wendy.agent.services.v2.ROS2Service/GetTopicInfo":             viewer,
	"/wendy.agent.services.v2.ROS2Service/ListServices":             viewer,
	"/wendy.agent.services.v2.ROS2Service/ListParams":               viewer,
	"/wendy.agent.services.v2.ROS2Service/GetParam":                 viewer,
	"/wendy.agent.services.v2.ROS2Service/SetParam":                 operator,
	"/wendy.agent.services.v2.ROS2Service/CallService":              operator,
	"/wendy.agent.services.v2.ROS2Service/GetGraph":                 viewer,
	"/wendy.agent.services.v2.ROS2Service/Doctor":                   viewer,
	"/wendy.agent.services.v2.ROS2Service/EchoTopic":                viewer,
	"/wendy.agent.services.v2.ROS2Service/MonitorHz":                viewer,
	"/wendy.agent.services.v2.ROS2Service/RecordBag":                operator,
	"/wendy.agent.services.v2.ROS2Service/ListBags":                 viewer,
	"/wendy.agent.services.v2.ROS2Service/DownloadBag":              viewer,
	"/wendy.agent.services.v2.ROS2Service/Exec":                     operator,
	"/wendy.agent.services.v2.ROS2Service/ListActions":              viewer,
	"/wendy.agent.services.v2.ROS2Service/GetActionInfo":            viewer,
	"/wendy.agent.services.v2.ROS2Service/SendActionGoal":           operator,
	"/wendy.agent.services.v2.ROS2Service/ListLifecycleNodes":       viewer,
	"/wendy.agent.services.v2.ROS2Service/GetLifecycleState":        viewer,
	"/wendy.agent.services.v2.ROS2Service/ListLifecycleTransitions": viewer,
	"/wendy.agent.services.v2.ROS2Service/SetLifecycleState":        operator,
	"/wendy.agent.services.v2.ROS2Service/ListComponents":           viewer,
	"/wendy.agent.services.v2.ROS2Service/LoadComponent":            operator,
	"/wendy.agent.services.v2.ROS2Service/UnloadComponent":          operator,

	// v2 WendyAgentUpdateService
	"/wendy.agent.services.v2.WendyAgentUpdateService/UpdateAgent": operator,

	// v2 WendyAudioService
	"/wendy.agent.services.v2.WendyAudioService/ListAudioDevices":      viewer,
	"/wendy.agent.services.v2.WendyAudioService/SetDefaultAudioDevice": operator,
	"/wendy.agent.services.v2.WendyAudioService/SetAudioVolume":        operator,
	"/wendy.agent.services.v2.WendyAudioService/StreamAudioLevels":     viewer,
	"/wendy.agent.services.v2.WendyAudioService/StreamAudio":           operator,

	// v2 WendyBluetoothService
	"/wendy.agent.services.v2.WendyBluetoothService/ScanBluetoothPeripherals":      viewer,
	"/wendy.agent.services.v2.WendyBluetoothService/ConnectBluetoothPeripheral":    operator,
	"/wendy.agent.services.v2.WendyBluetoothService/DisconnectBluetoothPeripheral": operator,
	"/wendy.agent.services.v2.WendyBluetoothService/ForgetBluetoothPeripheral":     operator,

	// v2 WendyBuildService
	"/wendy.agent.services.v2.WendyBuildService/GetBuildCapabilities": viewer,
	"/wendy.agent.services.v2.WendyBuildService/BuildImage":           operator,
	"/wendy.agent.services.v2.WendyBuildService/SetBuildHostEnabled":  operator,

	// v2 WendyContainerService
	"/wendy.agent.services.v2.WendyContainerService/StartContainer":     operator,
	"/wendy.agent.services.v2.WendyContainerService/AttachContainer":    operator,
	"/wendy.agent.services.v2.WendyContainerService/StopContainer":      operator,
	"/wendy.agent.services.v2.WendyContainerService/DeleteContainer":    operator,
	"/wendy.agent.services.v2.WendyContainerService/ListContainers":     viewer,
	"/wendy.agent.services.v2.WendyContainerService/ListVolumes":        viewer,
	"/wendy.agent.services.v2.WendyContainerService/RemoveVolume":       operator,
	"/wendy.agent.services.v2.WendyContainerService/ListContainerStats": viewer,
	"/wendy.agent.services.v2.WendyContainerService/PruneCache":         operator,

	// v2 WendyDeviceInfoService
	"/wendy.agent.services.v2.WendyDeviceInfoService/GetDeviceInfo":            viewer,
	"/wendy.agent.services.v2.WendyDeviceInfoService/ListHardwareCapabilities": viewer,

	// v2 WendyDriverService
	"/wendy.agent.services.v2.WendyDriverService/InstallDriver": operator,
	"/wendy.agent.services.v2.WendyDriverService/RemoveDriver":  operator,
	"/wendy.agent.services.v2.WendyDriverService/ListDrivers":   viewer,

	// v2 WendyMeshService — peer devices only in practice: the handler
	// requires an asset certificate of the device's tenant.
	"/wendy.agent.services.v2.WendyMeshService/MeshDial": operator,

	// v2 WendyOSUpdateService
	"/wendy.agent.services.v2.WendyOSUpdateService/UpdateOS":          operator,
	"/wendy.agent.services.v2.WendyOSUpdateService/GetOSUpdateStatus": viewer,

	// v2 WendyProvisioningService
	"/wendy.agent.services.v2.WendyProvisioningService/StartProvisioning":     owner,
	"/wendy.agent.services.v2.WendyProvisioningService/StartACMEProvisioning": owner,
	"/wendy.agent.services.v2.WendyProvisioningService/IsProvisioned":         viewer,
	"/wendy.agent.services.v2.WendyProvisioningService/Unprovision":           owner,
	"/wendy.agent.services.v2.WendyProvisioningService/RevokeACMECertificate": owner,
	"/wendy.agent.services.v2.WendyProvisioningService/CheckACMERevocation":   viewer,

	// v2 WendySensorPairingService
	"/wendy.agent.services.v2.WendySensorPairingService/AddSensorPairing":    operator,
	"/wendy.agent.services.v2.WendySensorPairingService/RemoveSensorPairing": operator,
	"/wendy.agent.services.v2.WendySensorPairingService/ListSensorPairings":  viewer,

	// v2 WendyTelemetryService
	"/wendy.agent.services.v2.WendyTelemetryService/StreamLogs":    viewer,
	"/wendy.agent.services.v2.WendyTelemetryService/StreamMetrics": viewer,
	"/wendy.agent.services.v2.WendyTelemetryService/StreamTraces":  viewer,

	// v2 WendyTimeSyncService
	"/wendy.agent.services.v2.WendyTimeSyncService/GetClock":  viewer,
	"/wendy.agent.services.v2.WendyTimeSyncService/SyncClock": operator,

	// v2 WendyTrustedKeysService — see ownerKeyMethods.
	"/wendy.agent.services.v2.WendyTrustedKeysService/AddTrustedKey":    owner,
	"/wendy.agent.services.v2.WendyTrustedKeysService/ListTrustedKeys":  owner,
	"/wendy.agent.services.v2.WendyTrustedKeysService/RemoveTrustedKey": owner,

	// v2 WendyTunnelService
	"/wendy.agent.services.v2.WendyTunnelService/Tunnel":         operator,
	"/wendy.agent.services.v2.WendyTunnelService/DatagramTunnel": operator,

	// v2 WendyUnitreeRobotService
	"/wendy.agent.services.v2.WendyUnitreeRobotService/Command": operator,
	"/wendy.agent.services.v2.WendyUnitreeRobotService/Stream":  viewer,

	// v2 WendyUnitreeService
	"/wendy.agent.services.v2.WendyUnitreeService/DiscoverUnitreeRobots": viewer,
	"/wendy.agent.services.v2.WendyUnitreeService/ListUnitreePairings":   viewer,
	"/wendy.agent.services.v2.WendyUnitreeService/PairUnitree":           operator,
	"/wendy.agent.services.v2.WendyUnitreeService/ForgetUnitree":         operator,

	// v2 WendyWiFiService
	"/wendy.agent.services.v2.WendyWiFiService/ListWiFiNetworks":         viewer,
	"/wendy.agent.services.v2.WendyWiFiService/ConnectToWiFi":            operator,
	"/wendy.agent.services.v2.WendyWiFiService/GetWiFiStatus":            viewer,
	"/wendy.agent.services.v2.WendyWiFiService/DisconnectWiFi":           operator,
	"/wendy.agent.services.v2.WendyWiFiService/ListKnownWiFiNetworks":    viewer,
	"/wendy.agent.services.v2.WendyWiFiService/SetWiFiNetworkPriority":   operator,
	"/wendy.agent.services.v2.WendyWiFiService/ReorderKnownWiFiNetworks": operator,
	"/wendy.agent.services.v2.WendyWiFiService/ForgetWiFiNetwork":        operator,

	// BLE dispatcher commands (BluetoothCommand oneof fields).
	BLEMethodPrefix + "wifi_list":            viewer,
	BLEMethodPrefix + "wifi_connect":         operator,
	BLEMethodPrefix + "wifi_status":          viewer,
	BLEMethodPrefix + "wifi_disconnect":      operator,
	BLEMethodPrefix + "wifi_known_list":      viewer,
	BLEMethodPrefix + "wifi_set_priority":    operator,
	BLEMethodPrefix + "wifi_reorder":         operator,
	BLEMethodPrefix + "wifi_forget":          operator,
	BLEMethodPrefix + "apps_list":            viewer,
	BLEMethodPrefix + "apps_stop":            operator,
	BLEMethodPrefix + "apps_remove":          operator,
	BLEMethodPrefix + "agent_version":        viewer,
	BLEMethodPrefix + "hardware_list":        viewer,
	BLEMethodPrefix + "bluetooth_list":       viewer,
	BLEMethodPrefix + "bluetooth_connect":    operator,
	BLEMethodPrefix + "bluetooth_disconnect": operator,
	BLEMethodPrefix + "bluetooth_forget":     operator,
}

// ownerKeyMethods require an owner authenticated by a trusted key. No other
// principal qualifies, whatever its role: a tenant certificate never grants
// local-owner rights, and the pre-enroll listener cannot plant a key.
var ownerKeyMethods = map[string]bool{
	"/wendy.agent.services.v2.WendyTrustedKeysService/AddTrustedKey":    true,
	"/wendy.agent.services.v2.WendyTrustedKeysService/ListTrustedKeys":  true,
	"/wendy.agent.services.v2.WendyTrustedKeysService/RemoveTrustedKey": true,
}

// peerMethods are the only calls another device's certificate may make: the
// mesh data plane and a build host delivering an image by chunks.
var peerMethods = map[string]bool{
	"/wendy.agent.services.v2.WendyMeshService/MeshDial":          true,
	"/wendy.agent.services.v1.WendyContainerService/QueryChunks":  true,
	"/wendy.agent.services.v1.WendyContainerService/QueryLayers":  true,
	"/wendy.agent.services.v1.WendyContainerService/WriteChunks":  true,
	"/wendy.agent.services.v1.WendyContainerService/PrepareImage": true,
}

// tenantUnprovisionMethods stay open to tenant operators (see permitted).
var tenantUnprovisionMethods = map[string]bool{
	"/wendy.agent.services.v1.WendyProvisioningService/Unprovision": true,
	"/wendy.agent.services.v2.WendyProvisioningService/Unprovision": true,
}
