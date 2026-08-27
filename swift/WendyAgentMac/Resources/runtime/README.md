# Wendy runtime artifacts

`WendyAgentMac/RuntimeGuest/build.sh` places the generated, architecture-specific initramfs
here. Release packaging also supplies its checksum-pinned matching kernel. Xcode
copies this directory into `WendyAgentMac.app/Contents/Resources/runtime`.
