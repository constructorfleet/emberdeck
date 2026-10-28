#!/usr/bin/env python3
"""Build and exercise EmberDeck's published container interface."""

import json
import subprocess
import sys
import time
import uuid
import urllib.error
import urllib.request


IMAGE = "emberdeck:ci-smoke"
KEY = "ci-smoke-key-not-for-production"


def docker(*args, check=True):
    return subprocess.run(["docker", *args], check=check, text=True, capture_output=True)


def container_base(name):
    result = docker("port", name, "8080/tcp", check=False)
    output = result.stdout.strip()
    if not output:
        return None
    return "http://" + output.splitlines()[0]


def api(base, method, path, payload=None):
    body = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(
        base + path,
        data=body,
        method=method,
        headers={"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            body = response.read()
            return response.status, json.loads(body) if body else None
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read())
    except OSError:
        return 0, None


def wait_for(description, predicate, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(0.1)
    raise RuntimeError(f"timed out waiting for {description}")


def wait_state(base, job_id, state, timeout=30):
    def check():
        status, job = api(base, "GET", f"/api/v1/jobs/{job_id}")
        if status != 200:
            raise RuntimeError(f"job lookup failed: HTTP {status}: {job}")
        return job if job.get("state") == state else None

    return wait_for(f"job {job_id} to become {state}", check, timeout)


def upload_and_start(base, name, gcode):
    status, job = api(base, "POST", "/api/v1/jobs", {"name": name, "gcode": gcode})
    if status != 201:
        raise RuntimeError(f"upload failed: HTTP {status}: {job}")
    status, assigned = api(
        base,
        "POST",
        f"/api/v1/jobs/{job['id']}/start",
        {"driver_id": "simulator", "confirmed": True, "bounds_override": True},
    )
    if status != 200:
        raise RuntimeError(f"start failed: HTTP {status}: {assigned}")
    return job["id"]


def main():
    name = f"emberdeck-smoke-{uuid.uuid4().hex[:10]}"
    volume = name + "-data"
    container = None
    try:
        docker("build", "--tag", IMAGE, ".")
        docker("volume", "create", volume)
        run = docker(
            "run", "--detach", "--name", name, "--publish", "127.0.0.1::8080",
            "--mount", f"type=volume,source={volume},target=/data",
            "--env", f"EMBERDECK_API_KEY={KEY}", IMAGE,
            "serve", "-listen", "0.0.0.0:8080", "-data", "/data", "-simulate",
        )
        container = run.stdout.strip()
        base = wait_for("container port mapping", lambda: container_base(name))
        wait_for("authenticated API startup", lambda: api(base, "GET", "/api/v1/jobs")[0] == 200)

        complete_id = upload_and_start(base, "smoke-complete.gcode", "G21\nG90\nG1 X10 F100\nM5")
        completed = wait_state(base, complete_id, "completed")
        if completed.get("acked") != 4:
            raise RuntimeError(f"expected four acknowledged lines, got {completed.get('acked')}")

        cancel_id = upload_and_start(base, "smoke-cancel.gcode", "M5\n" * 2000)
        wait_for(
            "simulator progress before cancellation",
            lambda: (lambda result: result[1] if result[0] == 200 and result[1].get("acked", 0) > 0 else None)(
                api(base, "GET", f"/api/v1/jobs/{cancel_id}")
            ),
        )
        status, canceled = api(base, "POST", f"/api/v1/jobs/{cancel_id}/cancel", {})
        if status != 200 or canceled.get("state") != "canceling":
            raise RuntimeError(f"cancel request failed: HTTP {status}: {canceled}")
        wait_state(base, cancel_id, "canceled")

        docker("restart", name)
        base = wait_for("restarted container port mapping", lambda: container_base(name))
        wait_for("restarted API", lambda: api(base, "GET", "/api/v1/jobs")[0] == 200)
        status, jobs = api(base, "GET", "/api/v1/jobs")
        if status != 200 or not {job["id"] for job in jobs}.issuperset({complete_id, cancel_id}):
            raise RuntimeError("uploaded jobs were not retained across container restart")
        print("Container smoke passed: non-root /data persistence, simulated execution, and cancellation.")
    except Exception:
        if container:
            state = docker("inspect", "--format", "{{json .State}}", name, check=False)
            print(f"Container state:\n{state.stdout or state.stderr}", file=sys.stderr)
            logs = docker("logs", "--tail", "100", name, check=False)
            if logs.stdout:
                print(f"Container stdout:\n{logs.stdout}", file=sys.stderr)
            if logs.stderr:
                print(f"Container stderr:\n{logs.stderr}", file=sys.stderr)
        raise
    finally:
        docker("rm", "--force", name, check=False)
        docker("volume", "rm", "--force", volume, check=False)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"container smoke failed: {error}", file=sys.stderr)
        sys.exit(1)
