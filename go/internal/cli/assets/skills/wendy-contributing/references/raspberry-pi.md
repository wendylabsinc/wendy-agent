# Raspberry Pi Layer (meta-wendyos-rpi)

## Machine Configuration

```bitbake
# In build/conf/local.conf
MACHINE = "raspberrypi4-64-edgeos"  # RPi 4
MACHINE = "raspberrypi5-edgeos"      # RPi 5
```

## Serial Console

- RPi 4: `ttyS0` at 115200 baud (GPIO pins 14/15)
- RPi 5: `ttyAMA10` at 115200 baud (GPIO pins 14/15)

## Flashing Images

```bash
# Using bmaptool (recommended)
sudo bmaptool copy edgeos-rpi-image-*.wic.bz2 /dev/sdX

# Using dd on macOS
diskutil unmountDisk /dev/diskN && \
bzcat output/edgeos-rpi5.wic.bz2 | sudo dd of=/dev/rdiskN bs=4m status=progress && \
diskutil eject /dev/diskN
```

## Partition Layout

| Partition | Mount | Size | Purpose |
|-----------|-------|------|---------|
| 1 | /boot | 256MB | RPi firmware, kernel, DTBs |
| 2 | / | 4GB | Root filesystem |
| 3 | /data | 2GB+ | Persistent data (containerd, agent) |
