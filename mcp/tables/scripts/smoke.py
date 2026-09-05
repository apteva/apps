#!/usr/bin/env python3
"""Exercise a compiled Tables sidecar with its SDK over real local HTTP/MCP.
Usage: python3 scripts/smoke.py /absolute/path/to/tables-binary
Uses temporary data and synthetic credentials; never calls a live gateway.
"""
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

binary = str(Path(sys.argv[1]).resolve())
with tempfile.TemporaryDirectory(prefix="tables-smoke-") as temp:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    env = dict(os.environ)
    env.update(DB_PATH=f"{temp}/tables.db", APTEVA_DATA_DIR=temp,
               APTEVA_APP_PORT=str(port), APTEVA_BIND_HOST="127.0.0.1",
               APTEVA_PROJECT_ID="smoke-project", APTEVA_APP_CONFIG="{}",
               APTEVA_APP_TOKEN=uuid.uuid4().hex, APTEVA_OUTBOUND_TOKEN="",
               APTEVA_GATEWAY_URL="")
    base = f"http://127.0.0.1:{port}"
    log = open(f"{temp}/sidecar.log", "w+")
    process = None
    def request(method, path, value=None):
        data = None if value is None else json.dumps(value).encode()
        req = urllib.request.Request(base + path, data=data, method=method,
            headers={"Authorization": "Bearer " + env["APTEVA_APP_TOKEN"], "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=5) as response:
            return json.load(response)
    def start():
        global process
        process = subprocess.Popen([binary], env=env, stdout=log, stderr=log)
        for _ in range(100):
            if process.poll() is not None:
                log.seek(0)
                raise RuntimeError(log.read())
            try:
                request("GET", "/health")
                return
            except OSError:
                time.sleep(.05)
        raise RuntimeError("Sidecar did not become healthy")
    def stop():
        if process and process.poll() is None:
            process.terminate()
            process.wait(timeout=15)
    def mcp(tool, args):
        reply = request("POST", "/mcp", {"jsonrpc":"2.0", "id":1, "method":"tools/call", "params":{"name":tool, "arguments":args}})
        assert "error" not in reply, reply
        return json.loads(reply["result"]["content"][0]["text"])
    try:
        start()
        created = mcp("tables_create", {"name":"records", "columns":[
            {"name":"revision","type":"text"}, {"name":"at","type":"datetime"},
            {"name":"payload","type":"json","default":{"id":9007199254740993}}]})
        ids = mcp("rows_insert", {"table":"records", "rows":[
            {"revision":"100%","at":"2026-01-01T01:00:00+01:00","payload":{"id":9007199254740993}},
            {"revision":"ordinary"}]})["ids"]
        row = mcp("rows_get", {"table":"records","id":str(ids[0])})["row"]
        assert row["payload"]["id"] == 9007199254740993, row
        assert mcp("rows_get", {"table":"records","id":str(ids[1])})["row"]["payload"]["id"] == 9007199254740993
        first = mcp("rows_search", {"table":"records","limit":1,"include_total":False})
        second = mcp("rows_search", {"table":"records","limit":1,"include_total":False,"cursor":first["next_cursor"]})
        assert first["rows"][0]["id"] != second["rows"][0]["id"]
        out = request("PATCH", f"/tables/records/rows/{ids[0]}?expected_revision=1&expected_table_id={created['id']}&select=id,_revision", {"revision":"patched"})
        assert out["row"] == {"id":ids[0],"_revision":2}, out
        stop()
        # Force a historical representation, then exercise the actual mount upgrader.
        with sqlite3.connect(env["DB_PATH"]) as db:
            physical = "t_" + str(created["id"])
            db.execute(f'ALTER TABLE "{physical}" DROP COLUMN _revision')
            db.execute(f'UPDATE "{physical}" SET created_at="2026-01-01 00:00:00", at="2026-01-01T01:00:00+01:00"')
            db.execute("UPDATE tables_meta SET storage_version=0,row_count=NULL")
        start()
        mcp("rows_insert", {"table":"records","rows":[{"revision":"after restart"}]})
        assert mcp("rows_count", {"table":"records"})["count"] == 3
        upgraded = mcp("rows_get", {"table":"records","id":str(ids[0])})["row"]
        assert upgraded["created_at"] == "2026-01-01T00:00:00.000000000Z", upgraded
        assert upgraded["at"] == "2026-01-01T00:00:00.000000000Z", upgraded
        assert upgraded["revision"] == "patched"
        assert upgraded["payload"]["id"] == 9007199254740993
        print("PASS: real HTTP/MCP, exact input/default numbers, cursors, projected optimistic update, restart migration and first write")
    finally:
        stop()
        log.close()
