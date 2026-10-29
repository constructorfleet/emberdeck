import importlib.util
import subprocess
import unittest
from pathlib import Path
from unittest.mock import call, patch


SCRIPT = Path(__file__).resolve().parents[1] / "docker_device_permission_smoke.py"
SPEC = importlib.util.spec_from_file_location("docker_device_permission_smoke", SCRIPT)
docker_device_permission_smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(docker_device_permission_smoke)


class DockerDevicePermissionSmokeTests(unittest.TestCase):
    def test_docker_probe_checks_access_without_and_with_supplementary_device_group(self):
        with patch.object(
            docker_device_permission_smoke, "docker_probe",
            side_effect=[
                subprocess.CompletedProcess([], 1, stderr="PermissionError"),
                subprocess.CompletedProcess([], 0),
            ],
        ) as probe:
            docker_device_permission_smoke.assert_docker_access("/tmp/probe", 20)

        self.assertEqual(probe.call_args_list, [
            call("/tmp/probe", 20, include_group=False),
            call("/tmp/probe", 20, include_group=True),
        ])

    def test_docker_probe_requires_permission_denied_without_group(self):
        with (
            patch.object(
                docker_device_permission_smoke, "docker_probe",
                side_effect=[subprocess.CompletedProcess([], 0)],
            ),
            self.assertRaisesRegex(RuntimeError, "expected PermissionError without the device group"),
        ):
            docker_device_permission_smoke.assert_docker_access("/tmp/probe", 20)

    def test_docker_probe_requires_success_with_group(self):
        with (
            patch.object(
                docker_device_permission_smoke, "docker_probe",
                side_effect=[
                    subprocess.CompletedProcess([], 1, stderr="PermissionError"),
                    subprocess.CompletedProcess([], 1, stderr="PermissionError"),
                ],
            ),
            self.assertRaisesRegex(RuntimeError, "could not open the character device with supplementary group 20"),
        ):
            docker_device_permission_smoke.assert_docker_access("/tmp/probe", 20)

    def test_docker_probe_passes_host_device_and_optional_numeric_group(self):
        result = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with patch.object(docker_device_permission_smoke, "docker", return_value=result) as run:
            docker_device_permission_smoke.docker_probe("/tmp/probe", 20, include_group=True)

        args = run.call_args.args
        self.assertEqual(list(args[:5]), ["run", "--rm", "--user", "65532", "--device"])
        self.assertIn(f"/tmp/probe:{docker_device_permission_smoke.DEVICE_IN_CONTAINER}:rw", args)
        self.assertIn("--group-add", args)
        self.assertIn("20", args)
        self.assertEqual(args[-4], docker_device_permission_smoke.HELPER_IMAGE)
        self.assertEqual(args[-3], "python3")
        self.assertIn(docker_device_permission_smoke.DEVICE_IN_CONTAINER, args[-1])
        self.assertEqual(run.call_args.kwargs, {"check": False})

    def test_node_creation_uses_null_device_and_sets_root_dialout_mode(self):
        with patch.object(docker_device_permission_smoke, "run_privileged") as run:
            docker_device_permission_smoke.create_device_node("/tmp/probe", 20)

        self.assertEqual(run.call_args_list, [
            call(["mknod", "/tmp/probe", "c", "1", "3"]),
            call(["chown", "0:20", "/tmp/probe"]),
            call(["chmod", "0660", "/tmp/probe"]),
        ])


if __name__ == "__main__":
    unittest.main()
