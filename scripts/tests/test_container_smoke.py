import subprocess
import sys
import importlib.util
import unittest
from pathlib import Path
from unittest.mock import call, patch

SCRIPT = Path(__file__).resolve().parents[1] / "container-smoke.py"
SPEC = importlib.util.spec_from_file_location("container_smoke", SCRIPT)
container_smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(container_smoke)


class PublishedContainerSmokeTests(unittest.TestCase):
    def test_published_smoke_runs_previous_then_current_image_on_the_same_volume(self):
        docker_calls = []

        def fake_docker(*args, check=True):
            docker_calls.append(args)
            if args[:2] == ("run", "--rm"):
                image = args[2]
                version = image.rsplit(":", 1)[-1]
                return subprocess.CompletedProcess(args, 0, stdout=f"EmberDeck {version}\n", stderr="")
            if args[:2] == ("run", "--detach"):
                return subprocess.CompletedProcess(args, 0, stdout="container-id\n", stderr="")
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        api_calls = []

        def fake_api(base, method, path, payload=None, authenticated=True):
            api_calls.append((base, method, path, payload, authenticated))
            if path == "/api/v1/jobs" and not authenticated:
                return 401, {"error": "unauthorized"}
            return 200, {"jobs": [{"id": "old-job"}, {"id": "new-job"}]}

        with (
            patch.object(container_smoke, "docker", side_effect=fake_docker),
            patch.object(container_smoke, "container_base", return_value="http://127.0.0.1:1234"),
            patch.object(container_smoke, "api", side_effect=fake_api),
            patch.object(container_smoke, "upload_and_start", side_effect=["old-job", "new-job"]) as upload,
            patch.object(container_smoke, "wait_state", return_value={"state": "completed", "acked": 4}),
        ):
            container_smoke.run_published_image_smoke(
                "ghcr.io/constructorfleet/emberdeck:v1.2.0", "v1.2.0",
                "ghcr.io/constructorfleet/emberdeck:v1.3.0", "v1.3.0",
            )

        image_checks = [entry for entry in docker_calls if entry[:2] == ("run", "--rm")]
        self.assertEqual(
            image_checks,
            [
                ("run", "--rm", "ghcr.io/constructorfleet/emberdeck:v1.2.0", "-version"),
                ("run", "--rm", "ghcr.io/constructorfleet/emberdeck:v1.3.0", "-version"),
            ],
        )
        container_runs = [entry for entry in docker_calls if entry[:2] == ("run", "--detach")]
        self.assertEqual(len(container_runs), 2)
        self.assertIn("ghcr.io/constructorfleet/emberdeck:v1.2.0", container_runs[0])
        self.assertIn("ghcr.io/constructorfleet/emberdeck:v1.3.0", container_runs[1])
        old_mount = container_runs[0][container_runs[0].index("--mount") + 1]
        new_mount = container_runs[1][container_runs[1].index("--mount") + 1]
        self.assertEqual(old_mount, new_mount)
        upload.assert_has_calls(
            [
                call("http://127.0.0.1:1234", "install-v1.2.0.gcode", "G21\nG90\nG1 X10 F100\nM5"),
                call("http://127.0.0.1:1234", "install-v1.3.0.gcode", "G21\nG90\nG1 X12 F100\nM5"),
            ]
        )
        self.assertIn(("http://127.0.0.1:1234", "GET", "/api/v1/jobs", None, False), api_calls)
        self.assertIn(("http://127.0.0.1:1234", "GET", "/api/v1/jobs", None, True), api_calls)
        self.assertIn(("restart", container_runs[0][container_runs[0].index("--name") + 1]), docker_calls)
        self.assertIn(("volume", "rm", "--force", old_mount.split("source=")[1].split(",")[0]), docker_calls)

    def test_image_version_mismatch_fails_before_startup(self):
        result = subprocess.CompletedProcess([], 0, stdout="EmberDeck v0.0.0\n", stderr="")
        with (
            patch.object(container_smoke, "docker", return_value=result) as run,
            self.assertRaisesRegex(RuntimeError, "expected EmberDeck v1.2.0"),
        ):
            container_smoke.verify_image_version("ghcr.io/constructorfleet/emberdeck:v1.2.0", "v1.2.0")
        run.assert_called_once_with("run", "--rm", "ghcr.io/constructorfleet/emberdeck:v1.2.0", "-version")

    def test_cli_requires_both_published_image_refs_and_versions(self):
        with (
            patch.object(sys, "argv", ["container-smoke.py", "--previous-image", "old"]),
            patch.object(container_smoke, "run_published_image_smoke") as smoke,
            self.assertRaises(SystemExit),
        ):
            container_smoke.main()
        smoke.assert_not_called()

    def test_cli_wires_published_image_refs_and_versions(self):
        argv = [
            "container-smoke.py",
            "--previous-image", "ghcr.io/emberdeck:v1.2.0",
            "--previous-version", "v1.2.0",
            "--current-image", "ghcr.io/emberdeck:v1.3.0",
            "--current-version", "v1.3.0",
        ]
        with (
            patch.object(sys, "argv", argv),
            patch.object(container_smoke, "run_published_image_smoke") as smoke,
        ):
            container_smoke.main()
        smoke.assert_called_once_with(
            "ghcr.io/emberdeck:v1.2.0", "v1.2.0",
            "ghcr.io/emberdeck:v1.3.0", "v1.3.0",
        )


if __name__ == "__main__":
    unittest.main()
