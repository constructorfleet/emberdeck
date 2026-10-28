#!/usr/bin/env python3
"""Exercise a supplied EmberDeck binary through its public HTTP and GRBL wires."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request


KEY = "artifact-smoke-only-key-do-not-use-outside-this-test"
PROGRAM = "G1 X12 F100\n"
DEADLINE = 18.0
HEARTBEAT_DEADLINE = 8.0


class SmokeFailure(RuntimeError):
    pass


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_for(predicate, description: str, timeout: float = DEADLINE, health=None):
    end = time.monotonic() + timeout
    last = None
    while time.monotonic() < end:
        try:
            if health:
                health()
            last = predicate()
            if last:
                return last
        except (OSError, urllib.error.URLError, TimeoutError, json.JSONDecodeError):
            pass
        time.sleep(0.05)
    raise SmokeFailure(f"timed out waiting for {description}; last value: {last!r}")


class FakeGRBL:
    """A deterministic TCP GRBL peer; ok means accepted, Run persists to drain."""

    def __init__(self, port: int):
        self.port = port
        self.events: list[str] = []
        self.lock = threading.Lock()
        self.ready = threading.Event()
        self.closed = threading.Event()
        self.error: BaseException | None = None
        self.thread = threading.Thread(target=self._serve, daemon=True)
        self.thread.start()
        if not self.ready.wait(5):
            raise SmokeFailure("fake GRBL TCP listener did not start")
        self.check_error()

    def check_error(self):
        if self.error is not None:
            raise SmokeFailure(f"fake GRBL peer failed: {self.error}")

    def record(self, event: str):
        with self.lock:
            self.events.append(event)

    def snapshot(self) -> list[str]:
        self.check_error()
        with self.lock:
            return list(self.events)

    def _serve(self):
        state = "Idle"
        resume_queries = 0
        resume_active = False
        try:
            with socket.socket() as listener:
                listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                listener.bind(("127.0.0.1", self.port))
                listener.listen(1)
                listener.settimeout(10)
                self.ready.set()
                conn, _ = listener.accept()
                with conn:
                    conn.settimeout(0.2)
                    buf = bytearray()
                    while not self.closed.is_set():
                        try:
                            data = conn.recv(4096)
                        except socket.timeout:
                            continue
                        if not data:
                            break
                        for byte in data:
                            if byte == ord("?"):
                                if resume_active:
                                    resume_queries += 1
                                    state = "Run" if resume_queries == 1 else "Idle"
                                self.record(f"query:{state}")
                                conn.sendall(f"<{state}|MPos:0,0,0>\r\n".encode())
                            elif byte == ord("!"):
                                state = "Hold:0"
                                resume_active = False
                                resume_queries = 0
                                self.record("hold")
                            elif byte == ord("~"):
                                state = "Run"
                                resume_active = True
                                resume_queries = 0
                                self.record("resume")
                            elif byte == 0x18:
                                self.record("reset")
                            elif byte == 10:
                                line = bytes(buf).decode("ascii", "replace").strip()
                                buf.clear()
                                if line:
                                    self.record(f"gcode:{line}")
                                    # GRBL's ok is only acceptance. The programmed
                                    # motion remains in Run until the operator's
                                    # confirmed pause/resume sequence drains it.
                                    state = "Run"
                                    conn.sendall(b"ok\r\n")
                                    self.record(f"accepted:{line}")
                            elif byte != 13:
                                buf.append(byte)
        except BaseException as exc:
            if not self.closed.is_set():
                self.error = exc
        finally:
            self.ready.set()

    def close(self):
        self.closed.set()
        self.thread.join(timeout=2)


def request(base: str, path: str, method: str = "GET", body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(
        base + path,
        data=data,
        method=method,
        headers={"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=3) as response:
            payload = response.read()
            return response.status, json.loads(payload) if payload else None
    except urllib.error.HTTPError as exc:
        payload = exc.read()
        try:
            detail = json.loads(payload)
        except json.JSONDecodeError:
            detail = payload.decode(errors="replace")
        raise SmokeFailure(f"HTTP {method} {path}: {exc.code} {detail}") from exc


def assert_version(binary: pathlib.Path, expected: str):
    completed = subprocess.run(
        [str(binary), "-version"], capture_output=True, text=True, timeout=8
    )
    output = (completed.stdout + completed.stderr).strip()
    if completed.returncode != 0 or output != f"EmberDeck {expected}":
        raise SmokeFailure(
            f"binary version mismatch: expected 'EmberDeck {expected}', got {output!r}"
        )


def run(binary: pathlib.Path, expected_version: str):
    binary = binary.resolve()
    if not binary.is_file():
        raise SmokeFailure(f"binary does not exist: {binary}")
    assert_version(binary, expected_version)
    tcp_port = free_port()
    peer = FakeGRBL(tcp_port)
    http_port = free_port()
    with tempfile.TemporaryDirectory(prefix="emberdeck-artifact-") as temp:
        data_dir = pathlib.Path(temp) / "data"
        env = os.environ.copy()
        env["EMBERDECK_API_KEY"] = KEY
        process = subprocess.Popen(
            [
                str(binary), "serve", "-tcp", f"127.0.0.1:{tcp_port}",
                "-listen", f"127.0.0.1:{http_port}", "-data", str(data_dir),
            ],
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        base = f"http://127.0.0.1:{http_port}"
        try:
            wait_for(lambda: request(base, "/api/v1/drivers")[0] == 200, "HTTP API startup", health=peer.check_error)
            def discovered_local():
                _, drivers = request(base, "/api/v1/drivers")
                return next((item for item in drivers if item.get("id") == "local"), None)

            driver = wait_for(discovered_local, "local worker registration", health=peer.check_error)
            if not driver or "pause_resume" not in driver.get("capabilities", []):
                raise SmokeFailure(f"local driver did not advertise pause_resume: {driver!r}")

            status, job = request(
                base, "/api/v1/jobs", "POST", {"name": "artifact-smoke.gcode", "gcode": PROGRAM}
            )
            if status != 201:
                raise SmokeFailure(f"upload returned HTTP {status}")
            job_id = job["id"]
            request(
                base,
                f"/api/v1/jobs/{job_id}/start",
                "POST",
                {"driver_id": "local", "confirmed": True, "bounds_override": True},
            )

            def running_and_acked():
                _, current = request(base, f"/api/v1/jobs/{job_id}")
                return current if current.get("state") == "running" and current.get("acked") == 1 and current.get("controller_state") == "Run" else None

            try:
                wait_for(running_and_acked, "source line acknowledged while controller remains Run", health=peer.check_error)
            except SmokeFailure as exc:
                raise SmokeFailure(f"{exc}; GRBL trace: {peer.snapshot()!r}") from exc
            wait_for(
                lambda: [event for event in peer.snapshot() if event.startswith("accepted:")]
                == ["accepted:G1 X12 F100", "accepted:M5"],
                "source and cleanup lines accepted by GRBL",
                health=peer.check_error,
            )
            source_before = [event for event in peer.snapshot() if event.startswith("gcode:")]
            if source_before != ["gcode:G1 X12 F100", "gcode:M5"]:
                raise SmokeFailure(f"expected one source line and one cleanup M5 before pause, got {source_before!r}")

            request(base, f"/api/v1/jobs/{job_id}/pause", "POST", {})

            def paused():
                _, current = request(base, f"/api/v1/jobs/{job_id}")
                return current if current.get("state") == "paused" and current.get("controller_state") == "Hold:0" else None

            held = wait_for(paused, "confirmed Hold:0 after pause", health=peer.check_error)
            # Start after confirmation, then observe two later worker heartbeats.
            _, drivers = request(base, "/api/v1/drivers")
            local = next((item for item in drivers if item.get("id") == "local"), None)
            if not local or not local.get("last_seen"):
                raise SmokeFailure(f"local worker has no heartbeat timestamp: {drivers!r}")
            baseline = local["last_seen"]
            seen = set()
            heartbeat_end = time.monotonic() + HEARTBEAT_DEADLINE
            while len(seen) < 2 and time.monotonic() < heartbeat_end:
                peer.check_error()
                _, current = request(base, f"/api/v1/jobs/{job_id}")
                if current.get("state") != "paused" or current.get("controller_state") != "Hold:0":
                    raise SmokeFailure(f"paused state changed before resume: {current!r}")
                if current.get("acked") != held.get("acked") or current.get("total") != held.get("total"):
                    raise SmokeFailure(f"paused progress changed across heartbeats: {current!r}")
                _, latest = request(base, "/api/v1/drivers")
                local = next((item for item in latest if item.get("id") == "local"), None)
                marker = local.get("last_seen") if local else None
                if marker and marker != baseline:
                    seen.add(marker)
                time.sleep(0.2)
            if len(seen) < 2:
                raise SmokeFailure(f"paused worker did not send two heartbeats after {baseline}: {sorted(seen)!r}")
            if held.get("acked") != 1 or held.get("total") != 1:
                raise SmokeFailure(f"pause lost acknowledged source progress: {held!r}")

            request(base, f"/api/v1/jobs/{job_id}/resume", "POST", {"confirmed": True})
            wait_for(lambda: "resume" in peer.snapshot(), "GRBL resume realtime byte", health=peer.check_error)

            def complete():
                _, current = request(base, f"/api/v1/jobs/{job_id}")
                return current if current.get("state") == "completed" else None

            try:
                result = wait_for(complete, "completion after fresh post-resume Run then Idle", health=peer.check_error)
            except SmokeFailure as exc:
                _, current = request(base, f"/api/v1/jobs/{job_id}")
                raise SmokeFailure(f"{exc}; current job: {current!r}; GRBL trace: {peer.snapshot()!r}") from exc
            final_events = [event for event in peer.snapshot() if event.startswith("gcode:")]
            if final_events != source_before:
                raise SmokeFailure(f"source or cleanup was replayed after control: {final_events!r}")
            events = peer.snapshot()
            if events.count("hold") != 1 or events.count("resume") != 1 or "reset" in events:
                raise SmokeFailure(f"expected exactly one hold/resume and no reset: {events!r}")
            if result.get("acked") != 1 or result.get("total") != 1 or result.get("controller_state") != "Idle":
                raise SmokeFailure(f"unexpected terminal receipt: {result!r}")
            print(
                "PASS artifact smoke: version=%s source_acked=%s/%s wire=%s"
                % (expected_version, result["acked"], result["total"], ",".join(events))
            )
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=4)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=2)
            peer.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=pathlib.Path)
    parser.add_argument("--expected-version", required=True)
    args = parser.parse_args()
    try:
        run(args.binary, args.expected_version)
    except (SmokeFailure, OSError, subprocess.SubprocessError) as exc:
        print(f"FAIL artifact smoke: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
