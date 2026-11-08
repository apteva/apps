import json
import tempfile
import time
import unittest
from device_runtime import DeviceRuntime


class RuntimeTests(unittest.TestCase):
    def test_duplicate_expiry_and_interrupted_claim(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = DeviceRuntime(directory + "/journal.sqlite")
            command = {"command_id": "cmd_one", "expires_unix_ms": int(time.time() * 1000) + 60000,
                       "operation": "echo", "arguments": {"on": True}}
            calls = []
            first = runtime.command(command, lambda c: calls.append(c) or {"ok": True})
            runtime.close()
            runtime = DeviceRuntime(directory + "/journal.sqlite")
            self.assertEqual(runtime.command(command, lambda c: calls.append(c)), first)
            self.assertEqual(len(calls), 1)
            changed = {**command, "arguments": {"on": False}}
            self.assertFalse(runtime.command(changed, lambda _: None)["success"])
            expired = {**command, "command_id": "cmd_expired", "expires_unix_ms": 1}
            self.assertIn("expired", runtime.command(expired, lambda _: self.fail("executed expired"))["error"])
            # Simulate process death after the durable claim and before hardware result.
            try:
                runtime.command({**command, "command_id": "cmd_interrupted"}, lambda _: (_ for _ in ()).throw(KeyboardInterrupt()))
            except KeyboardInterrupt:
                pass
            runtime.close()
            runtime = DeviceRuntime(directory + "/journal.sqlite")
            result = runtime.command({**command, "command_id": "cmd_interrupted"}, lambda _: self.fail("reexecuted interrupted"))
            self.assertIn("unconfirmed", result["error"])
            runtime.close()

    def test_samples_survive_until_application_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = DeviceRuntime(directory + "/journal.sqlite", max_pending=1)
            mid = runtime.sample("state", {"id": "opaque", "array": [True, None]})
            with self.assertRaises(RuntimeError):
                runtime.sample("state", {})
            runtime.close()
            runtime = DeviceRuntime(directory + "/journal.sqlite")
            sends = []
            runtime.resend(lambda kind, payload: sends.append((kind, payload)))
            self.assertEqual(json.loads(sends[0][1])["message_id"], mid)
            runtime.receipt(json.dumps({"message_id": mid, "status": "accepted"}))
            runtime.resend(lambda *_: self.fail("resent acknowledged sample"))
            runtime.close()


if __name__ == "__main__":
    unittest.main()
