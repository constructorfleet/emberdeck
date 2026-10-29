import importlib.util
import subprocess
import unittest
from pathlib import Path
from unittest.mock import call, patch


SCRIPT = Path(__file__).resolve().parents[1] / "docker_pty_permission_smoke.py"
SPEC = importlib.util.spec_from_file_location("docker_pty_permission_smoke", SCRIPT)
docker_pty_permission_smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(docker_pty_permission_smoke)


class DockerPtyPermissionSmokeTests(unittest.TestCase):
    def test_docker_probe_checks_access_without_and_with_supplementary_device_group(self):
        with patch.object(
            docker_pty_permission_smoke, "docker_probe",
            side_effect=[subprocess.CompletedProcess([], 13), subprocess.CompletedProcess([], 0)],
        ) as probe:
            docker_pty_permission_smoke.assert_docker_access("/dev/pts/7", 20)

        self.assertEqual(probe.call_args_list, [
            call("/dev/pts/7", 20, include_group=False),
            call("/dev/pts/7", 20, include_group=True),
        ])

    def test_docker_probe_requires_permission_denied_without_group(self):
        with (
            patch.object(
                docker_pty_permission_smoke, "docker_probe",
                side_effect=[subprocess.CompletedProcess([], 0)],
            ),
            self.assertRaisesRegex(RuntimeError, "expected EACCES without the device group"),
        ):
            docker_pty_permission_smoke.assert_docker_access("/dev/pts/7", 20)

    def test_docker_probe_requires_success_with_group(self):
        with (
            patch.object(
                docker_pty_permission_smoke, "docker_probe",
                side_effect=[subprocess.CompletedProcess([], 13), subprocess.CompletedProcess([], 13)],
            ),
            self.assertRaisesRegex(RuntimeError, "could not open the PTY with supplementary group 20"),
        ):
            docker_pty_permission_smoke.assert_docker_access("/dev/pts/7", 20)

    def test_docker_probe_passes_host_device_and_optional_numeric_group(self):
        result = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with patch.object(docker_pty_permission_smoke.subprocess, "run", return_value=result) as run:
            docker_pty_permission_smoke.docker_probe("/dev/pts/7", 20, include_group=True)

        args = run.call_args.args[0]
        self.assertEqual(args[:5], [
            "docker", "run", "--rm", "--user", "65532",
        ])
        self.assertIn("/dev/pts/7:/dev/emberdeck-probe:rw", args)
        self.assertIn("--group-add", args)
        self.assertIn("20", args)
        self.assertEqual(args[-4], docker_pty_permission_smoke.HELPER_IMAGE)
        self.assertEqual(args[-3], "python3")
        self.assertIn("/dev/emberdeck-probe", args[-1])
        self.assertEqual(run.call_args.kwargs, {"check": False, "capture_output": True, "text": True})

    def test_device_permission_setup_uses_root_dialout_and_mode_0660(self):
        with patch.object(docker_pty_permission_smoke, "run_privileged") as run:
            docker_pty_permission_smoke.set_device_permissions("/dev/pts/7", 20)

        self.assertEqual(run.call_args_list, [
            call(["chown", "0:20", "/dev/pts/7"]),
            call(["chmod", "0660", "/dev/pts/7"]),
        ])


if __name__ == "__main__":
    unittest.main()
