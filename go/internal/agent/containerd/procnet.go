package containerd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// maxProcNetBytes caps how much of a /proc/<pid>/net/* table is read per poll. On
// a host with tens of thousands of sockets these virtual files can grow large;
// the cap bounds agent memory at the cost of truncating the socket list on
// pathological hosts.
const maxProcNetBytes = 8 << 20 // 8 MiB

// maxFDsPerPID caps how many /proc/<pid>/fd entries are scanned per process so a
// process with a runaway number of open descriptors cannot stall a poll.
const maxFDsPerPID = 1 << 16 // 65536

// listeningPort is a single listening/bound socket parsed from /proc/<pid>/net/*.
type listeningPort struct {
	protocol string
	port     uint32
	address  string
	inode    string // socket inode, used to attribute the socket to a process
}

// parseProcNet parses the contents of a /proc/<pid>/net/{tcp,tcp6,udp,udp6}
// file and returns the listening (TCP) or bound (UDP) sockets. protocol is the
// label to stamp on each entry ("tcp", "tcp6", "udp", "udp6"); ipv6 selects the
// address-decoding width.
//
// Selection rule: TCP sockets are included only in the LISTEN state (st 0A);
// UDP sockets are included when they have no connected peer (remote port 0).
// An unconnected UDP socket on an ephemeral local port is almost always a
// client (e.g. an outbound sendto), not a service, so UDP sockets bound at or
// above udpEphemeralFloor are skipped. Pass 0 to keep every bound UDP socket.
func parseProcNet(data []byte, protocol string, ipv6 bool, udpEphemeralFloor uint32) []listeningPort {
	isTCP := strings.HasPrefix(protocol, "tcp")
	var out []listeningPort

	sc := bufio.NewScanner(bytes.NewReader(data))
	first := true
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// Header line ("sl local_address ...") and short lines are skipped.
		if first {
			first = false
			continue
		}
		if len(fields) < 4 {
			continue
		}
		local := fields[1]  // HEXADDR:HEXPORT
		remote := fields[2] // HEXADDR:HEXPORT
		st := fields[3]

		addrHex, port, ok := splitHexAddr(local)
		if !ok {
			continue
		}

		if isTCP {
			if st != "0A" { // TCP_LISTEN
				continue
			}
		} else {
			// UDP: only unconnected (bound) sockets, excluding ephemeral
			// client ports.
			if _, rport, ok := splitHexAddr(remote); !ok || rport != 0 {
				continue
			}
			if udpEphemeralFloor > 0 && port >= udpEphemeralFloor {
				continue
			}
		}

		addr := decodeHexAddr(addrHex, ipv6)
		if addr == "" {
			continue
		}
		inode := ""
		if len(fields) >= 10 {
			inode = fields[9]
		}
		out = append(out, listeningPort{protocol: protocol, port: port, address: addr, inode: inode})
	}
	return out
}

// splitHexAddr splits a "HEXADDR:HEXPORT" token into the address hex and the
// decoded port.
func splitHexAddr(tok string) (addrHex string, port uint32, ok bool) {
	i := strings.LastIndex(tok, ":")
	if i < 0 {
		return "", 0, false
	}
	p, err := strconv.ParseUint(tok[i+1:], 16, 32)
	if err != nil {
		return "", 0, false
	}
	return tok[:i], uint32(p), true
}

// decodeHexAddr decodes the hex local address from /proc/net/* into a printable
// IP. IPv4 is 8 hex chars in little-endian byte order; IPv6 is 32 hex chars
// stored as four little-endian 32-bit words.
func decodeHexAddr(h string, ipv6 bool) string {
	if !ipv6 {
		if len(h) != 8 {
			return ""
		}
		b := make([]byte, 4)
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
			if err != nil {
				return ""
			}
			b[3-i] = byte(v) // little-endian → reverse
		}
		return net.IPv4(b[0], b[1], b[2], b[3]).String()
	}

	if len(h) != 32 {
		return ""
	}
	ip := make([]byte, 16)
	// Four 32-bit words, each little-endian.
	for w := 0; w < 4; w++ {
		word := h[8*w : 8*w+8]
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseUint(word[2*i:2*i+2], 16, 8)
			if err != nil {
				return ""
			}
			ip[w*4+(3-i)] = byte(v)
		}
	}
	return net.IP(ip).String()
}

