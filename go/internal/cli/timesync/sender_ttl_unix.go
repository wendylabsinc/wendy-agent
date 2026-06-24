//go:build linux || darwin

package clitimesync

import "syscall"

func setMulticastTTL(fd uintptr, ttl int) {
	syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, ttl) //nolint:errcheck
}
