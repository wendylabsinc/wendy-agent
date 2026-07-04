package oci

import "testing"

// TestValidateMountsRejectsContainerdRuntimeDir is the regression test for
// WDY-1102: no container bind mount may resolve into containerd's runtime
// directory. Writable access to the containerd control socket lets a container
// spawn privileged containers and escape to the host, so the spec builder must
// reject such a mount as a defense-in-depth backstop.
func TestValidateMountsRejectsContainerdRuntimeDir(t *testing.T) {
	spec := &Spec{Mounts: []Mount{
		{Destination: "/run/containerd", Source: "/run/containerd", Type: "bind", Options: []string{"rbind", "rw"}},
	}}
	if err := ValidateMounts(spec); err == nil {
		t.Fatal("expected error for a bind mount of /run/containerd")
	}
}

func TestValidateMountsRejectsContainerdSocketSubpath(t *testing.T) {
	spec := &Spec{Mounts: []Mount{
		{Destination: "/x", Source: "/run/containerd/containerd.sock", Type: "bind", Options: []string{"rbind", "rw"}},
	}}
	if err := ValidateMounts(spec); err == nil {
		t.Fatal("expected error for a bind mount of the containerd socket")
	}
}

// TestValidateMountsRejectsUncleanTraversal ensures the check resolves
// ".."-bearing sources before matching, so a crafted source cannot slip past.
// /var/lib/wendy/volumes + four ".." resolves to "/", then /run/containerd/...
func TestValidateMountsRejectsUncleanTraversal(t *testing.T) {
	spec := &Spec{Mounts: []Mount{
		{Destination: "/x", Source: "/var/lib/wendy/volumes/../../../../run/containerd/containerd.sock", Type: "bind"},
	}}
	if err := ValidateMounts(spec); err == nil {
		t.Fatal("expected error for a traversal that resolves into /run/containerd")
	}
}

// TestValidateMountsRejectsVarRunAlias guards the conventional /var/run -> /run
// symlink alias, which would otherwise reach the same socket lexically.
func TestValidateMountsRejectsVarRunAlias(t *testing.T) {
	spec := &Spec{Mounts: []Mount{
		{Destination: "/x", Source: "/var/run/containerd/containerd.sock", Type: "bind"},
	}}
	if err := ValidateMounts(spec); err == nil {
		t.Fatal("expected error for the /var/run/containerd alias")
	}
}

// TestValidateMountsAllowsNormalMounts ensures the guard does not reject
// legitimate mounts, including a sibling directory whose path merely shares the
// /run/containerd prefix.
func TestValidateMountsAllowsNormalMounts(t *testing.T) {
	spec := &Spec{Mounts: []Mount{
		{Destination: "/proc", Source: "proc", Type: "proc"},
		{Destination: "/dev/shm", Source: "tmpfs", Type: "tmpfs"},
		{Destination: "/data", Source: "/var/lib/wendy/volumes/data", Type: "bind", Options: []string{"rbind"}},
		{Destination: "/c", Source: "/run/containerd-helper", Type: "bind", Options: []string{"rbind"}},
	}}
	if err := ValidateMounts(spec); err != nil {
		t.Fatalf("expected no error for legitimate mounts; got %v", err)
	}
}
