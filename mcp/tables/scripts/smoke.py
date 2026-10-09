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
import urllib.error
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
        for path in ("/tables", "/tables?sig=untrusted", "/diagnostics", "/diagnostics/1"):
            try:
                with urllib.request.urlopen(base + path, timeout=5) as response:
                    raise AssertionError(f"Unauthenticated request accepted: {path}: {response.status}")
            except urllib.error.HTTPError as error:
                assert error.code == 401, (path, error.code)
        assert "tables" in request("GET", "/tables?sig=untrusted")
        assert request("GET", "/projections/worker-status")["cleanup_interval_seconds"] == 45
        assert mcp("projections_worker_status", {})["refresh_fallback_interval_seconds"] == 1
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
        # Exercise watched dependencies through the public MCP API and actual
        # background worker, then restart the sidecar with captured dirty work.
        mcp("tables_create", {"name":"measurements", "columns":[
            {"name":"centre_id","type":"text"}, {"name":"value","type":"number"},
            {"name":"metadata","type":"text"}]})
        measurement_id=mcp("rows_insert", {"table":"measurements","rows":[{"centre_id":"a","value":2}]})["ids"][0]
        mcp("projections_create", {"name":"measurement_totals","version":1,
            "sql":"SELECT centre_id,SUM(value) AS total FROM {measurements} GROUP BY centre_id",
            "source_tables":["measurements"],"scope_columns":["centre_id"],
            "result_columns":[{"name":"centre_id","type":"text"},{"name":"total","type":"number"}],
            "source_dependencies":[{"table":"measurements","watched_columns":["centre_id","value"]}]})
        def await_projection(total):
            for _ in range(200):
                status=mcp("projections_status", {"name":"measurement_totals"})
                if status["ready"]:
                    rows=mcp("tables_query", {"sql":"SELECT total FROM {measurement_totals}"})["rows"]
                    if len(rows)==1 and rows[0]["total"]==total:
                        return status
                time.sleep(.05)
            raise AssertionError(f"Projection did not publish {total}: {status}")
        initial=await_projection(2)
        metrics=mcp("projections_worker_status", {})["metrics"]
        assert metrics["refresh_jobs"] > 0 and metrics["sql_execution_ns"] > 0, metrics
        mcp("indexes_create", {"table":"measurement_totals","name":"by_centre","columns":["centre_id"]})
        index=mcp("indexes_create", {"table":"measurement_totals","name":"by_centre","columns":["centre_id"],"layout":"filter_first","replace":True})["index"]
        assert index["layout"]=="filter_first"
        assert [c["col"] for c in index["physical_columns"]]==["centre_id","_projection_generation"]

        mcp("rows_update", {"table":"measurements","id":measurement_id,"fields":{"metadata":"synced"}})
        clean=mcp("projections_status", {"name":"measurement_totals"})
        assert clean["ready"] and clean["latest_relevant_change"]==initial["latest_relevant_change"], clean
        mcp("projections_pause", {"name":"measurement_totals","paused":True})
        mcp("rows_update", {"table":"measurements","id":measurement_id,"fields":{"value":7}})
        stop()
        start()
        paused=mcp("projections_status", {"name":"measurement_totals"})
        assert paused["status"]=="paused" and not paused["ready"], paused
        mcp("projections_pause", {"name":"measurement_totals","paused":False})
        await_projection(7)
        index=mcp("indexes_list", {"table":"measurement_totals"})["indexes"][0]
        assert index["layout"]=="filter_first" and [c["col"] for c in index["physical_columns"]]==["centre_id","_projection_generation"]

        definition=mcp("projections_describe", {"name":"measurement_totals"})
        mcp("projections_create", {"name":"measurement_totals","version":2,"activate":False,"inherit_indexes":True,
            "sql":definition["sql"],"source_tables":definition["source_tables"],"result_columns":definition["result_columns"],
            "scope_columns":definition["scope_columns"],"source_dependencies":definition["source_dependencies"]})
        assert mcp("indexes_list", {"table":"measurement_totals","version":2})["indexes"][0]["layout"]=="filter_first"
        for _ in range(200):
            if mcp("projections_status", {"name":"measurement_totals","version":2})["ready"]:
                break
            time.sleep(.05)
        else:
            raise AssertionError("Inherited replacement never became ready")
        mcp("projections_activate", {"name":"measurement_totals","version":2})
        await_projection(7)
        mcp("indexes_drop", {"table":"measurement_totals","name":"by_centre","confirm":True})
        assert mcp("indexes_list", {"table":"measurement_totals"})["indexes"]==[]

        assert mcp("projections_describe", {"name":"measurement_totals"})["source_dependencies"][0]["watched_columns"]==["centre_id","value"]
        # Rehearse the additive 0.2.14 -> 0.2.15 diagnostics index migration
        # against the actual SDK ledger, preserving pre-existing observations.
        stop()
        with sqlite3.connect(env["DB_PATH"]) as db:
            for key in ("outcome", "request", "query", "operation", "call"):
                db.execute(f"DROP INDEX read_diagnostics_project_{key}_time")
            db.execute("DELETE FROM _migrations WHERE filename='019_diagnostics_history_indexes.sql'")
            db.executemany("INSERT INTO read_diagnostics(project_id,recorded_at_ms,operation,call_id,request_id,query_id,outcome,total_ms) VALUES('smoke-project',1000,'tables_query',?,'history-smoke','fingerprint','error',300)", [(f"history-{i}",) for i in range(3)])
        start()
        tools=request("POST", "/mcp", {"jsonrpc":"2.0", "id":1, "method":"tools/list", "params":{}})["result"]["tools"]
        assert {"diagnostics_list", "diagnostics_get"} <= {tool["name"] for tool in tools}
        history=mcp("diagnostics_list", {"limit":2,"request_id":"history-smoke"})
        assert len(history["diagnostics"])==2 and history["has_more"] and "total" not in history, history
        assert history["page_summary"]["failures"]==2
        second=mcp("diagnostics_list", {"limit":2,"request_id":"history-smoke","cursor":history["next_cursor"]})
        assert len(second["diagnostics"])==1 and not second["has_more"], second
        summary=request("GET", "/diagnostics?request_id=history-smoke&include_summary=true")
        assert summary["total"]==3 and summary["error_count"]==3, summary
        item=history["diagnostics"][0]
        assert request("GET", f"/diagnostics/{item['id']}")["diagnostic"]==item
        assert mcp("diagnostics_get", {"id":item["id"]})["diagnostic"]==item
        with sqlite3.connect(env["DB_PATH"]) as db:
            assert db.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='read_diagnostics_project_request_time'").fetchone()[0]==1
        print("PASS: real HTTP/MCP authentication, exact numbers, cursors, optimistic updates, automatic restart migration and watched projection recovery, filter-leading indexes, worker metrics, diagnostics cursor/detail/summary APIs and additive history migration")
    finally:
        stop()
        log.close()
