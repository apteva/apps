"""Durable command deduplication and application-acknowledged data delivery.

A claim is committed BEFORE calling hardware. An interrupted claim is reported
as unconfirmed after restart and is never automatically executed again.
"""
import hashlib
import json
import re
import sqlite3
import threading
import time
import uuid
from datetime import datetime, timezone
from typing import Any, Callable


class DeviceRuntime:
    def __init__(self, path: str, max_pending: int = 10000):
        self.db = sqlite3.connect(path, check_same_thread=False)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.execute("PRAGMA synchronous=FULL")
        self.db.executescript("""
          CREATE TABLE IF NOT EXISTS commands (
            id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, state TEXT NOT NULL,
            response TEXT, expires_ms INTEGER NOT NULL);
          CREATE TABLE IF NOT EXISTS outgoing (
            id TEXT PRIMARY KEY, kind TEXT NOT NULL, payload TEXT NOT NULL);
        """)
        self.db.commit()
        self.lock = threading.Lock()
        self.max_pending = max_pending
        self.session_id = uuid.uuid4().hex
        self.sequence = 0

    def command(self, request: dict[str, Any], execute: Callable[[dict[str, Any]], Any]) -> dict[str, Any]:
        command_id = request.get("command_id") or request.get("id")
        if not isinstance(command_id, str) or not re.fullmatch(r"cmd_[a-zA-Z0-9_-]{1,120}", command_id):
            raise ValueError("invalid command_id")
        canonical = json.dumps(request, sort_keys=True, separators=(",", ":"))
        fingerprint = hashlib.sha256(canonical.encode()).hexdigest()
        expires = request.get("expires_unix_ms")
        if not isinstance(expires, int):
            raise ValueError("absolute expires_unix_ms is required")
        response: dict[str, Any] = {"command_id": command_id, "success": False}
        with self.lock:
            row = self.db.execute("SELECT fingerprint,state,response FROM commands WHERE id=?", (command_id,)).fetchone()
            if row:
                if row[0] != fingerprint:
                    return {**response, "error": "command_id reused with a different request"}
                if row[1] == "completed":
                    return json.loads(row[2])
                return {**response, "error": "execution unconfirmed after interruption; inspect hardware before a new command"}
            now_ms = int(time.time() * 1000)
            if expires <= now_ms:
                return {**response, "error": "command expired; not executed"}
            # Expired requests cannot execute even after their cache is removed.
            self.db.execute("DELETE FROM commands WHERE state='completed' AND expires_ms<?", (now_ms - 86400000,))
            if self.db.execute("SELECT COUNT(*) FROM commands").fetchone()[0] >= self.max_pending:
                self.db.commit()
                return {**response, "error": "command journal full; not executed"}
            self.db.execute("INSERT INTO commands VALUES(?,?,'executing',NULL,?)", (command_id, fingerprint, expires))
            self.db.commit()
        try:
            result = execute(request)
            response.update(success=True, result=result)
        except Exception as exc:
            response["error"] = str(exc)
        with self.lock:
            self.db.execute("UPDATE commands SET state='completed',response=? WHERE id=?", (json.dumps(response), command_id))
            self.db.commit()
        return response

    def sample(self, kind: str, data: Any, metadata: dict[str, Any] | None = None, mode: str = "patch") -> str:
        if kind not in ("state", "telemetry"):
            raise ValueError("kind must be state or telemetry")
        with self.lock:
            if self.db.execute("SELECT COUNT(*) FROM outgoing").fetchone()[0] >= self.max_pending:
                raise RuntimeError("outgoing journal full; application must apply backpressure")
            self.sequence += 1
            mid = uuid.uuid4().hex
            payload = json.dumps({"message_id": mid, "observed_at": datetime.now(timezone.utc).isoformat(),
                                  "session_id": self.session_id, "sequence": self.sequence, "mode": mode,
                                  "data": data, "metadata": metadata or {}}, separators=(",", ":"))
            if len(payload.encode()) > 131072:
                raise ValueError("payload exceeds default Devices limit")
            self.db.execute("INSERT INTO outgoing VALUES(?,?,?)", (mid, kind, payload))
            self.db.commit()
            return mid

    def receipt(self, payload: str) -> None:
        value = json.loads(payload)
        if value.get("status") == "accepted":
            with self.lock:
                self.db.execute("DELETE FROM outgoing WHERE id=?", (value.get("message_id"),))
                self.db.commit()

    def resend(self, publish: Callable[[str, str], None]) -> None:
        with self.lock:
            rows = self.db.execute("SELECT kind,payload FROM outgoing ORDER BY rowid LIMIT 100").fetchall()
        for kind, payload in rows:
            publish(kind, payload)

    def close(self) -> None:
        self.db.close()
