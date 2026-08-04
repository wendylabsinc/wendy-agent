//go:build windows

package commands

import "golang.org/x/sys/windows/registry"

func hostOSVersion() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()

	productName, _, _ := key.GetStringValue("ProductName")
	displayVersion, _, _ := key.GetStringValue("DisplayVersion")
	if displayVersion == "" {
		displayVersion, _, _ = key.GetStringValue("ReleaseId")
	}
	buildNumber, _, _ := key.GetStringValue("CurrentBuildNumber")
	return formatWindowsOSVersion(productName, displayVersion, buildNumber)
}
