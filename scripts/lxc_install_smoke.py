#!/usr/bin/env python3
"""Validate published Linux releases and systemd lifecycle inside classic LXC."""

from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import pathlib
import secrets
import shlex
import stat
import subprocess
import sys
import time
import urllib.error


PROGRAM = "G21\nG90\nG1 X12 F100\nM5\n"
API_BASE = "http://127.0.0.1:8080"
SERVER_UNIT = "emberdeck.service"
DRIVER_UNIT = "emberdeck-driver.service"
DRIVER_ID = "lxc-smoke"
ROOT = pathlib.Path(__file__).resolve().parents[1]
DIALOUT_PTY_PERMISSION_PROBE = r'''
import grp, os, pty, subprocess

master, slave = pty.openpty()
path = os.ttyname(slave)
os.chown(path, 0, grp.getgrnam("dialout").gr_gid)
os.chmod(path, 0o660)
probe = ["/usr/bin/python3", "-c", "import os,sys; fd=os.open(sys.argv[1], os.O_RDWR | os.O_NOCTTY); os.close(fd)", path]
common = ["systemd-run", "--quiet", "--wait", "--pipe", "--collect", "--property=User=emberdeck", "--property=Group=emberdeck"]
denied = subprocess.run(common + probe, capture_output=True, text=True, timeout=20)
if denied.returncode == 0:
    raise SystemExit("PTY unexpectedly opened without dialout supplementary group")
allowed = subprocess.run(common + ["--property=SupplementaryGroups=dialout"] + probe, capture_output=True, text=True, timeout=20)
if allowed.returncode != 0:
    raise SystemExit(f"PTY open with dialout failed: {allowed.stderr or allowed.stdout}")
print("PASS Linux PTY DAC access: root:dialout mode 0660 denied without dialout and opened with systemd SupplementaryGroups=dialout")
'''.strip()


def verify_asset_checksum(asset: pathlib.Path, manifest: pathlib.Path) -> None:
    """Check a release asset against its SHA256SUMS manifest."""
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


def assert_version(binary: pathlib.Path, expected: str) -> None:
    result = subprocess.run([str(binary), "-version"], capture_output=True, text=True, timeout=10)
    actual = (result.stdout + result.stderr).strip()
    wanted = f"EmberDeck {expected}"
    if result.returncode != 0 or actual != wanted:
        raise RuntimeError(f"expected {wanted} from {binary.name}, got {actual!r}")


def guest(container: str, *command: str, input: bytes | None = None,
          timeout: float = 30, check: bool = True) -> subprocess.CompletedProcess:
    """Run as guest root; stdin lets the host safely stage files in its user namespace."""
    return subprocess.run(
        ["sudo", "lxc-attach", "--name", container, "--", *command],
        input=input,
        capture_output=True,
        text=False,
        timeout=timeout,
        check=check,
    )


def guest_text(container: str, *command: str, timeout: float = 30, check: bool = True) -> str:
    result = guest(container, *command, timeout=timeout, check=check)
    return result.stdout.decode(errors="replace").strip()


def guest_file(container: str, path: str, content: bytes, mode: str = "0600") -> None:
    command = f"umask 077; cat > {shlex.quote(path)} && chmod {mode} {shlex.quote(path)}"
    guest(container, "sh", "-c", command, input=content)


def namespace_pid(container: str) -> str:
    output = subprocess.run(
        ["sudo", "lxc-info", "--name", container, "--pid"],
        capture_output=True,
        text=True,
        timeout=30,
        check=True,
    ).stdout.strip()
    pid = output.split()[-1] if output else ""
    if not pid.isdigit() or int(pid) <= 1:
        raise RuntimeError(f"could not read LXC init pid for {container}: {output!r}")
    return pid


