import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
MATRIX = " ".join((ROOT / "docs" / "hardware-qualification.md").read_text(encoding="utf-8").split())


class HardwareQualificationDocumentationTests(unittest.TestCase):
    def test_matrix_does_not_claim_unverified_hardware_is_qualified(self):
        self.assertIn("Controller board and revision", MATRIX)
        self.assertIn("Firmware version/build", MATRIX)
        self.assertIn("Transport", MATRIX)
        self.assertIn("Baud rate", MATRIX)
        self.assertIn("No exact controller setup has been physically tested", MATRIX)
        self.assertIn("**Unqualified**", MATRIX)
        self.assertIn("None; no signed report exists", MATRIX)
        self.assertIn("There are currently no qualified physical controller setups", MATRIX)

    def test_procedure_covers_the_required_safety_and_failure_gates(self):
        for requirement in (
            "Keep the machine's laser physically disconnected",
            "Door, Alarm, and Hold",
            "Pause and cancel latency",
            "Controller serial unplug, worker process loss, and EmberDeck server loss",
            "Motion-only test",
            "Enclosed low-power test",
            "enclosure door interlock",
            "physical emergency stop",
            "operator's and independent reviewer's",
        ):
            with self.subTest(requirement=requirement):
                self.assertIn(requirement, MATRIX)


if __name__ == "__main__":
    unittest.main()
