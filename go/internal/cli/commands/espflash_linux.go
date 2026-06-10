//go:build linux

package commands

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func serialPortGroup(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	group, err := user.LookupGroupId(strconv.Itoa(int(stat.Gid)))
	if err != nil {
		return strconv.Itoa(int(stat.Gid))
	}
	return group.Name
}