def api(container: str, method: str, path: str, key: str, payload=None,
        authenticated: bool = True) -> tuple[int, object]:
    """Call the guest loopback listener from its network namespace."""
    pid = namespace_pid(container)
    command = [
        "sudo", "nsenter", "--target", pid, "--net", "curl", "--silent", "--show-error",
        "--request", method, "--header", "Content-Type: application/json",
        "--write-out", "\n%{http_code}",
    ]
    if authenticated:
        command.extend(["--header", f"Authorization: Bearer {key}"])
    body = None
    if payload is not None:
        body = json.dumps(payload).encode()
        command.extend(["--data-binary", "@-"])
    command.append(API_BASE + path)
    result = subprocess.run(
        command,
        input=body,
        capture_output=True,
        text=False,
        timeout=10,
        check=True,
    )
    output = result.stdout.decode(errors="replace")
    response, separator, status_text = output.rpartition("\n")
    if not separator or not status_text.isdigit():
        raise RuntimeError(f"could not parse HTTP response for {method} {path}: {output!r}")
    if not response:
        decoded = None
    else:
        try:
            decoded = json.loads(response)
        except json.JSONDecodeError:
            decoded = response
    return int(status_text), decoded


def wait_for(description: str, predicate, timeout: float = 30, interval: float = 0.2):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            result = predicate()
            if result:
                return result
        except (OSError, RuntimeError, subprocess.SubprocessError, urllib.error.URLError) as error:
            last_error = error
        time.sleep(interval)
    detail = f"; last error: {last_error}" if last_error else ""
    raise RuntimeError(f"timed out waiting for {description}{detail}")


def assert_api(container: str, key: str, method: str, path: str, payload=None,
               expected_status: int = 200):
    status, response = api(container, method, path, key, payload)
    if status != expected_status:
        raise RuntimeError(f"{method} {path} returned HTTP {status}, expected {expected_status}: {response}")
    return response


def wait_server(container: str, key: str, first_start: bool = False) -> None:
    if first_start:
        wait_for(
            "first-start unauthenticated rejection",
            lambda: api(container, "GET", "/api/v1/jobs", key, authenticated=False)[0] == 401,
        )
    wait_for(
        "authenticated server API",
        lambda: api(container, "GET", "/api/v1/jobs", key)[0] == 200,
    )


def create_worker_key(container: str, admin_key: str) -> str:
    result = assert_api(
        container,
        admin_key,
        "POST",
        "/api/v1/credentials",
        {"name": "LXC smoke worker", "role": "worker", "driver_ids": [DRIVER_ID]},
        201,
    )
    key = result.get("key") if isinstance(result, dict) else None
    if not isinstance(key, str) or not key:
        raise RuntimeError("worker credential response did not contain its one-time key")
    return key


def online_driver(drivers, fresh_after: float | None = None):
    for driver in drivers if isinstance(drivers, list) else ():
        if driver.get("id") != DRIVER_ID:
            continue
        if fresh_after is None:
            return driver
        last_seen = driver.get("last_seen")
        if not isinstance(last_seen, str):
            return False
        try:
            timestamp = datetime.datetime.fromisoformat(last_seen.replace("Z", "+00:00")).timestamp()
        except ValueError:
            return False
        return driver if timestamp >= fresh_after else False
    return False


def wait_driver_online(container: str, admin_key: str, fresh_after: float | None = None) -> None:
    def online():
        drivers = api(container, "GET", "/api/v1/drivers", admin_key)
        if drivers[0] != 200 or not isinstance(drivers[1], list):
            return False
        return online_driver(drivers[1], fresh_after)

    wait_for(f"worker {DRIVER_ID} to register", online, timeout=45)


def upload_and_dispatch(container: str, admin_key: str, name: str) -> str:
    job = assert_api(container, admin_key, "POST", "/api/v1/jobs", {"name": name, "gcode": PROGRAM}, 201)
    job_id = job["id"]
    assert_api(
        container,
        admin_key,
        "POST",
        f"/api/v1/jobs/{job_id}/start",
        {"driver_id": DRIVER_ID, "confirmed": True, "bounds_override": True},
    )

    def completed():
        status, current = api(container, "GET", f"/api/v1/jobs/{job_id}", admin_key)
        if status != 200:
            return False
        return current if current.get("state") == "completed" else False

    wait_for(f"simulated job {job_id} to complete", completed, timeout=45)
    return job_id


