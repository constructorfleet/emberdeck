import errno
import importlib.util
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import patch


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

    def test_compose_run_executes_image_version_with_profile_environment(self):
        env = docker_compose_serial_smoke.serial_environment(device="/dev/null", gid=0)
        expected = subprocess.CompletedProcess([], 0, stdout="EmberDeck dev\n", stderr="")
        with patch.object(docker_compose_serial_smoke, "compose", return_value=expected) as run:
            result = docker_compose_serial_smoke.run_image_version("test-project", env)
        self.assertEqual(result, "EmberDeck dev")
        self.assertEqual(run.call_args.args, (
            "test-project", "run", "--rm", "--no-deps", "serial-driver", "-version",
        ))
        self.assertEqual(run.call_args.kwargs, {"env": env})

    def test_cleanup_removes_the_temporary_compose_project_and_image(self):
        env = docker_compose_serial_smoke.serial_environment(device="/dev/null", gid=0)
        result = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with (
            patch.object(docker_compose_serial_smoke.subprocess, "run", return_value=result) as run,
            patch.object(docker_compose_serial_smoke, "docker", return_value=result) as docker,
        ):
            docker_compose_serial_smoke.cleanup_compose_project("test-project", env, "emberdeck:test")

        self.assertEqual(run.call_args.args[0], docker_compose_serial_smoke.compose_command(
            "test-project", "down", "--remove-orphans"
        ))
        self.assertEqual(run.call_args.kwargs, {
            "check": False, "text": True, "capture_output": True, "env": env,
        })
        docker.assert_called_once_with("image", "rm", "--force", "emberdeck:test", check=False)

    def test_actual_pty_modem_ioctl_is_unsupported_on_this_linux_kernel(self):
        if not sys.platform.startswith("linux"):
            self.skipTest("Linux PTY modem ioctl behavior")
        self.assertEqual(docker_compose_serial_smoke.confirm_pty_lacks_modem_ioctl(), errno.ENOTTY)


if __name__ == "__main__":
    unittest.main()
