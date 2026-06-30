# wendy device mount

Mounts a WendyOS persistent volume as a local network drive.

- macOS/Linux: userspace NFSv3 on 127.0.0.1, auto-mounted with `mount -t nfs`.
- Windows: WebDAV on 127.0.0.1, mapped with `net use`.

The device runs no file server; all I/O flows over the existing agent gRPC
channel (mTLS on LAN, or the cloud tunnel), via `WendyVolumeFsService`.

## Manual verification (real OS mount)

1. `wendy device mount <volume>`  (read-write by default; add `--read-only` to opt out)
2. Open the printed mountpoint (`~/Wendy/<device>/<volume>` on macOS/Linux, `W:` on Windows).
3. Copy a file in; confirm it appears on the device under
   `/var/lib/wendy/volumes/<volume>`.
4. Ctrl-C; confirm the mount is gone (`mount | grep <volume>` empty).

## Notes

- macOS may prompt to install command-line NFS on first use; it is built in.
- Windows requires the WebClient service running for WebDAV drive mapping.
  **Note:** the Windows `wendy` binary currently does not build due to a
  pre-existing unrelated issue (`device_attach.go` uses `syscall.SIGWINCH`,
  which is unavailable on Windows). WebDAV mounting is implemented and
  unit-tested, but is not yet shippable as a Windows binary until that issue
  is resolved.
- Mounting a volume in use by a running app is allowed but warns: concurrent
  writes can corrupt app data.