def assert_job_persisted(container: str, admin_key: str, job_id: str, stage: str) -> None:
    status, job = api(container, "GET", f"/api/v1/jobs/{job_id}", admin_key)
    if status != 200 or not isinstance(job, dict) or job.get("state") != "completed":
        raise RuntimeError(f"job {job_id} was not retained after {stage}: HTTP {status}: {job}")


def main_pid(container: str, unit: str) -> int:
    value = guest_text(container, "systemctl", "show", "--property=MainPID", "--value", unit)
    if not value.isdigit():
        raise RuntimeError(f"could not read MainPID for {unit}: {value!r}")
    return int(value)


def proc_group_ids(status: str) -> set[str]:
    for line in status.splitlines():
        if line.startswith("Groups:"):
            return set(line.split()[1:])
    raise RuntimeError("process status does not contain a Groups line")


def assert_service_supplementary_group(container: str, unit: str, group_name: str) -> None:
    group = guest_text(container, "getent", "group", group_name)
    fields = group.rstrip("\n").split(":")
    if len(fields) < 3 or not fields[2].isdigit():
        raise RuntimeError(f"could not find numeric gid for {group_name}: {group!r}")
    gid = fields[2]
    pid = main_pid(container, unit)
    status = guest_text(container, "cat", f"/proc/{pid}/status")
    groups = proc_group_ids(status)
    if gid not in groups:
        raise RuntimeError(f"{unit} process is missing {group_name} supplementary group (gid {gid})")
    print(f"PASS systemd supplementary group: {unit} process includes {group_name} (gid {gid})")


def verify_dialout_pty_permissions(container: str) -> None:
    guest(container, "python3", "-c", DIALOUT_PTY_PERMISSION_PROBE)


def wait_service_active(container: str, unit: str, timeout: float = 45) -> int:
    def active_pid():
        active = guest_text(container, "systemctl", "is-active", unit, check=False)
        pid = main_pid(container, unit)
        return pid if active == "active" and pid > 1 else False

    return wait_for(f"{unit} to be active", active_pid, timeout=timeout)


def crash_and_wait_restart(container: str, unit: str) -> None:
    before = main_pid(container, unit)
    if before <= 1:
        raise RuntimeError(f"{unit} has no running MainPID before crash test")
    guest(container, "systemctl", "kill", "--kill-whom=main", "--signal=KILL", unit)

    def restarted():
        active = guest_text(container, "systemctl", "is-active", unit, check=False)
        pid = main_pid(container, unit)
        return pid if active == "active" and pid > 1 and pid != before else False

    after = wait_for(f"{unit} to restart after SIGKILL", restarted, timeout=60)
    print(f"PASS systemd crash restart: {unit} MainPID {before} -> {after}")


def lxc_state(container: str) -> str:
    result = subprocess.run(
        ["sudo", "lxc-info", "--name", container, "--state"],
        capture_output=True,
        text=True,
        timeout=30,
        check=True,
    )
    output = result.stdout.strip()
    return output.split()[-1] if output else ""


def assert_host_autostart_selected(container: str) -> None:
    result = subprocess.run(
        ["sudo", "lxc-autostart", "--list"],
        capture_output=True,
        text=True,
        timeout=30,
        check=True,
    )
    selected = {line.split()[0] for line in result.stdout.splitlines() if line.split()}
    if container not in selected:
        raise RuntimeError(f"{container} is not selected by host LXC autostart: {result.stdout!r}")


def start_host_autostart(container: str) -> None:
    subprocess.run(
        ["sudo", "lxc-autostart"], capture_output=True, text=True, timeout=120, check=True
    )
    wait_for(f"host LXC autostart to start {container}", lambda: lxc_state(container) == "RUNNING", timeout=60)

    def systemd_running():
        state = guest_text(container, "systemctl", "is-system-running", check=False)
        return state if state in ("running", "degraded") else False

    state = wait_for(f"systemd after host LXC autostart for {container}", systemd_running, timeout=45)
    print(f"PASS host LXC autostart dispatch: {container} running with systemd {state}")


