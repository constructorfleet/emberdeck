import importlib.util
import json
import subprocess
import unittest
from pathlib import Path
from unittest.mock import MagicMock, call, patch


SCRIPT = Path(__file__).resolve().parents[1] / "docker_compose_serial_smoke.py"
SPEC = importlib.util.spec_from_file_location("docker_compose_serial_smoke", SCRIPT)
docker_compose_serial_smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(docker_compose_serial_smoke)


class DockerComposeSerialSmokeTests(unittest.TestCase):
    def test_compose_command_selects_serial_profile_and_repo_compose_file(self):
        command = docker_compose_serial_smoke.compose_command("emberdeck-serial-test", "config")
        self.assertEqual(command, [
            "docker", "compose", "--project-name", "emberdeck-serial-test", "-f",
            str(docker_compose_serial_smoke.REPO_ROOT / "compose.yaml"), "--profile", "serial", "config",
        ])

    def test_serial_environment_maps_all_contract_values(self):
        self.assertEqual(
            docker_compose_serial_smoke.serial_environment(
                device="/dev/null", gid=0, key="worker-key", image="emberdeck:test"
            ),
            {
                "EMBERDECK_API_KEY": "worker-key",
                "EMBERDECK_DRIVER_API_KEY": "worker-key",
                "EMBERDECK_SERVER_URL": "http://emberdeck:8080",
                "EMBERDECK_DRIVER_ID": "compose-serial-smoke",
                "EMBERDECK_SERIAL_DEVICE": "/dev/null",
                "EMBERDECK_SERIAL_GID": "0",
                "EMBERDECK_SERIAL_BAUD": "115200",
                "EMBERDECK_SERIAL_DTR": "off",
                "EMBERDECK_SERIAL_RTS": "off",
                "EMBERDECK_SERIAL_IMAGE": "emberdeck:test",
            },
        )

    def test_config_requires_profile_device_gid_key_and_serial_flags(self):
        config = {
            "services": {
                "serial-driver": {
                    "profiles": ["serial"],
                    "devices": [{"source": "/dev/null", "target": "/dev/null", "permissions": "rw"}],
                    "group_add": ["0"],
                    "environment": {"EMBERDECK_API_KEY": "worker-key"},
                    "image": "emberdeck:test",
                    "command": [
                        "driver", "-server", "http://emberdeck:8080", "-id", "compose-serial-smoke",
                        "-serial", "/dev/null", "-baud", "115200", "-serial-dtr", "off", "-serial-rts", "off",
                    ],
                },
            },
        }
        docker_compose_serial_smoke.verify_serial_service_config(
            config, "/dev/null", 0, "worker-key", "emberdeck:test"
        )

    def test_config_rejects_missing_device_mapping(self):
        config = {"services": {"serial-driver": {
            "profiles": ["serial"], "devices": [], "group_add": ["0"], "command": [], "environment": {},
        }}}
        with self.assertRaisesRegex(RuntimeError, "does not map serial device"):
            docker_compose_serial_smoke.verify_serial_service_config(
                config, "/dev/null", 0, "worker-key", "emberdeck:test"
            )

    def test_config_rejects_missing_numeric_supplementary_group(self):
        config = {"services": {"serial-driver": {
            "profiles": ["serial"],
            "devices": [{"source": "/dev/null", "target": "/dev/null", "permissions": "rw"}],
            "group_add": [], "command": [], "environment": {},
        }}}
        with self.assertRaisesRegex(RuntimeError, "did not add serial device GID 0"):
            docker_compose_serial_smoke.verify_serial_service_config(
                config, "/dev/null", 0, "worker-key", "emberdeck:test"
            )

    def test_cleanup_removes_the_temporary_compose_project_and_image(self):
        env = docker_compose_serial_smoke.serial_environment(device="/dev/null", gid=0)
        result = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with (
            patch.object(docker_compose_serial_smoke.subprocess, "run", return_value=result) as run,
            patch.object(docker_compose_serial_smoke, "docker", side_effect=[result, result]) as docker,
        ):
            docker_compose_serial_smoke.cleanup_compose_project(
                "test-project", env, "emberdeck:test", "/tmp/ports.yaml"
            )

        self.assertEqual(run.call_args.args[0], docker_compose_serial_smoke.compose_command(
            "test-project", "down", "--remove-orphans", "--volumes", override_file="/tmp/ports.yaml"
        ))
        self.assertEqual(run.call_args.kwargs, {
            "check": False, "text": True, "capture_output": True, "env": env,
        })
        self.assertEqual(docker.call_args_list, [
            call("image", "inspect", "emberdeck:test", check=False),
            call("image", "rm", "--force", "emberdeck:test", check=False),
        ])

    def test_settings_error_distinguishes_device_permission_from_post_open_ioctl(self):
        docker_compose_serial_smoke.verify_settings_error(
            'worker: open controller for settings read: serial transport: open "/tmp/probe": '
            'permission denied', "permission denied"
        )
        docker_compose_serial_smoke.verify_settings_error(
            'worker: open controller for settings read: serial transport: open "/tmp/probe": '
            'inappropriate ioctl for device', "inappropriate ioctl for device"
        )

    def test_settings_error_rejects_a_different_failure_boundary(self):
        with self.assertRaisesRegex(RuntimeError, "expected post-open ioctl failure"):
            docker_compose_serial_smoke.verify_settings_error("permission denied", "inappropriate ioctl for device")

    def test_worker_credential_helper_returns_the_one_time_secret(self):
        response = MagicMock()
        response.__enter__.return_value.read.return_value = json.dumps({"key": "scoped-worker-key"}).encode()
        with patch.object(docker_compose_serial_smoke, "urlopen", return_value=response) as open_url:
            key = docker_compose_serial_smoke.create_worker_credential(
                "http://127.0.0.1:18080", "admin-key", "bench"
            )
        self.assertEqual(key, "scoped-worker-key")
        request = open_url.call_args.args[0]
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.full_url, "http://127.0.0.1:18080/api/v1/credentials")
        self.assertEqual(json.loads(request.data), {
            "name": "Compose serial smoke worker", "role": "worker", "driver_ids": ["bench"],
        })

    def test_wait_for_settings_error_retries_until_expected_boundary(self):
        with (
            patch.object(docker_compose_serial_smoke, "read_driver_settings_error", side_effect=[None, "permission denied"]),
            patch.object(docker_compose_serial_smoke.time, "sleep"),
        ):
            result = docker_compose_serial_smoke.wait_for_settings_error(
                "http://127.0.0.1:18080", "admin-key", "bench", "permission denied", timeout=1
            )
        self.assertEqual(result, "permission denied")

    def test_device_node_creation_sets_root_group_and_mode_0660(self):
        with patch.object(docker_compose_serial_smoke, "run_privileged") as run:
            docker_compose_serial_smoke.create_device_node("/tmp/controller", 20)
        self.assertEqual(run.call_args_list, [
            call(["mknod", "/tmp/controller", "c", "1", "3"]),
            call(["chown", "0:20", "/tmp/controller"]),
            call(["chmod", "0660", "/tmp/controller"]),
        ])

    def test_compose_command_includes_optional_ports_override(self):
        command = docker_compose_serial_smoke.compose_command(
            "emberdeck-serial-test", "up", "--detach", "emberdeck", override_file="/tmp/ports.yaml"
        )
        self.assertEqual(command[4:6], ["-f", str(docker_compose_serial_smoke.REPO_ROOT / "compose.yaml")])
        self.assertEqual(command[6:8], ["-f", "/tmp/ports.yaml"])


if __name__ == "__main__":
    unittest.main()
