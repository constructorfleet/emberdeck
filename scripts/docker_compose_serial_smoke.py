#!/usr/bin/env python3
"""Smoke the Compose serial profile and report the Linux PTY ioctl limitation.

The built EmberDeck image is exercised through the `serial` Compose profile,
including its resolved host-device mapping, numeric supplementary GID,
dedicated key, baud rate, and DTR/RTS flags. Linux PTYs do not implement the
modem-bit ioctls EmberDeck must apply to physical serial ports, so a PTY cannot
serve as a fake GRBL controller without weakening production safety behavior.
This smoke therefore does not claim serial protocol or hardware qualification.
"""

from __future__ import annotations

import argparse
import errno
import json
import os
import stat
import subprocess
import sys
import uuid
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
IMAGE = "emberdeck:local"
DEVICE = "/dev/null"
DRIVER_KEY = "compose-serial-smoke-worker-key"
DRIVER_ID = "compose-serial-smoke"
BAUD = 115200


def docker(*args: str, check: bool = True, env: dict[str, str] | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(["docker", *args], check=check, text=True, capture_output=True, env=env)


def compose_command(project: str, *args: str) -> list[str]:
    return [
        "docker", "compose", "--project-name", project, "-f", str(REPO_ROOT / "compose.yaml"),
        "--profile", "serial", *args,
    ]


def compose(project: str, *args: str, env: dict[str, str]) -> subprocess.CompletedProcess:
    return subprocess.run(compose_command(project, *args), check=True, text=True, capture_output=True, env=env)


def serial_environment(
    *, device: str, gid: int, key: str = DRIVER_KEY, image: str = IMAGE
) -> dict[str, str]:
    """Return the serial service variables, including a disposable key for CI."""
    return {
        "EMBERDECK_API_KEY": key,
        "EMBERDECK_DRIVER_API_KEY": key,
        "EMBERDECK_SERVER_URL": "http://emberdeck:8080",
        "EMBERDECK_DRIVER_ID": DRIVER_ID,
        "EMBERDECK_SERIAL_DEVICE": device,
        "EMBERDECK_SERIAL_GID": str(gid),
        "EMBERDECK_SERIAL_BAUD": str(BAUD),
        "EMBERDECK_SERIAL_DTR": "off",
        "EMBERDECK_SERIAL_RTS": "off",
        "EMBERDECK_SERIAL_IMAGE": image,
    }


def compose_config(project: str, env: dict[str, str]) -> dict:
    result = compose(project, "config", "--format", "json", env=env)
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError(f"Compose returned invalid JSON config: {error}: {result.stdout}") from error


def verify_serial_service_config(
    config: dict, device: str, gid: int, driver_key: str, image: str
) -> None:
    service = config.get("services", {}).get("serial-driver")
    if service is None:
        raise RuntimeError("Compose config has no serial-driver service")
    if "serial" not in service.get("profiles", []):
        raise RuntimeError("serial-driver is not gated by the serial profile")
    devices = service.get("devices", [])
    mapping = [entry for entry in devices if entry.get("source") == device]
    if not mapping or not any(entry.get("target") == device for entry in mapping):
        raise RuntimeError(f"Compose does not map serial device {device!r} into the same path: {devices}")
    groups = {str(group) for group in service.get("group_add", [])}
    if str(gid) not in groups:
        raise RuntimeError(f"Compose did not add serial device GID {gid}: {sorted(groups)}")
    command = service.get("command", [])
    for expected in (
        "driver", "-server", "http://emberdeck:8080", "-id", DRIVER_ID,
        "-serial", device, "-baud", str(BAUD), "-serial-dtr", "off", "-serial-rts", "off",
    ):
        if expected not in command:
            raise RuntimeError(f"serial-driver command lacks {expected!r}: {command}")
    environment = service.get("environment", {})
    if environment.get("EMBERDECK_API_KEY") != driver_key:
        raise RuntimeError("serial-driver must map EMBERDECK_DRIVER_API_KEY into EMBERDECK_API_KEY")
    if service.get("image") != image:
        raise RuntimeError(f"serial-driver uses unexpected image {service.get('image')!r}, expected {image!r}")


def confirm_pty_lacks_modem_ioctl() -> int:
    """Return ENOTTY when devpts cannot model the modem bits required by OpenSerial."""
    import fcntl
    import pty
    import struct
    import termios

    if not hasattr(termios, "TIOCMGET"):
        raise RuntimeError("platform does not expose TIOCMGET for the PTY diagnostic")
    master_fd, slave_fd = pty.openpty()
    try:
        try:
            fcntl.ioctl(slave_fd, termios.TIOCMGET, struct.pack("i", 0))
        except OSError as error:
            if error.errno != errno.ENOTTY:
                raise RuntimeError(f"PTY TIOCMGET failed unexpectedly: {error}") from error
            return error.errno
        raise RuntimeError("PTY unexpectedly supports TIOCMGET; serial protocol smoke should be reconsidered")
    finally:
        os.close(master_fd)
        os.close(slave_fd)


def run_image_version(project: str, env: dict[str, str]) -> str:
    result = compose(project, "run", "--rm", "--no-deps", "serial-driver", "-version", env=env)
    if result.returncode != 0 or "EmberDeck " not in result.stdout:
        raise RuntimeError(f"serial profile image did not report its version: {result.stdout} {result.stderr}")
    return result.stdout.strip()


def cleanup_compose_project(project: str, env: dict[str, str], image: str) -> None:
    result = subprocess.run(
        compose_command(project, "down", "--remove-orphans"),
        check=False, text=True, capture_output=True, env=env,
    )
    if result.returncode != 0:
        raise RuntimeError(f"could not remove Compose smoke project: {result.stderr or result.stdout}")
    image_result = docker("image", "rm", "--force", image, check=False)
    if image_result.returncode != 0:
        raise RuntimeError(f"could not remove Compose smoke image {image}: {image_result.stderr or image_result.stdout}")


def run_smoke() -> None:
    if not sys.platform.startswith("linux"):
        raise RuntimeError("Compose serial smoke requires Linux and Docker Compose")
    metadata = os.stat(DEVICE)
    if not stat.S_ISCHR(metadata.st_mode):
        raise RuntimeError(f"smoke device must be a character device: {DEVICE}")
    gid = metadata.st_gid
    suffix = uuid.uuid4().hex[:10]
    project = f"emberdeck-serial-{suffix}"
    image = f"emberdeck:compose-serial-smoke-{suffix}"
    env = os.environ.copy()
    env.update(serial_environment(device=DEVICE, gid=gid, image=image))

    # Build the exact image referenced by the Compose profile, then validate the
    # fully interpolated active profile before executing any container.
    docker("build", "--tag", image, ".")
    try:
        config = compose_config(project, env)
        verify_serial_service_config(config, DEVICE, gid, DRIVER_KEY, image)

        # Command override keeps this read-only check independent of a live
        # server or hardware while retaining the service's image/device/group.
        version = run_image_version(project, env)

        # `go.bug.st/serial` applies the explicitly requested modem bits via
        # TIOCMGET/TIOCMSET. A devpts fake controller cannot satisfy that API.
        errno_value = confirm_pty_lacks_modem_ioctl()
        print(
            f"PASS Compose serial profile resolved {DEVICE} with supplementary GID {gid}, baud {BAUD}, "
            f"DTR/RTS off, and ran the built EmberDeck image ({version}). "
            f"PTY TIOCMGET returns ENOTTY ({errno_value}); no serial-stream or GRBL protocol claim is made."
        )
    finally:
        cleanup_compose_project(project, env, image)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args(argv)
    try:
        run_smoke()
        return 0
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"FAIL Compose serial smoke: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