def stop_start_guest(container: str) -> None:
    subprocess.run(["sudo", "lxc-stop", "--name", container, "--kill"], check=True, timeout=30)
    subprocess.run(["sudo", "lxc-start", "--name", container, "--daemon"], check=True, timeout=30)
    wait_for(f"LXC guest {container} to boot", lambda: lxc_state(container) == "RUNNING", timeout=60)
    def systemd_running():
        state = guest_text(container, "systemctl", "is-system-running", check=False)
        return state if state in ("running", "degraded") else False

    system_state = wait_for("guest systemd after LXC reboot", systemd_running, timeout=45)
    print(f"PASS LXC guest reboot: systemd {system_state}")


def install_units(container: str) -> None:
    guest(container, "sh", "-c", "id emberdeck >/dev/null 2>&1 || useradd --system --home /var/lib/emberdeck --create-home emberdeck")
    guest(container, "install", "-d", "-m", "0750", "/etc/emberdeck")
    for filename in ("emberdeck.service", "emberdeck-driver.service"):
        content = (ROOT / "deploy" / filename).read_bytes()
        guest_file(container, f"/etc/systemd/system/{filename}", content, "0644")
    guest(container, "systemctl", "daemon-reload")


def install_binary(container: str, binary: pathlib.Path) -> None:
    guest(container, "sh", "-c", "cat > /usr/local/bin/emberdeck && chmod 0755 /usr/local/bin/emberdeck", input=binary.read_bytes())


def configure_driver(container: str, worker_key: str) -> None:
    driver_env = (
        f"EMBERDECK_API_KEY={worker_key}\n"
        f"EMBERDECK_SERVER_URL={API_BASE}\n"
        f"EMBERDECK_DRIVER_ID={DRIVER_ID}\n"
        "EMBERDECK_DRIVER_CONNECTION=-simulate\n"
    ).encode()
    guest(container, "install", "-d", "-m", "0750", "/etc/emberdeck")
    guest_file(container, "/etc/emberdeck/driver.env", driver_env, "0600")


def create_server_env(container: str, admin_key: str) -> None:
    guest(container, "install", "-d", "-m", "0750", "/etc/emberdeck")
    guest_file(container, "/etc/emberdeck/emberdeck.env", f"EMBERDECK_API_KEY={admin_key}\n".encode())


def verify_version_in_guest(container: str, expected: str) -> None:
    output = guest_text(container, "/usr/local/bin/emberdeck", "-version", timeout=10)
    wanted = f"EmberDeck {expected}"
    if output != wanted:
        raise RuntimeError(f"expected {wanted} in LXC guest, got {output!r}")


def start_services(container: str) -> None:
    guest(container, "systemctl", "enable", SERVER_UNIT, DRIVER_UNIT)
    guest(container, "systemctl", "start", SERVER_UNIT)


def start_driver(container: str) -> None:
    guest(container, "systemctl", "start", DRIVER_UNIT)


def assert_enabled_services(container: str) -> None:
    for unit in (SERVER_UNIT, DRIVER_UNIT):
        enabled = guest_text(container, "systemctl", "is-enabled", unit, check=False)
        if enabled != "enabled":
            raise RuntimeError(f"{unit} is not enabled after guest boot: {enabled!r}")
        wait_service_active(container, unit)


