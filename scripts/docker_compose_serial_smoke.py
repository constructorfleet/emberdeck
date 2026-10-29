#!/usr/bin/env python3
"""Verify Compose passes Linux device DAC through EmberDeck's serial opener.

A disposable /dev/null-backed character device distinguishes EACCES without
the configured GID from the expected post-open ENOTTY with it. This does not
claim serial streaming, GRBL behavior, or physical hardware qualification.
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen


REPO_ROOT = Path(__file__).resolve().parents[1]
IMAGE = "emberdeck:local"
ADMIN_KEY = "compose-serial-smoke-admin-key"
DRIVER_KEY = "compose-serial-smoke-worker-key"
DRIVER_ID = "compose-serial-smoke"
BAUD = 115200


def docker(*args: str, check: bool = True, env: dict[str, str] | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(["docker", *args], check=check, text=True, capture_output=True, env=env)


def compose_command(project: str, *args: str, override_file: str | None = None) -> list[str]:
    command = ["docker", "compose", "--project-name", project, "-f", str(REPO_ROOT / "compose.yaml")]
    if override_file:
        command.extend(["-f", override_file])
    return [*command, "--profile", "serial", *args]


def compose(
    project: str, *args: str, env: dict[str, str], override_file: str | None = None
) -> subprocess.CompletedProcess:
    return subprocess.run(
        compose_command(project, *args, override_file=override_file),
        check=True, text=True, capture_output=True, env=env,
    )


def run_privileged(args: list[str]) -> subprocess.CompletedProcess:
    command = args if os.geteuid() == 0 else ["sudo", *args]
    return subprocess.run(command, check=True, text=True, capture_output=True)


def create_device_node(device_path: str, group_id: int) -> None:
    run_privileged(["mknod", device_path, "c", "1", "3"])
    run_privileged(["chown", f"0:{group_id}", device_path])
    run_privileged(["chmod", "0660", device_path])


def remove_device_node(device_path: str) -> None:
    if os.path.exists(device_path):
        run_privileged(["rm", "-f", device_path])


def write_port_override(path: str, port: int) -> None:
    Path(path).write_text(
        json.dumps({"services": {"emberdeck": {"ports": [f"127.0.0.1:{port}:8080"]}}}),
        encoding="utf-8",
    )


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


def verify_settings_error(error: str, expected: str) -> None:
    normalized = error.lower()
    if expected == "permission denied" and "inappropriate ioctl" in normalized:
        raise RuntimeError(f"expected permission denial before device open, got {error!r}")
    if expected == "inappropriate ioctl for device" and "permission denied" in normalized:
        raise RuntimeError(f"expected post-open ioctl failure, got permission denial: {error!r}")
    if expected.lower() not in normalized:
        raise RuntimeError(f"expected {expected!r} in controller settings error, got {error!r}")


def read_driver_settings_error(base_url: str, key: str, driver_id: str) -> str | None:
    request = Request(
        f"{base_url}/api/v1/drivers", headers={"Authorization": f"Bearer {key}"}
    )
    with urlopen(request, timeout=2) as response:
        drivers = json.loads(response.read())
    for driver_record in drivers:
        if driver_record.get("id") == driver_id:
            return driver_record.get("settings_error")
    return None


def wait_for_api(base_url: str, key: str, timeout: float = 30) -> None:
    deadline = time.monotonic() + timeout
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            request = Request(f"{base_url}/api/v1/auth", headers={"Authorization": f"Bearer {key}"})
            with urlopen(request, timeout=2):
                return
        except (OSError, URLError) as error:
            last_error = error
            time.sleep(0.25)
    raise RuntimeError(f"EmberDeck API did not become ready: {last_error}")


def create_worker_credential(base_url: str, admin_key: str, driver_id: str) -> str:
    payload = json.dumps({
        "name": "Compose serial smoke worker", "role": "worker", "driver_ids": [driver_id],
    }).encode()
    request = Request(
        f"{base_url}/api/v1/credentials", data=payload, method="POST",
        headers={"Authorization": f"Bearer {admin_key}", "Content-Type": "application/json"},
    )
    with urlopen(request, timeout=5) as response:
        credential = json.loads(response.read())
    key = credential.get("key")
    if not key:
        raise RuntimeError("EmberDeck did not return a one-time worker credential")
    return key


def wait_for_settings_error(
    base_url: str, key: str, driver_id: str, expected: str, timeout: float = 30
) -> str:
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            last_error = read_driver_settings_error(base_url, key, driver_id)
        except (OSError, URLError, json.JSONDecodeError):
            pass
        if last_error:
            if expected.lower() in last_error.lower():
                return last_error
            # A stale previous result is allowed while the recreated worker reconnects.
            if "permission denied" not in last_error.lower() and "inappropriate ioctl" not in last_error.lower():
                raise RuntimeError(f"unexpected controller settings error: {last_error}")
        time.sleep(0.25)
    raise RuntimeError(f"timed out waiting for controller settings error {expected!r}; last={last_error!r}")


def cleanup_compose_project(
    project: str, env: dict[str, str], image: str, override_file: str | None = None
) -> None:
    result = subprocess.run(
        compose_command(
            project, "down", "--remove-orphans", "--volumes", override_file=override_file
        ),
        check=False, text=True, capture_output=True, env=env,
    )
    if result.returncode != 0:
        raise RuntimeError(f"could not remove Compose smoke project: {result.stderr or result.stdout}")
    exists = docker("image", "inspect", image, check=False)
    if exists.returncode == 0:
        image_result = docker("image", "rm", "--force", image, check=False)
        if image_result.returncode != 0:
            raise RuntimeError(
                f"could not remove Compose smoke image {image}: {image_result.stderr or image_result.stdout}"
            )


def run_smoke() -> None:
    if not sys.platform.startswith("linux"):
        raise RuntimeError("Compose serial smoke requires Linux and Docker Compose")
    import grp

    gid = grp.getgrnam("dialout").gr_gid
    suffix = uuid.uuid4().hex[:10]
    project = f"emberdeck-serial-{suffix}"
    image = f"emberdeck:compose-serial-smoke-{suffix}"
    with tempfile.TemporaryDirectory(prefix="emberdeck-compose-serial-") as directory:
        device = os.path.join(directory, "controller")
        override_file = os.path.join(directory, "ports.yaml")
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        write_port_override(override_file, port)
        create_device_node(device, gid)
        env = os.environ.copy()
        env.update(serial_environment(device=device, gid=gid, key=DRIVER_KEY, image=image))
        env["EMBERDECK_API_KEY"] = ADMIN_KEY
        base_url = f"http://127.0.0.1:{port}"
        try:
            docker("build", "--tag", image, ".")
            compose(project, "up", "--detach", "emberdeck", env=env, override_file=override_file)
            wait_for_api(base_url, ADMIN_KEY)
            worker_key = create_worker_credential(base_url, ADMIN_KEY, DRIVER_ID)
            env["EMBERDECK_DRIVER_API_KEY"] = worker_key
            config = compose_config(project, env)
            verify_serial_service_config(config, device, gid, worker_key, image)

            # First omit the device GID. The real worker probe must report EACCES.
            denied_env = dict(env, EMBERDECK_SERIAL_GID=str(gid + 1))
            compose(project, "up", "--detach", "serial-driver", env=denied_env, override_file=override_file)
            denied = wait_for_settings_error(base_url, ADMIN_KEY, DRIVER_ID, "permission denied")
            verify_settings_error(denied, "permission denied")

            # Recreate with the configured numeric group. Open succeeds and the
            # /dev/null-backed device reaches the expected termios ENOTTY limit.
            compose(project, "stop", "serial-driver", env=denied_env, override_file=override_file)
            compose(project, "rm", "--force", "serial-driver", env=denied_env, override_file=override_file)
            compose(project, "up", "--detach", "serial-driver", env=env, override_file=override_file)
            allowed = wait_for_settings_error(base_url, ADMIN_KEY, DRIVER_ID, "inappropriate ioctl for device")
            verify_settings_error(allowed, "inappropriate ioctl for device")
            print(
                f"PASS Compose EmberDeck serial opener: mode 0660 root:dialout gid {gid} device denied without "
                "the supplementary GID and reached post-open ENOTTY with it; no serial-stream or hardware claim is made."
            )
        finally:
            try:
                cleanup_compose_project(project, env, image, override_file)
            finally:
                remove_device_node(device)


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