// GetListeningPorts returns the listening TCP and bound UDP sockets owned by the
// app's own processes. WendyOS single-container apps share the host network
// namespace, so reading /proc/<pid>/net/* alone would surface every listener on
// the device (sshd, systemd-resolved, the agent's OTLP collector, NFS, …). To
// show only the app's ports, each socket is attributed to the process that holds
// it open (via /proc/<pid>/fd socket inodes) and filtered to the container's
// process tree. Results are de-duplicated and sorted by port.
func (c *Client) GetListeningPorts(ctx context.Context, appName string) ([]*agentpb.PortEntry, error) {
	ids, err := c.ContainerIDsForApp(ctx, appName)
	if err != nil || len(ids) == 0 {
		// Fall back to treating appName as a bare container ID.
		ids = []string{appName}
	}

	nsCtx := c.withNamespace(ctx)
	udpFloor := udpEphemeralFloor()
	seen := make(map[string]bool)
	var out []*agentpb.PortEntry

	protocols := []struct {
		name string
		ipv6 bool
	}{
		{"tcp", false}, {"tcp6", true}, {"udp", false}, {"udp6", true},
	}

	for _, id := range ids {
		container, err := c.client.LoadContainer(nsCtx, id)
		if err != nil {
			continue
		}
		task, err := container.Task(nsCtx, nil)
		if err != nil {
			continue
		}

		pids := containerPIDs(nsCtx, task)
		if len(pids) == 0 {
			continue
		}
		ownInodes := appSocketInodes(pids)

		// All processes in a container share one network namespace, so the
		// socket tables are identical regardless of which PID we read.
		netPID := pids[0]
		for _, p := range protocols {
			data, err := readBounded(fmt.Sprintf("/proc/%d/net/%s", netPID, p.name), maxProcNetBytes)
			if err != nil {
				continue
			}
			for _, lp := range parseProcNet(data, p.name, p.ipv6, udpFloor) {
				// Attribute the socket to the app: skip sockets the app's
				// processes do not hold open (host services, the agent, etc.).
				if _, owned := ownInodes[lp.inode]; !owned {
					continue
				}
				key := fmt.Sprintf("%s|%s|%d", lp.protocol, lp.address, lp.port)
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, &agentpb.PortEntry{
					Protocol: lp.protocol,
					Port:     lp.port,
					Address:  lp.address,
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Protocol < out[j].Protocol
	})
	return out, nil
}

// containerPIDs returns every PID in the container's process tree, falling back
// to the task's main PID if the per-process listing is unavailable.
func containerPIDs(ctx context.Context, task containerd.Task) []uint32 {
	if procs, err := task.Pids(ctx); err == nil && len(procs) > 0 {
		pids := make([]uint32, 0, len(procs))
		for _, p := range procs {
			pids = append(pids, p.Pid)
		}
		return pids
	}
	if pid := task.Pid(); pid != 0 {
		return []uint32{pid}
	}
	return nil
}

// appSocketInodes scans /proc/<pid>/fd for the given PIDs and returns the set of
// socket inodes those processes hold open (fd symlinks of the form
// "socket:[12345]").
func appSocketInodes(pids []uint32) map[string]struct{} {
	inodes := make(map[string]struct{})
	for _, pid := range pids {
		// task.Pids() reports host-namespace PIDs, so a container process is
		// never the host init (1) or the kernel (0). Skip those defensively in
		// case containerd returns an unexpected value, to avoid attributing the
		// host's own sockets to the app.
		if pid <= 1 {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for i, e := range entries {
			if i >= maxFDsPerPID {
				break
			}
			link, err := os.Readlink(fmt.Sprintf("%s/%s", fdDir, e.Name()))
			if err != nil {
				continue
			}
			if inode, ok := socketInode(link); ok {
				inodes[inode] = struct{}{}
			}
		}
	}
	return inodes
}

// readBounded reads up to max bytes from path. /proc virtual files report a zero
// size, so a plain ReadFile would buffer the entire (potentially huge) table;
// the LimitReader bounds the allocation.
func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// socketInode extracts the inode from an fd symlink target like "socket:[12345]".
func socketInode(link string) (string, bool) {
	const prefix = "socket:["
	if !strings.HasPrefix(link, prefix) || !strings.HasSuffix(link, "]") {
		return "", false
	}
	return link[len(prefix) : len(link)-1], true
}

// udpEphemeralFloor returns the lowest ephemeral (client) port, read from
// /proc/sys/net/ipv4/ip_local_port_range. UDP sockets bound at or above this
// are treated as clients and hidden. Falls back to the common Linux default.
func udpEphemeralFloor() uint32 {
	const def = 32768
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return def
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return def
	}
	v, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil || v == 0 {
		return def
	}
	return uint32(v)
}
