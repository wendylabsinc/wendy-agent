# PR #1827 Go2 hardware validation

- Original PR: https://github.com/wendylabsinc/WendyOS/pull/1827
- Original head tested: `1a3b59df644b4b908f05829e05dd91dd7acdb9b3`
- Device: Woof, an ARM64 Jetson Orin companion connected to a Unitree Go2
- Safety boundary: perception only; no locomotion, actuator, DDS control, robot reset, or robot reboot commands

## Original result

PR #1827 discovered `/frontvideostream` as camera 128 and created
`/dev/video128` after the compatible kernel module was loaded, but two
unattended ten-second `camera view --id 128 --stdout` captures produced zero
bytes. The agent reported `video360p: cdr: payload too short`.

Passing CI, camera discovery, and loopback-node creation were therefore not
evidence that the advertised Go2 path worked end to end.

## Issues and required tweaks

### 1. The tested companion image did not ship v4l2loopback

`modinfo v4l2loopback` failed and neither `/dev/v4l2loopback` nor a video node
was present. ROS 2 cameras use a loopback node for both CLI viewing and
container access, so discovery alone could not produce a stream.

### 2. Ubuntu 22.04's module is ABI-incompatible

Ubuntu's `v4l2loopback-dkms` 0.12.7 package built and loaded against the active
Tegra kernel, but did not provide the 0.15.x dynamic control interface the
agent uses. Testing required v4l2loopback 0.15.4 at the commit already pinned
by the WendyOS recipe. Installing the distro package is not a valid workaround.

### 3. The missing-module message was transport-inaccurate

The shared error said that `camera view` still worked. That is true for direct
IP-camera streaming, but false for ROS 2 cameras because their CLI path also
opens the loopback node. The error and documentation now state the compatible
module requirement without making the IP-only promise globally.

### 4. A failed module check was cached for the agent lifetime

`Loopback.Available` used `sync.Once`, so loading a compatible module after the
first failure still required an agent restart. Failed detection is now
retryable while successful detection remains cached.

### 5. The live Go2 layout and codec differed from the decoder

A one-shot bounded payload diagnostic captured this sanitized prefix:

```text
payload_len=2728
head_hex=000100019823e1030000000068010000930a00000000000141e0018003027fdb
```

It decodes as little-endian CDR, an eight-byte timestamp, resolution `360`, a
2707-byte sequence, and an Annex-B H.264 NAL beginning `00 00 01 41`. The PR
treated `360` as the byte count of a `video720p` JPEG field, then tried to read
another sequence from the middle of the H.264 access unit. Correcting offsets
alone was insufficient because the writer also advertised MJPEG unconditionally.

The fix recognizes both this live H.264 layout and the SDK-declared three-JPEG
layout, carries the codec with each decoded frame, and configures the loopback
writer as H.264 or MJPEG without transcoding.

### 6. The Go2 unit fixture did not represent hardware

The original test generated a JPEG inside the SDK-declared struct. It could not
catch the resolution-plus-H.264 layout. A regression fixture now mirrors the
captured wire shape, including CDR padding, and asserts codec, dimensions, and
exact access-unit preservation. The manager test also asserts that H.264 reaches
the loopback writer unchanged as H.264.

### 7. Restarting from an agent-hosted shell delayed recovery

Restarting `wendy-agent` inside `wendy device shell` leaves that shell in the
service cgroup while systemd is trying to stop it. The connection must close
before the restart completes. This is a validation-workflow issue, not a camera
crash: close the device shell before a restart, or use `wendy device push-agent`
for binary replacement.

### 8. The existing V4L2 capture struct was shifted by four bytes

After the Go2 decoder and H.264 writer were corrected, `camera view` still
fell back to GStreamer and failed negotiation. A bounded ioctl probe showed
that the kernel's valid 640x360 H.264 format was read as width 0, height 640,
and pixel format 360. The video service's `v4l2_format` declaration omitted the
four bytes required to align the UAPI union at offset 8 on 64-bit Linux.

The struct now matches the 208-byte UAPI layout used by the writer, with both a
regression test and compile-time size/offset checks. Native H.264 capture then
worked without the raw-video GStreamer fallback.

### 9. Parameterless entitlement labels disappeared in containerd

The first `camera`-entitled test app received the `/dev` mount and video-device
permissions, but the ROS 2 manager never saw it as a consumer. Live container
metadata showed no `sh.wendy/entitlement.camera` label. Parameterless
entitlements were encoded with an empty label value, which did not survive
containerd persistence, so the truth-driven consumer scan always returned an
empty set.

Parameterless entitlements now use the non-empty `enabled=true` sentinel. The
existing parser ignores that field and reconstructs the entitlement from its
key, preserving compatibility while keeping camera, GPU, and other
parameterless entitlements visible to lifecycle reconciliation.

## Final hardware result

With all fixes applied and v4l2loopback 0.15.4 loaded:

- The same agent PID recovered after an initial missing-module failure; no
  agent restart was needed after loading the module.
- The PR-built CLI captured a clean 373,223-byte Annex-B H.264 stream in ten
  seconds, with the file growing on every one-second observation after startup.
- The capture contained 104 non-IDR slices and four each of IDR, SPS, and PPS
  NAL units, proving advancing and independently decodable stream structure.
- A temporary `TEST-pr1827-go2-camera` Stagefile app with the `camera`
  entitlement saw `/dev/video128` as 640x360 H.264 and captured 101,569 bytes
  across 30 buffers at approximately 13.3 fps.
- The temporary app and image were removed after the test.

The loopback device is exclusive-caps: an app that opens it immediately at
process start can briefly see `VIDIOC_G_FMT: Invalid argument` before the first
producer frame configures the capture side. The test app used a bounded retry,
then captured successfully.

## Validation workflow notes

- The installed host CLI was older than the PR and wrote its status banner into
  redirected `--stdout` output. Final stream evidence used a CLI built from the
  fixing branch, whose status line is on stderr and whose stdout begins with an
  Annex-B start code.
- The host Docker config referenced an unavailable
  `docker-credential-desktop`. The temporary Stagefile build used an isolated
  empty Docker config for anonymous pulls and the native Apple Container
  builder; no workstation Docker configuration was changed.

## Automated verification

Focused race tests, related package tests, vet, ARM64 agent compilation, and a
Linux/ARM64 ROS 2 camera test-binary compile pass. The broad `go test ./...`
run also reached three unrelated, pre-existing Mac/environment failures: the
tegrastats fallback test, a `/private/var` path-alias expectation, and a live
address-resolution expectation. None is in a package changed by this fix.
