import importlib.util
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "macos_serial_permission_smoke.py"
SPEC = importlib.util.spec_from_file_location("macos_serial_permission_smoke", SCRIPT)
macos_serial_permission_smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(macos_serial_permission_smoke)


class MacOSSerialPermissionSmokeTests(unittest.TestCase):
    def test_worker_command_uses_requested_identity_and_native_macos_pty_path(self):
        command = macos_serial_permission_smoke.worker_command(
            "/repo/bin/emberdeck", "http://127.0.0.1:18080", "worker-key",
            "macos-dac-test", "/dev/ttys001", identity="_www",
        )
        self.assertEqual(command, [
            "sudo", "-u", "_www", "/usr/bin/env", "EMBERDECK_API_KEY=worker-key",
            "/repo/bin/emberdeck", "driver", "-server", "http://127.0.0.1:18080",
            "-id", "macos-dac-test", "-serial", "/dev/ttys001", "-baud", "115200",
            "-serial-dtr", "off", "-serial-rts", "off",
        ])

    def test_settings_error_requires_expected_open_boundary(self):
        macos_serial_permission_smoke.verify_settings_error(
            "serial transport: open permission denied", "permission denied"
        )
        macos_serial_permission_smoke.verify_settings_error(
            "serial transport: open Inappropriate ioctl for device", "inappropriate ioctl for device"
        )

    def test_settings_error_rejects_permission_denial_for_allowed_user(self):
        with self.assertRaisesRegex(RuntimeError, "post-open ioctl failure"):
            macos_serial_permission_smoke.verify_settings_error(
                "permission denied", "inappropriate ioctl for device"
            )

if __name__ == "__main__":
    unittest.main()
