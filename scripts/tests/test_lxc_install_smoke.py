import hashlib
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import call, patch


SCRIPT = Path(__file__).resolve().parents[1] / "lxc_install_smoke.py"
SPEC = importlib.util.spec_from_file_location("lxc_install_smoke", SCRIPT)
lxc_install_smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(lxc_install_smoke)


class PublishedLxcInstallSmokeTests(unittest.TestCase):
    def test_release_checksum_is_verified_by_asset_basename(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            asset = root / "emberdeck-linux-amd64"
            content = b"published binary contents"
            asset.write_bytes(content)
            checksum = hashlib.sha256(content).hexdigest()
            manifest = root / "SHA256SUMS"
            manifest.write_text(f"{checksum}  emberdeck-linux-amd64\n", encoding="utf-8")

            lxc_install_smoke.verify_asset_checksum(asset, manifest)

    def test_checksum_mismatch_fails_before_guest_mutation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            asset = root / "emberdeck-linux-amd64"
            asset.write_bytes(b"unexpected")
            manifest = root / "SHA256SUMS"
            manifest.write_text(f"{'0' * 64}  {asset.name}\n", encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                lxc_install_smoke.verify_asset_checksum(asset, manifest)

    def test_published_binary_must_report_expected_release_version(self):
        result = subprocess.CompletedProcess([], 0, stdout="EmberDeck v1.2.0\n", stderr="")
        with patch.object(lxc_install_smoke.subprocess, "run", return_value=result) as run:
            lxc_install_smoke.assert_version(Path("emberdeck-linux-amd64"), "v1.2.0")
        run.assert_called_once_with(
            ["emberdeck-linux-amd64", "-version"], capture_output=True, text=True, timeout=10
        )

    def test_version_mismatch_fails(self):
        result = subprocess.CompletedProcess([], 0, stdout="EmberDeck v1.2.0\n", stderr="")
        with (
            patch.object(lxc_install_smoke.subprocess, "run", return_value=result),
            self.assertRaisesRegex(RuntimeError, "expected EmberDeck v1.3.0"),
        ):
            lxc_install_smoke.assert_version(Path("emberdeck-linux-amd64"), "v1.3.0")

    def test_guest_command_runs_as_guest_root_and_streams_files_over_stdin(self):
        result = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with patch.object(lxc_install_smoke.subprocess, "run", return_value=result) as run:
            actual = lxc_install_smoke.guest("smoke", "sh", "-c", "cat > /tmp/unit", input=b"unit")

        self.assertEqual(actual.returncode, 0)
        run.assert_called_once_with(
            ["sudo", "lxc-attach", "--name", "smoke", "--", "sh", "-c", "cat > /tmp/unit"],
            input=b"unit",
            capture_output=True,
            text=False,
            timeout=30,
            check=True,
        )

    def test_api_call_uses_guest_network_namespace_and_parses_status_and_json(self):
        pid = subprocess.CompletedProcess([], 0, stdout="3672\n", stderr="")
        response = subprocess.CompletedProcess([], 0, stdout=b'{"jobs":[]}\n200', stderr=b"")
        payload = {"name": "smoke.gcode", "gcode": "M5\n"}
        with patch.object(lxc_install_smoke.subprocess, "run", side_effect=[pid, response]) as run:
            status, body = lxc_install_smoke.api(
                "smoke", "POST", "/api/v1/jobs", "admin-key", payload
            )

        self.assertEqual(status, 200)
        self.assertEqual(body, {"jobs": []})
        self.assertEqual(run.call_args_list[0], call(
            ["sudo", "lxc-info", "--name", "smoke", "--pid"],
            capture_output=True,
            text=True,
            timeout=30,
            check=True,
        ))
        args = run.call_args_list[1].args[0]
        self.assertEqual(args[:5], ["sudo", "nsenter", "--target", "3672", "--net"])
        self.assertIn("Authorization: Bearer admin-key", args)
        self.assertIn("Content-Type: application/json", args)
        self.assertEqual(run.call_args_list[1].kwargs["input"], b'{"name": "smoke.gcode", "gcode": "M5\\n"}')

    def test_api_preserves_unauthorized_response_for_first_start_check(self):
        pid = subprocess.CompletedProcess([], 0, stdout="3672\n", stderr="")
        response = subprocess.CompletedProcess([], 0, stdout=b'{"error":"unauthorized"}\n401', stderr=b"")
        with patch.object(lxc_install_smoke.subprocess, "run", side_effect=[pid, response]):
            status, body = lxc_install_smoke.api(
                "smoke", "GET", "/api/v1/jobs", "admin-key", authenticated=False
            )

        self.assertEqual(status, 401)
        self.assertEqual(body, {"error": "unauthorized"})

    def test_worker_registration_must_have_fresh_heartbeat_when_requested(self):
        drivers = [
            {"id": "other", "last_seen": "2026-10-29T21:00:05Z"},
            {"id": "lxc-smoke", "last_seen": "2026-10-29T21:00:04Z"},
        ]

        self.assertFalse(lxc_install_smoke.online_driver(drivers, fresh_after=1_793_307_605))
        self.assertEqual(
            lxc_install_smoke.online_driver(drivers, fresh_after=1_793_307_603),
            drivers[1],
        )

    def test_host_autostart_selection_requires_the_configured_container(self):
        listed = subprocess.CompletedProcess([], 0, stdout="other\nsmoke-guest\n", stderr="")
        with patch.object(lxc_install_smoke.subprocess, "run", return_value=listed) as run:
            lxc_install_smoke.assert_host_autostart_selected("smoke-guest")
        run.assert_called_once_with(
            ["sudo", "lxc-autostart", "--list"],
            capture_output=True,
            text=True,
            timeout=30,
            check=True,
        )

    def test_host_autostart_selection_fails_when_container_is_not_listed(self):
        listed = subprocess.CompletedProcess([], 0, stdout="other\n", stderr="")
        with (
            patch.object(lxc_install_smoke.subprocess, "run", return_value=listed),
            self.assertRaisesRegex(RuntimeError, "smoke-guest is not selected by host LXC autostart"),
        ):
            lxc_install_smoke.assert_host_autostart_selected("smoke-guest")

    def test_host_autostart_dispatch_starts_selected_guest_and_systemd(self):
        with (
            patch.object(lxc_install_smoke.subprocess, "run") as run,
            patch.object(lxc_install_smoke, "lxc_state", return_value="RUNNING"),
            patch.object(lxc_install_smoke, "guest_text", return_value="running"),
            patch.object(lxc_install_smoke, "wait_for", side_effect=lambda _, predicate, timeout: predicate()),
        ):
            lxc_install_smoke.start_host_autostart("smoke-guest")

        run.assert_called_once_with(
            ["sudo", "lxc-autostart"], capture_output=True, text=True, timeout=120, check=True
        )

    def test_proc_status_group_parser_returns_effective_supplementary_gids(self):
        status = "Name:\temberdeck\nUid:\t998\t998\t998\t998\nGroups:\t998 20 985\n"
        self.assertEqual(lxc_install_smoke.proc_group_ids(status), {"20", "998", "985"})

    def test_service_must_have_dialout_as_effective_supplementary_group(self):
        with (
            patch.object(lxc_install_smoke, "main_pid", return_value=431),
            patch.object(lxc_install_smoke, "guest_text", side_effect=["dialout:x:985:\n", "Groups:\t998 985\n"]) as guest_text,
        ):
            lxc_install_smoke.assert_service_supplementary_group("guest", "emberdeck-driver.service", "dialout")

        self.assertEqual(guest_text.call_args_list[0], call("guest", "getent", "group", "dialout"))
        self.assertEqual(guest_text.call_args_list[1], call("guest", "cat", "/proc/431/status"))

    def test_service_group_check_fails_if_dialout_is_not_effective(self):
        with (
            patch.object(lxc_install_smoke, "main_pid", return_value=431),
            patch.object(lxc_install_smoke, "guest_text", side_effect=["dialout:x:985:\n", "Groups:\t998\n"]),
            self.assertRaisesRegex(RuntimeError, "emberdeck-driver.service process is missing dialout supplementary group"),
        ):
            lxc_install_smoke.assert_service_supplementary_group("guest", "emberdeck-driver.service", "dialout")

    def test_dialout_pty_permission_probe_runs_inside_guest(self):
        result = subprocess.CompletedProcess([], 0, stdout="PASS Linux PTY DAC access\n", stderr="")
        with patch.object(lxc_install_smoke, "guest", return_value=result) as guest:
            lxc_install_smoke.verify_dialout_pty_permissions("guest")

        args = guest.call_args.args
        self.assertEqual(args[:3], ("guest", "python3", "-c"))
        self.assertIn("pty.openpty()", args[3])
        self.assertIn("systemd-run", args[3])
        self.assertIn("SupplementaryGroups=dialout", args[3])

    def test_cli_wires_published_binary_paths_checksums_and_versions(self):
        argv = [
            "--container", "guest",
            "--previous-binary", "old-bin",
            "--previous-version", "v1.2.0",
            "--previous-checksums", "old-sums",
            "--current-binary", "new-bin",
            "--current-version", "v1.3.0",
            "--current-checksums", "new-sums",
        ]
        with patch.object(lxc_install_smoke, "run_lxc_install_smoke") as run:
            result = lxc_install_smoke.main(argv)

        self.assertEqual(result, 0)
        run.assert_called_once_with(
            "guest", Path("old-bin"), "v1.2.0", Path("old-sums"),
            Path("new-bin"), "v1.3.0", Path("new-sums"),
        )


if __name__ == "__main__":
    unittest.main()
