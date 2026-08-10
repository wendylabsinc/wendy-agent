//go:build !windows

package commands

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// psListTimeout bounds the process listing. It is only ever reached on the
// already-degraded path, and a hung `ps` must not become a second way to stall
// a build.
const psListTimeout = 5 * time.Second

// psOwnProcesses lists this user's processes. `ps -x` (without -A) is the
// portable spelling for "processes owned by me" on both macOS and Linux, which
// keeps the reaper from ever considering another user's processes.
func psOwnProcesses() ([]procInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psListTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ps", "-xo", "pid=,ppid=,pgid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var procs []procInfo
	for line := range strings.Lines(string(out)) {
		// pid, ppid and pgid are fixed-width numeric columns; the command is
		// the rest of the line and may contain spaces, so split only 4 ways.
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		pgid, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		command := strings.TrimSpace(strings.Join(fields[3:], " "))
		procs = append(procs, procInfo{PID: pid, PPID: ppid, PGID: pgid, Command: command})
	}
	return procs, nil
}

// killProcessGroupByPGID SIGKILLs a whole process group. The group is the unit
// that matters: a stranded `docker buildx rm` is two processes (the CLI and the
// plugin it exec'd) and killing only one leaves the lock held.
func killProcessGroupByPGID(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
