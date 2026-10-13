package rtps

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PeripheralsConfigFile overrides automatic peripheral interface detection. It
// lives in the agent's config directory (/etc/wendy-agent by default, or
// $WENDY_CONFIG_PATH), next to ros2-battery.json, e.g.
//
//	{"interfaces": ["eth1"]}
//
// An empty list means the device has no peripheral interfaces.
const PeripheralsConfigFile = "ros2-peripherals.json"

// Kernel interface names: at most 15 bytes. The names end up in CycloneDDS XML,
// so anything else is rejected rather than escaped.
var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

// AgentConfigDir is the agent's config directory.
func AgentConfigDir() string {
	if dir := os.Getenv("WENDY_CONFIG_PATH"); dir != "" {
		return dir
	}
	return "/etc/wendy-agent"
}

// PeripheralInterfaces returns the interfaces that lead to hardware attached to
// the device rather than to the network: the override in configDir when
// present, otherwise every eligible wired interface (see HostInterfaces) that
// does not carry a default route. A robot or sensor on its own cable has no
// default route; the device's uplink does. When the routes cannot be read, no
// interface qualifies, so peripheral discovery never widens to an uplink.
func PeripheralInterfaces(configDir string) ([]string, error) {
	if names, ok, err := peripheralOverride(configDir); ok || err != nil {
		return names, err
	}
	wired, err := HostInterfaces()
	if err != nil {
		return nil, err
	}
	uplinks, err := defaultRouteInterfaces("/proc/net/route", "/proc/net/ipv6_route")
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, name := range wired {
		if !uplinks[name] && interfaceNamePattern.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func peripheralOverride(configDir string) ([]string, bool, error) {
	data, err := os.ReadFile(filepath.Join(configDir, PeripheralsConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("reading %s: %w", PeripheralsConfigFile, err)
	}
	var file struct {
		Interfaces *[]string `json:"interfaces"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, true, fmt.Errorf("parsing %s: %w", PeripheralsConfigFile, err)
	}
	if file.Interfaces == nil {
		return nil, true, fmt.Errorf("%s: missing \"interfaces\"", PeripheralsConfigFile)
	}
	for _, name := range *file.Interfaces {
		if !interfaceNamePattern.MatchString(name) {
			return nil, true, fmt.Errorf("%s: invalid interface name %q", PeripheralsConfigFile, name)
		}
	}
	return *file.Interfaces, true, nil
}

// defaultRouteInterfaces reads the IPv4 and IPv6 routing tables and returns the
// interfaces that carry a default route. A missing IPv6 table is fine; a
// missing IPv4 table is an error.
func defaultRouteInterfaces(ipv4Path, ipv6Path string) (map[string]bool, error) {
	uplinks := map[string]bool{}
	ipv4, err := os.Open(ipv4Path)
	if err != nil {
		return nil, err
	}
	defer ipv4.Close()
	scanner := bufio.NewScanner(ipv4)
	for scanner.Scan() {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 8 && fields[1] == "00000000" && fields[7] == "00000000" {
			uplinks[fields[0]] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if ipv6, err := os.Open(ipv6Path); err == nil {
		defer ipv6.Close()
		scanner := bufio.NewScanner(ipv6)
		for scanner.Scan() {
			// Destination PrefixLen Source SourcePrefixLen NextHop Metric RefCnt Use Flags Iface
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 10 && fields[0] == strings.Repeat("0", 32) && fields[1] == "00" && fields[9] != "lo" {
				uplinks[fields[9]] = true
			}
		}
	}
	return uplinks, nil
}
