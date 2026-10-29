#!/usr/bin/env python3
"""Check Docker Linux device mapping and supplementary-group DAC behavior."""

from __future__ import annotations

import grp
import os
import subprocess
import sys
import tempfile


HELPER_IMAGE = "python:3.12-slim"
DEVICE_IN_CONTAINER = "/dev/emberdeck-probe"
PROBE_CODE = f'''\
import os
fd = os.open("{DEVICE_IN_CONTAINER}", os.O_RDWR)
os.close(fd)
print("character device open succeeded")
'''.strip()


def docker(*args, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(["docker", *args], check=check, text=True, capture_output=True)


def run_privileged(args: list[str]) -> subprocess.CompletedProcess:
    command = args if os.geteuid() == 0 else ["sudo", *args]
    return subprocess.run(command, check=True, text=True, capture_output=True)


def create_device_node(device_path: str, group_id: int) -> None:
    # /dev/null is a harmless, openable character device (major 1, minor 3).
    # A disposable node lets this check DAC and --device without devpts behavior.
    run_privileged(["mknod", device_path, "c", "1", "3"])
    run_privileged(["chown", f"0:{group_id}", device_path])
    run_privileged(["chmod", "0660", device_path])


def docker_probe(device_path: str, group_id: int, include_group: bool) -> subprocess.CompletedProcess:
    args = [
        "run", "--rm", "--user", "65532",
        "--device", f"{device_path}:{DEVICE_IN_CONTAINER}:rw",
    ]
    if include_group:
        args.extend(["--group-add", str(group_id)])
    args.extend([HELPER_IMAGE, "python3", "-c", PROBE_CODE])
    return docker(*args, check=False)


def assert_docker_access(device_path: str, group_id: int) -> None:
    denied = docker_probe(device_path, group_id, include_group=False)
    if denied.returncode != 1 or "PermissionError" not in denied.stderr:
        raise RuntimeError(
            "Docker probe expected PermissionError without the device group, "
            f"got exit {denied.returncode}: {denied.stderr or denied.stdout}"
        )

    allowed = docker_probe(device_path, group_id, include_group=True)
    if allowed.returncode != 0:
        raise RuntimeError(
            f"Docker probe could not open the character device with supplementary group {group_id}: "
            f"{allowed.stderr or allowed.stdout}"
        )


def run_docker_device_permission_smoke() -> None:
    if not sys.platform.startswith("linux"):
        raise RuntimeError("Docker device permission smoke requires Linux")
    group_id = grp.getgrnam("dialout").gr_gid
    with tempfile.TemporaryDirectory(prefix="emberdeck-device-") as directory:
        device_path = os.path.join(directory, "probe")
        create_device_node(device_path, group_id)
        assert_docker_access(device_path, group_id)
    print(
        f"PASS Docker Linux character-device DAC access: root:dialout gid {group_id} mode 0660 "
        "denied without group and opened with supplementary group"
    )


def main() -> int:
    try:
        run_docker_device_permission_smoke()
        return 0
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"FAIL Docker device permission smoke: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
