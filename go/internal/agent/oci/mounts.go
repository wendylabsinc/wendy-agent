package oci

import (
	"fmt"
	"path"
	"strings"
)

// containerdRuntimeDirs are containerd's host runtime directories — they hold
// the control socket (containerd.sock) and runtime state. No container may
// mount them. Both /run/containerd and its conventional /var/run alias are
// listed because /var/run is typically a symlink to /run, and ValidateMounts
// matches lexically (it does not resolve symlinks).
var containerdRuntimeDirs = []string{"/run/containerd", "/var/run/containerd"}

// ValidateMounts is a defense-in-depth backstop for WDY-1102: it rejects a spec
// whose bind-mount sources resolve into containerd's runtime directory. A
// container with write access to the containerd socket can spawn privileged
// containers and modify host namespaces — a full host escape. The spec builder
// does not intentionally mount these paths; this guard ensures no future code
// path or crafted source can either. It must be called on the fully assembled
// spec, immediately before the spec is handed to the runtime.
func ValidateMounts(spec *Spec) error {
	for _, m := range spec.Mounts {
		// path.Clean resolves any ".." so a crafted source cannot slip past.
		clean := path.Clean(m.Source)
		for _, dir := range containerdRuntimeDirs {
			if clean == dir || strings.HasPrefix(clean, dir+"/") {
				return fmt.Errorf("security: refusing container mount with source %q: %s is off-limits to containers (WDY-1102)", m.Source, dir)
			}
		}
	}
	return nil
}
