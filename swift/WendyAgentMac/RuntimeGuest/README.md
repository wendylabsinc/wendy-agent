# WendyAgentMac local-runtime guest

This is the immutable Linux initramfs used by the macOS Wendy-managed VM. It
runs three long-lived services:

- containerd, with its content and snapshot roots on the persistent data disk;
- BuildKit, using the containerd worker and the `wendy` namespace;
- a small vsock proxy exposing BuildKit on port 6237 and containerd on 6238.

The host maps those vsock ports to user-owned Unix sockets. Build contexts flow
through BuildKit's session protocol and image outputs use
`store=true,unpack=true`; neither the image nor its snapshots are copied back to
macOS for a local run.

`build.sh` builds the architecture-specific initramfs into `../Resources/runtime`
using a reachable BuildKit daemon. The kernel and its modules come from the
same Alpine `linux-virt` package transaction: the modules remain in the
initramfs and the kernel is copied beside it for Virtualization.framework to
boot directly. APK repository signatures cover that transaction, and release
packaging can additionally pin the resulting artifact digests.
