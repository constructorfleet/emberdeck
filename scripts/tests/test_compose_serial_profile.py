import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[2]


class ComposeSerialProfileTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with (ROOT / "compose.yaml").open(encoding="utf-8") as compose_file:
            cls.services = yaml.safe_load(compose_file)["services"]

    def test_serial_worker_is_opt_in_and_maps_device_and_numeric_group(self):
        service = self.services["serial-driver"]
        self.assertEqual(service["profiles"], ["serial"])
        self.assertEqual(
            service["devices"],
            [
                "${EMBERDECK_SERIAL_DEVICE:-/dev/emberdeck-serial-device}"
                ":${EMBERDECK_SERIAL_DEVICE:-/dev/emberdeck-serial-device}:rw"
            ],
        )
        self.assertEqual(service["group_add"], ["${EMBERDECK_SERIAL_GID:-65534}"])

    def test_serial_worker_uses_private_key_and_cli_serial_transport_options(self):
        service = self.services["serial-driver"]
        self.assertEqual(service["image"], "${EMBERDECK_SERIAL_IMAGE:-emberdeck:local}")
        self.assertEqual(
            service["environment"]["EMBERDECK_API_KEY"],
            "${EMBERDECK_DRIVER_API_KEY:-not-configured}",
        )
        self.assertEqual(
            service["command"],
            [
                "driver", "-server", "${EMBERDECK_SERVER_URL:-http://emberdeck:8080}",
                "-id", "${EMBERDECK_DRIVER_ID:-bench}", "-serial",
                "${EMBERDECK_SERIAL_DEVICE:-/dev/emberdeck-serial-device}", "-baud",
                "${EMBERDECK_SERIAL_BAUD:-115200}", "-serial-dtr",
                "${EMBERDECK_SERIAL_DTR:-off}", "-serial-rts",
                "${EMBERDECK_SERIAL_RTS:-off}",
            ],
        )
        self.assertEqual(service["restart"], "always")


if __name__ == "__main__":
    unittest.main()