def run_lxc_install_smoke(container: str, previous_binary: pathlib.Path, previous_version: str,
                          previous_checksums: pathlib.Path, current_binary: pathlib.Path,
                          current_version: str, current_checksums: pathlib.Path) -> None:
    previous_binary = previous_binary.resolve()
    current_binary = current_binary.resolve()
    verify_asset_checksum(previous_binary, previous_checksums)
    verify_asset_checksum(current_binary, current_checksums)
    for binary in (previous_binary, current_binary):
        binary.chmod(stat.S_IMODE(binary.stat().st_mode) | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    assert_version(previous_binary, previous_version)
    assert_version(current_binary, current_version)

    admin_key = secrets.token_hex(32)
    install_units(container)
    create_server_env(container, admin_key)
    install_binary(container, previous_binary)
    verify_version_in_guest(container, previous_version)
    start_services(container)
    wait_service_active(container, SERVER_UNIT)
    wait_server(container, admin_key, first_start=True)

    worker_key = create_worker_key(container, admin_key)
    configure_driver(container, worker_key)
    start_driver(container)
    wait_service_active(container, DRIVER_UNIT)
    wait_driver_online(container, admin_key)
    assert_service_supplementary_group(container, DRIVER_UNIT, "dialout")
    verify_dialout_pty_permissions(container)

    previous_job = upload_and_dispatch(container, admin_key, f"lxc-install-{previous_version}.gcode")
    assert_job_persisted(container, admin_key, previous_job, "initial published install")

    # Install from a published release, then upgrade in place over the same data.
    guest(container, "systemctl", "stop", DRIVER_UNIT, SERVER_UNIT)
    install_binary(container, current_binary)
    verify_version_in_guest(container, current_version)
    guest(container, "systemctl", "start", SERVER_UNIT)
    wait_service_active(container, SERVER_UNIT)
    wait_server(container, admin_key)
    guest(container, "systemctl", "start", DRIVER_UNIT)
    wait_service_active(container, DRIVER_UNIT)
    wait_driver_online(container, admin_key)
    assert_job_persisted(container, admin_key, previous_job, "published binary upgrade")
    current_job = upload_and_dispatch(container, admin_key, f"lxc-upgrade-{current_version}.gcode")

    # Verify both systemd units recover from a process crash and the guest boot.
    crash_and_wait_restart(container, SERVER_UNIT)
    wait_server(container, admin_key)
    assert_job_persisted(container, admin_key, current_job, "server crash recovery")
    crash_and_wait_restart(container, DRIVER_UNIT)
    wait_driver_online(container, admin_key, fresh_after=time.time())
    rebooted_job = upload_and_dispatch(container, admin_key, f"lxc-reboot-{current_version}.gcode")
    reboot_started_at = time.time()
    stop_start_guest(container)
    assert_enabled_services(container)
    wait_server(container, admin_key)
    wait_driver_online(container, admin_key, fresh_after=reboot_started_at)
    for job_id in (previous_job, current_job, rebooted_job):
        assert_job_persisted(container, admin_key, job_id, "LXC guest reboot")

    # Uninstall the services and executable while proving durable state remains.
    guest(container, "systemctl", "disable", "--now", DRIVER_UNIT, SERVER_UNIT)
    guest(container, "rm", "-f", "/etc/systemd/system/emberdeck-driver.service",
          "/etc/systemd/system/emberdeck.service", "/etc/emberdeck/driver.env",
          "/etc/emberdeck/emberdeck.env", "/usr/local/bin/emberdeck")
    guest(container, "systemctl", "daemon-reload")
    guest(container, "test", "-s", "/var/lib/emberdeck/jobs.json")
    guest(container, "test", "-s", "/var/lib/emberdeck/credentials.json")
    subprocess.run(["sudo", "lxc-stop", "--name", container, "--kill"], check=True, timeout=30)
    if lxc_state(container) != "STOPPED":
        raise RuntimeError(f"{container} did not stop before host LXC autostart test")
    assert_host_autostart_selected(container)
    start_host_autostart(container)
    print(
        f"PASS LXC published install smoke: {previous_version} -> {current_version}; "
        "auth, simulated dispatch, crash restart, guest reboot persistence, uninstall, and host autostart dispatch"
    )


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", required=True, help="running LXC container name")
    parser.add_argument("--previous-binary", required=True, type=pathlib.Path)
    parser.add_argument("--previous-version", required=True)
    parser.add_argument("--previous-checksums", required=True, type=pathlib.Path)
    parser.add_argument("--current-binary", required=True, type=pathlib.Path)
    parser.add_argument("--current-version", required=True)
    parser.add_argument("--current-checksums", required=True, type=pathlib.Path)
    args = parser.parse_args(argv)
    try:
        run_lxc_install_smoke(
            args.container,
            args.previous_binary,
            args.previous_version,
            args.previous_checksums,
            args.current_binary,
            args.current_version,
            args.current_checksums,
        )
        return 0
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
        print(f"FAIL LXC install smoke: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
