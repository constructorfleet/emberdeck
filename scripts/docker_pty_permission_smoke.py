#!/usr/bin/env python3
"""Check Docker Linux PTY DAC behavior with and without its device group."""

from __future__ import annotations

import grp
import os
import pty
import subprocess
import sys


HELPER_IMAGE = "python:3.12-slim"
PROBE_CODE = r'''
import errno, os, sys
try:
    fd = os.open("/dev/emberdeck-probe", os.O_RDWR | os.O_NOCTTY)
except OSError as error:
    print(f"open denied with errno {error.errno}")
    sys.exit(13 if error.errno == errno.EACCES else 14)
else:
    os.close(fd)
    print("PTY open succeeded")
'''.strip()


def docker(*args, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(["docker", *args], check=check, text=True, capture_output=True)


def run_privileged(args: list[str]) -> subprocess.CompletedProcess:
    command = args if os.geteuid() == 0 else ["sudo", *args]
    return subprocess.run(command, check=True, text=True, capture_output=True)


def set_device_permissions(device_path: str, group_id: int) -> None:
    run_privileged(["chown", f"0:{group_id}", device_path])
    run_privileged(["chmod", "0660", device_path])


def docker_probe(device_path: str, group_id: int, include_group: bool) -> subprocess.CompletedProcess:
    args = [
        "run", "--rm", "--user", "65532",
        "--device", f"{device_path}:/dev/emberdeck-probe:rw",
    ]
    if include_group:
        args.extend(["--group-add", str(group_id)])
    args.extend([HELPER_IMAGE, "python3", "-c", PROBE_CODE])
    return docker(*args, check=False)


def assert_docker_access(device_path: str, group_id: int) -> None:
    denied = docker_probe(device_path, group_id, include_group=False)
    if denied.returncode != 13:
        raise RuntimeError(
            "Docker probe expected EACCES without the device group, "
            f"got exit {denied.returncode}: {denied.stderr or denied.stdout}"
        )

    allowed = docker_probe(device_path, group_id, include_group=True)
    if allowed.returncode != 0:
        raise RuntimeError(
            f"Docker probe could not open the PTY with supplementary group {group_id}: "
            f"{allowed.stderr or allowed.stdout}"
        )


def run_docker_pty_permission_smoke() -> None:
    if not sys.platform.startswith("linux"):
        raise RuntimeError("Docker PTY permission smoke requires Linux")
    group_id = grp.getgrnam("dialout").gr_gid
    master, slave = pty.openpty()
    device_path = os.ttyname(slave)
    try:
        set_device_permissions(device_path, group_id)
        assert_docker_access(device_path, group_id)
        print(
            f"PASS Docker Linux PTY DAC access: root:dialout gid {group_id} mode 0660 "
            "denied without group and opened with supplementary group"
        )
    finally:
        os.close(slave)
        os.close(master)


def main() -> int:
    try:
        run_docker_pty_permission_smoke()
        return 0
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"FAIL Docker PTY permission smoke: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
