package containerd

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// userHz is the kernel USER_HZ (clock ticks per second) used to convert the
// /proc/<pid>/stat starttime field into seconds. It is 100 on essentially every
// mainstream Linux build (CONFIG_HZ aside, USER_HZ is fixed at 100), and we
// build the agent with CGO disabled for cross targets so sysconf(_SC_CLK_TCK)
// isn't readily available. 100 is the correct, stable value here.
const userHz = 100

// processStartTime returns the wall-clock start time of the process with the
// given PID, derived from /proc. It returns ok=false when the value can't be
// determined (process gone, malformed procfs, PID 0). Reading the live process
// means the value survives agent restarts — it reflects the actual current run
// of the container task, and resets only when the task itself restarts.
func processStartTime(pid uint32) (time.Time, bool) {
	if pid == 0 {
		return time.Time{}, false
	}

	btime, ok := bootTime()
	if !ok {
		return time.Time{}, false
	}

	data, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	ticks, ok := parseStarttimeTicks(string(data))
	if !ok {
		return time.Time{}, false
	}
	return btime.Add(time.Duration(ticks) * time.Second / userHz), true
}

// parseStarttimeTicks extracts the starttime field (field 22 of
// /proc/<pid>/stat) in clock ticks. The format is "pid (comm) state ppid ...";
// comm can contain spaces and parentheses, so we anchor on the LAST ')' and
// parse the fields after it. The first field after ')' is field 3 (state), so
// starttime is the 20th post-')' field → index 19.
func parseStarttimeTicks(stat string) (int64, bool) {
	close := strings.LastIndexByte(stat, ')')
	if close < 0 || close+1 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(stat[close+1:])
	const starttimeIdx = 19
	if len(fields) <= starttimeIdx {
		return 0, false
	}
	ticks, err := strconv.ParseInt(fields[starttimeIdx], 10, 64)
	if err != nil || ticks < 0 {
		return 0, false
	}
	return ticks, true
}

// bootTime reads the system boot time (the "btime" line of /proc/stat) and
// returns it as an absolute time.
func bootTime() (time.Time, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	secs, ok := parseBtime(string(data))
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// parseBtime extracts the "btime" (boot time, epoch seconds) line of /proc/stat.
func parseBtime(procStat string) (int64, bool) {
	for _, line := range strings.Split(procStat, "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(line[len("btime "):]), 10, 64)
		if err != nil {
			return 0, false
		}
		return secs, true
	}
	return 0, false
}
