#!/usr/bin/env python3
"""Validate two published release binaries through installation and HTTP flows."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


KEY = "release-install-smoke-key-do-not-use-outside-this-test"
PROGRAM = "G21\nG90\nM5\n"


def verify_asset_checksum(asset: pathlib.Path, manifest: pathlib.Path) -> None:
    """Check one release asset against the SHA256SUMS file published beside it."""
    expected = None
    for line in manifest.read_text(encoding="utf-8").splitlines():
        fields = line.strip().split(maxsplit=1)
        if len(fields) != 2:
            continue
        name = fields[1].lstrip("*\\")
        if pathlib.PurePosixPath(name).name == asset.name:
            expected = fields[0].lower()
            break
    if expected is None:
        raise ValueError(f"{asset.name} is missing from SHA256SUMS")
    actual = hashlib.sha256(asset.read_bytes()).hexdigest()
    if actual != expected:
        raise ValueError(f"checksum mismatch for {asset.name}: expected {expected}, got {actual}")


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def api(base: str, path: str, method: str = "GET", payload=None, authenticated=True):
    data = None if payload is None else json.dumps(payload).encode()
    headers = {"Content-Type": "application/json"}
    if authenticated:
        headers["Authorization"] = f"Bearer {KEY}"
    request = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=3) as response:
            body = response.read()
            return response.status, json.loads(body) if body else None
    except urllib.error.HTTPError as exc:
        body = exc.read()
        try:
            detail = json.loads(body)
        except json.JSONDecodeError:
            detail = body.decode(errors="replace")
        return exc.code, detail


def wait_for(predicate, description: str, timeout: float = 25):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = predicate()
            if last:
                return last
        except (OSError, urllib.error.URLError, TimeoutError, json.JSONDecodeError):
            # The service may still be starting; retry until the deadline.
            continue
        time.sleep(0.1)
    raise RuntimeError(f"timed out waiting for {description}; last value: {last!r}")


def stop(process: subprocess.Popen) -> None:
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)


def start_server(binary: pathlib.Path, data_dir: pathlib.Path) -> tuple[subprocess.Popen, str]:
    port = free_port()
    env = os.environ.copy()
    env["EMBERDECK_API_KEY"] = KEY
    process = subprocess.Popen(
        [str(binary), "serve", "-listen", f"127.0.0.1:{port}", "-data", str(data_dir), "-simulate"],
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    base = f"http://127.0.0.1:{port}"
    try:
        status = wait_for(lambda: api(base, "/api/v1/jobs", authenticated=False)[0], "HTTP listener")
        if status != 401:
            raise RuntimeError(f"unauthenticated first-start request returned HTTP {status}, want 401")
        status, jobs = api(base, "/api/v1/jobs")
        if status != 200 or not isinstance(jobs, dict):
            raise RuntimeError(f"authenticated login failed: HTTP {status}: {jobs}")
        return process, base
    except Exception:
        stop(process)
        raise


def upload_and_dispatch(base: str, name: str) -> str:
    status, job = api(base, "/api/v1/jobs", "POST", {"name": name, "gcode": PROGRAM})
    if status != 201:
        raise RuntimeError(f"upload failed: HTTP {status}: {job}")
    job_id = job["id"]
    status, assigned = api(
        base,
        f"/api/v1/jobs/{job_id}/start",
        "POST",
        {"driver_id": "simulator", "confirmed": True, "bounds_override": True},
    )
    if status != 200:
        raise RuntimeError(f"simulated dispatch failed: HTTP {status}: {assigned}")

    def completed():
        status, current = api(base, f"/api/v1/jobs/{job_id}")
        if status != 200:
            raise RuntimeError(f"job lookup failed: HTTP {status}: {current}")
        return current if current.get("state") == "completed" else None

    wait_for(completed, f"simulated job {job_id} to complete")
    return job_id


def assert_version(binary: pathlib.Path, expected: str) -> None:
    result = subprocess.run([str(binary), "-version"], capture_output=True, text=True, timeout=10)
    output = (result.stdout + result.stderr).strip()
    if result.returncode != 0 or output != f"EmberDeck {expected}":
        raise RuntimeError(f"expected EmberDeck {expected}, got {output!r}")


def persisted_job(base: str, job_id: str) -> bool:
    status, job = api(base, f"/api/v1/jobs/{job_id}")
    return status == 200 and job.get("state") == "completed"


def run(previous_binary: pathlib.Path, previous_version: str,
        current_binary: pathlib.Path, current_version: str) -> None:
    previous_binary = previous_binary.resolve()
    current_binary = current_binary.resolve()

    with tempfile.TemporaryDirectory(prefix="emberdeck-release-install-") as temporary:
        root = pathlib.Path(temporary)
        install_dir = root / "install"
        install_dir.mkdir()
        suffix = ".exe" if os.name == "nt" else ""
        installed_previous = install_dir / ("emberdeck-previous" + suffix)
        installed_current = install_dir / ("emberdeck-current" + suffix)
        shutil.copy2(previous_binary, installed_previous)
        shutil.copy2(current_binary, installed_current)
        if os.name != "nt":
            for binary in (installed_previous, installed_current):
                binary.chmod(stat.S_IMODE(binary.stat().st_mode) | 0o111)
        assert_version(installed_previous, previous_version)
        assert_version(installed_current, current_version)
        data_dir = root / "data"

        previous, base = start_server(installed_previous, data_dir)
        try:
            previous_job = upload_and_dispatch(base, f"upgrade-from-{previous_version}.gcode")
        finally:
            stop(previous)

        current, base = start_server(installed_current, data_dir)
        try:
            if not persisted_job(base, previous_job):
                raise RuntimeError(f"job {previous_job} was lost when upgrading to {current_version}")
            current_job = upload_and_dispatch(base, f"install-{current_version}.gcode")
        finally:
            stop(current)

        current, base = start_server(installed_current, data_dir)
        try:
            for job_id in (previous_job, current_job):
                if not persisted_job(base, job_id):
                    raise RuntimeError(f"job {job_id} was lost after restarting {current_version}")
        finally:
            stop(current)

        # Exercise the released binary/GRBL wire contract as well as the simulator API path.
        artifact_smoke = pathlib.Path(__file__).with_name("artifact-smoke.py")
        subprocess.run(
            [sys.executable, str(artifact_smoke), str(installed_current), "--expected-version", current_version],
            check=True,
            timeout=150,
        )
        shutil.rmtree(install_dir)
        if install_dir.exists():
            raise RuntimeError("uninstall failed: temporary release binaries remain installed")
        print(
            f"PASS published install smoke: {previous_version} -> {current_version}; "
            "login, upload, simulated dispatch, persistence, restart, and uninstall"
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--previous-binary", required=True, type=pathlib.Path)
    parser.add_argument("--previous-version", required=True)
    parser.add_argument("--previous-checksums", required=True, type=pathlib.Path)
    parser.add_argument("--current-binary", required=True, type=pathlib.Path)
    parser.add_argument("--current-version", required=True)
    parser.add_argument("--current-checksums", required=True, type=pathlib.Path)
    args = parser.parse_args()
    try:
        verify_asset_checksum(args.previous_binary, args.previous_checksums)
        verify_asset_checksum(args.current_binary, args.current_checksums)
        run(args.previous_binary, args.previous_version, args.current_binary, args.current_version)
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as exc:
        print(f"FAIL published install smoke: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
