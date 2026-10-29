#!/usr/bin/env python3
"""Check macOS PTY DAC through EmberDeck's production serial opener.

The PTY's native /dev/ttys* path distinguishes an unprivileged worker's
permission denial from the expected post-open modem-ioctl limitation. macOS
does not permit creating test aliases inside /dev; this does not test real
/dev/cu.* IOKit/USB serial devices, streaming, or physical controller behavior.
"""

from __future__ import annotations

import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parents[1]
BINARY = ROOT / "bin" / "emberdeck"
ADMIN_KEY = "macos-serial-smoke-admin-key"
BAUD = "115200"
DENIED_ID = "macos-serial-denied"
ALLOWED_ID = "macos-serial-allowed"


def driver_command(
    binary: str, server_url: str, driver_id: str, port_path: str
) -> list[str]:
    return [
        binary, "driver", "-server", server_url, "-id", driver_id,
        "-serial", port_path, "-baud", BAUD, "-serial-dtr", "off", "-serial-rts", "off",
    ]


def worker_command(
    binary: str, server_url: str, key: str, driver_id: str, port_path: str,
    *, identity: str | None = None,
) -> list[str]:
    command = driver_command(binary, server_url, driver_id, port_path)
    if identity is None:
        return command
    return ["sudo", "-u", identity, "/usr/bin/env", f"EMBERDECK_API_KEY={key}", *command]


def verify_settings_error(error: str, expected: str) -> None:
    normalized = error.lower()
    if expected == "inappropriate ioctl for device" and "permission denied" in normalized:
        raise RuntimeError(f"expected post-open ioctl failure, got permission denial: {error!r}")
    if expected.lower() not in normalized:
        raise RuntimeError(f"expected {expected!r} in controller settings error, got {error!r}")


def api_request(
    base_url: str, key: str, path: str, *, method: str = "GET", payload: dict | None = None
) -> dict | list:
    body = None if payload is None else json.dumps(payload).encode()
    headers = {"Authorization": f"Bearer {key}"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = Request(f"{base_url}{path}", data=body, method=method, headers=headers)
    with urlopen(request, timeout=3) as response:
        raw = response.read()
    return {} if not raw else json.loads(raw)


def wait_for_api(base_url: str, timeout: float = 30) -> None:
    deadline = time.monotonic() + timeout
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            api_request(base_url, ADMIN_KEY, "/api/v1/auth")
            return
        except (OSError, URLError) as error:
            last_error = error
            time.sleep(0.25)
    raise RuntimeError(f"EmberDeck API did not become ready: {last_error}")


def create_worker_credential(base_url: str) -> str:
    credential = api_request(
        base_url, ADMIN_KEY, "/api/v1/credentials", method="POST",
        payload={
            "name": "macOS serial smoke worker", "role": "worker",
            "driver_ids": [DENIED_ID, ALLOWED_ID],
        },
    )
    key = credential.get("key") if isinstance(credential, dict) else None
    if not key:
        raise RuntimeError("EmberDeck did not return a one-time worker credential")
    return key


def read_settings_error(base_url: str, driver_id: str) -> str | None:
    drivers = api_request(base_url, ADMIN_KEY, "/api/v1/drivers")
    for driver_record in drivers:
        if driver_record.get("id") == driver_id:
            return driver_record.get("settings_error")
    return None


def wait_for_settings_error(base_url: str, driver_id: str, expected: str, timeout: float = 30) -> str:
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            last_error = read_settings_error(base_url, driver_id)
        except (OSError, URLError, json.JSONDecodeError):
            pass
        if last_error:
            if expected.lower() in last_error.lower():
                return last_error
            if "permission denied" not in last_error.lower() and "inappropriate ioctl" not in last_error.lower():
                raise RuntimeError(f"unexpected controller settings error: {last_error}")
        time.sleep(0.25)
    raise RuntimeError(f"timed out waiting for settings error {expected!r}; last={last_error!r}")


def start_process(command: list[str], env: dict[str, str], log_path: str):
    log_file = open(log_path, "w", encoding="utf-8")
    process = subprocess.Popen(command, env=env, stdout=log_file, stderr=subprocess.STDOUT)
    return process, log_file


def stop_process(process: subprocess.Popen | None, log_file) -> None:
    if process is not None and process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    if log_file is not None:
        log_file.close()


def run_smoke() -> None:
    if sys.platform != "darwin":
        raise RuntimeError("macOS serial permission smoke requires macOS")
    if not BINARY.is_file():
        raise RuntimeError(f"expected built EmberDeck binary at {BINARY}")

    import pty

    master_fd, slave_fd = pty.openpty()
    pty_path = os.ttyname(slave_fd)
    os.chmod(pty_path, 0o600)
    with tempfile.TemporaryDirectory(prefix="emberdeck-macos-serial-") as directory:
        data_dir = os.path.join(directory, "data")
        server_log = os.path.join(directory, "server.log")
        denied_log = os.path.join(directory, "denied-worker.log")
        allowed_log = os.path.join(directory, "allowed-worker.log")
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        server_url = f"http://127.0.0.1:{port}"
        server_env = os.environ.copy()
        server_env["EMBERDECK_API_KEY"] = ADMIN_KEY
        server, server_file = None, None
        denied_worker, denied_file = None, None
        allowed_worker, allowed_file = None, None
        try:
            server, server_file = start_process(
                [str(BINARY), "serve", "-listen", f"127.0.0.1:{port}", "-data", data_dir],
                server_env, server_log,
            )
            wait_for_api(server_url)
            worker_key = create_worker_credential(server_url)
            worker_env = os.environ.copy()
            worker_env["EMBERDECK_API_KEY"] = worker_key

            denied_worker, denied_file = start_process(
                worker_command(
                    str(BINARY), server_url, worker_key, DENIED_ID, pty_path, identity="_www"
                ),
                worker_env, denied_log,
            )
            denied = wait_for_settings_error(server_url, DENIED_ID, "permission denied")
            verify_settings_error(denied, "permission denied")
            stop_process(denied_worker, denied_file)
            denied_worker, denied_file = None, None

            allowed_worker, allowed_file = start_process(
                worker_command(str(BINARY), server_url, worker_key, ALLOWED_ID, pty_path),
                worker_env, allowed_log,
            )
            allowed = wait_for_settings_error(
                server_url, ALLOWED_ID, "inappropriate ioctl for device"
            )
            verify_settings_error(allowed, "inappropriate ioctl for device")
            print(
                f"PASS macOS serial DAC: _www was denied mode-0600 PTY access at {pty_path}; "
                "the owning user reached post-open ENOTTY through EmberDeck's serial opener. "
                "No /dev/cu.* IOKit/USB, stream, or hardware claim is made."
            )
        finally:
            stop_process(allowed_worker, allowed_file)
            stop_process(denied_worker, denied_file)
            stop_process(server, server_file)
            os.close(master_fd)
            os.close(slave_fd)


def main() -> int:
    try:
        run_smoke()
        return 0
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"FAIL macOS serial permission smoke: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
