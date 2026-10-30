# Raspberry Pi OS under QEMU: feasibility evidence

## Observed experiment (2026-10-29)

On a Mac M2, QEMU 11.1.3 ran the `raspi4b` machine with the official Raspberry Pi OS Lite 64-bit Trixie image dated 2026-10-06. The experiment extracted and decompressed `kernel8`, passed `bcm2711-rpi-4-b.dtb`, mounted the image's actual root partition, and reached arm64 systemd startup. QEMU warned that it disabled the Pi PCIe, RNG, thermal, and GENET devices described by the DTB. USB networking was attached and host port forwarding accepted a TCP connection, but SSH timed out during banner exchange; no guest SSH or network handshake was observed.

A separate Pi 3 run with USB networking ended in a fatal `dwc_otg_handle_common_intr` kernel panic.

## What this establishes

This is evidence that this particular image's arm64 kernel can boot far enough under this QEMU machine model to start systemd while using its root partition. It is not evidence of a working guest network or SSH connection. The TCP port-forward connection alone does not establish that the guest completed a network handshake.

It does not establish that the EmberDeck release installer works in QEMU, so it cannot satisfy a release-install smoke check. It also does not establish behavior on native Raspberry Pi hardware or through a physical USB serial connection. Treat the result as limited boot and kernel evidence, not as support or acceptance completion.

## Relevant primary documentation

QEMU documents `raspi4b` as a Raspberry Pi 4 model and lists incomplete modeled hardware, including the Pi 4 PCIe root port and GENET Ethernet controller. Its implementation explicitly removes the BCM2711 PCIe, RNG, thermal, and GENET nodes from the DTB because those devices are not implemented. [QEMU Raspberry Pi board documentation](https://www.qemu.org/docs/master/system/arm/raspi.html) · [QEMU `raspi4b` source at `c8660c6`](https://github.com/qemu/qemu/blob/c8660c6977e6c0ac5c2b1ba5c6f4ec89f85c9d58/hw/arm/raspi4b.c)

Raspberry Pi's configuration documentation identifies `kernel8.img` as a 64-bit kernel image for BCM2711, including Raspberry Pi 4, and explains that firmware loads a DTB for the kernel. This describes the native boot files; it does not claim that QEMU emulation is equivalent to a Pi. [Raspberry Pi configuration documentation](https://www.raspberrypi.com/documentation/computers/configuration.html)
