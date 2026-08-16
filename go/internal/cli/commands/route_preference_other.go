//go:build !darwin && !linux && !windows

package commands

func networkInterfaceRoutePreference(string) routePreference { return routeUnknown }
