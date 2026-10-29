#!/usr/bin/env python3
"""Build and exercise EmberDeck's published container interface."""

import argparse
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


def api(base, method, path, payload=None, authenticated=True):
    body = None if payload is None else json.dumps(payload).encode()
    headers = {"Content-Type": "application/json"}
    if authenticated:
        headers["Authorization"] = f"Bearer {KEY}"
    request = urllib.request.Request(base + path, data=body, method=method, headers=headers)
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


def verify_image_version(image, version):
    result = docker("run", "--rm", image, "-version")
    actual = (result.stdout or "").strip()
    expected = f"EmberDeck {version}"
    if result.returncode != 0 or actual != expected:
        raise RuntimeError(f"expected {expected} from image {image}, got {actual!r}")


def start_container(name, volume, image):
    run = docker(
        "run", "--detach", "--name", name, "--publish", "127.0.0.1::8080",
        "--mount", f"type=volume,source={volume},target=/data",
        "--env", f"EMBERDECK_API_KEY={KEY}", image,
        "serve", "-listen", "0.0.0.0:8080", "-data", "/data", "-simulate",
    )
    return run.stdout.strip()


def await_container_api(name):
    base = wait_for("container port mapping", lambda: container_base(name))
    wait_for("unauthenticated API rejection", lambda: api(base, "GET", "/api/v1/jobs", authenticated=False)[0] == 401)
    wait_for("authenticated API startup", lambda: api(base, "GET", "/api/v1/jobs")[0] == 200)
    return base


def assert_jobs_persisted(base, job_ids, stage):
    status, page = api(base, "GET", "/api/v1/jobs")
    if status != 200 or not {job["id"] for job in page["jobs"]}.issuperset(job_ids):
        raise RuntimeError(f"uploaded jobs were not retained {stage}")


def run_published_image_smoke(previous_image, previous_version, current_image, current_version):
    """Exercise two published images through installation, upgrade and cleanup."""
    verify_image_version(previous_image, previous_version)
    verify_image_version(current_image, current_version)

    name = f"emberdeck-smoke-{uuid.uuid4().hex[:10]}"
    volume = name + "-data"
    container = None
    try:
        docker("volume", "create", volume)
        container = start_container(name, volume, previous_image)
        base = await_container_api(name)

        previous_job = upload_and_start(base, f"install-{previous_version}.gcode", "G21\nG90\nG1 X10 F100\nM5")
        completed = wait_state(base, previous_job, "completed")
        if completed.get("acked") != 4:
            raise RuntimeError(f"expected four acknowledged lines, got {completed.get('acked')}")

        docker("restart", name)
        base = wait_for("restarted container port mapping", lambda: container_base(name))
        wait_for("restarted API", lambda: api(base, "GET", "/api/v1/jobs")[0] == 200)
        assert_jobs_persisted(base, {previous_job}, "container restart")

        # A released image upgrade replaces the container while retaining its volume.
        docker("rm", "--force", name)
        container = start_container(name, volume, current_image)
        base = await_container_api(name)
        assert_jobs_persisted(base, {previous_job}, "image upgrade")

        current_job = upload_and_start(base, f"install-{current_version}.gcode", "G21\nG90\nG1 X12 F100\nM5")
        completed = wait_state(base, current_job, "completed")
        if completed.get("acked") != 4:
            raise RuntimeError(f"expected four acknowledged lines after upgrade, got {completed.get('acked')}")
        docker("restart", name)
        base = wait_for("upgraded container restart", lambda: container_base(name))
        wait_for("upgraded API", lambda: api(base, "GET", "/api/v1/jobs")[0] == 200)
        assert_jobs_persisted(base, {previous_job, current_job}, "upgraded container restart")
        print(f"Published container smoke passed: {previous_version} -> {current_version}; authenticated dispatch, upgrade, restart persistence, and uninstall.")
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


def run_local_image_smoke():
    """Build and exercise the current source checkout as a local container."""
    name = f"emberdeck-smoke-{uuid.uuid4().hex[:10]}"
    volume = name + "-data"
    container = None
    try:
        docker("build", "--tag", IMAGE, ".")
        docker("volume", "create", volume)
        container = start_container(name, volume, IMAGE)
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
        assert_jobs_persisted(base, {complete_id, cancel_id}, "container restart")
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


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--previous-image", help="previous published container image")
    parser.add_argument("--previous-version", help="expected version in the previous image")
    parser.add_argument("--current-image", help="current published container image")
    parser.add_argument("--current-version", help="expected version in the current image")
    args = parser.parse_args(argv)
    published = (args.previous_image, args.previous_version, args.current_image, args.current_version)
    if any(published) and not all(published):
        parser.error("published-image mode requires all four --previous/current-image/version options")
    if all(published):
        run_published_image_smoke(*published)
    else:
        run_local_image_smoke()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"container smoke failed: {error}", file=sys.stderr)
        sys.exit(1)
