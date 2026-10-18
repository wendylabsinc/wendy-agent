//go:build unix

package trustedkeys

import (
	"fmt"
	"os"
	"syscall"
)

// checkOwner refuses a file owned by anyone but the agent's own user: that
// user could grant itself owner.
func checkOwner(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if euid := os.Geteuid(); int(st.Uid) != euid {
		return fmt.Errorf("owned by uid %d, not the agent's uid %d; refusing to trust it", st.Uid, euid)
	}
	return nil
}
