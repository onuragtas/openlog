"""Instrumentation tests against real PostgreSQL, MySQL and Redis (tests/integration/docker-compose.yml)."""

import json
import signal
import subprocess
import sys
import time
from pathlib import Path

from otlp import CLIENT, CONSUMER, PRODUCER, SERVER

from apps import App, agent_env, free_port

APPS = Path(__file__).resolve().parents[1] / "apps"
BIN = Path(sys.executable).parent


def run_script(script, env, timeout=120):
    full = agent_env("", "x")
    full.update(env)
    import os

    base = {k: v for k, v in os.environ.items() if not k.startswith(("OPENLOG_", "OTEL_"))}
    base.update({"PATH": f"{BIN}:{os.environ.get('PATH', '')}", "PYTHONUNBUFFERED": "1"})
    base.update(full)
    return subprocess.run(
        [str(BIN / "openlog-instrument"), "python", script],
        cwd=str(APPS),
        env=base,
        capture_output=True,
        text=True,
        timeout=timeout,
    )


def db_spans(cap, system):
    return [
        s
        for s in cap.spans
        if s["kind"] == CLIENT and (s["attributes"].get("db.system.name") or s["attributes"].get("db.system")) == system
    ]


def texts(spans):
    return [s["attributes"].get("db.query.text") or s["attributes"].get("db.statement") for s in spans]


def test_databases_and_clients(capture, db_env):
    env = dict(db_env, OPENLOG_ENDPOINT=capture.url, OPENLOG_SERVICE_NAME="db-app")
    proc = run_script("db_app.py", env)
    assert proc.returncode == 0, proc.stdout + proc.stderr
    root = next(s for s in capture.spans if s["name"] == "db-job")
    assert all(s["trace_id"] == root["trace_id"] for s in capture.spans if s["kind"] == CLIENT)
    everything = json.dumps(capture.spans)
    # (HTTP client url.full keeps the query string, as in the OpenTelemetry HTTP semantic conventions)
    for secret in (
        "secret-pg2",
        "secret-pg3",
        "secret-asyncpg",
        "secret-sqlalchemy",
        "secret-pymysql",
        "secret-mysqlclient",
        "secret-redis",
        "secret-hash",
    ):
        assert secret not in everything, secret

    pg = texts(db_spans(capture, "postgresql"))
    assert "SELECT id, name FROM items WHERE name = ? AND id > ?" in pg  # psycopg2
    assert "SELECT * FROM items WHERE id IN (%s, %s, %s)" in pg  # placeholders kept
    assert "SELECT ? || %s, ?" in pg  # psycopg 3
    assert "SELECT $1::int + ?, ?" in pg  # asyncpg
    assert any(t == "SELECT ? WHERE ? = ? OR ? = %(v)s" for t in pg), pg  # SQLAlchemy (psycopg2 dialect)
    failed = next(
        s for s in db_spans(capture, "postgresql") if "missing_table_42" in (s["attributes"].get("db.query.text") or "")
    )
    assert failed["status"]["code"] == 2

    my = texts(db_spans(capture, "mysql"))
    assert "SELECT * FROM information_schema.tables WHERE table_name = ? AND ? IN (?)" in my, (
        my
    )  # PyMySQL, "..." is a string
    assert "SELECT ?, ?, ?" in my, my  # mysqlclient, # comment removed

    rd = texts(db_spans(capture, "redis"))
    assert "SET ? ?" in rd and "GET ?" in rd, rd
    assert any(t.startswith("HSET ?") for t in rd), rd

    http_clients = [
        s for s in capture.spans if s["kind"] == CLIENT and s["attributes"].get("http.request.method") == "GET"
    ]
    scopes = {s["scope"] for s in http_clients}
    assert any("aiohttp" in sc for sc in scopes) and any("urllib" in sc for sc in scopes), scopes
    # every client span carries the resource of the process
    assert {s["resource"]["service.name"] for s in capture.spans} == {"db-app"}


def test_grpc(capture):
    proc = run_script("grpc_app.py", {"OPENLOG_ENDPOINT": capture.url, "OPENLOG_SERVICE_NAME": "grpc-app"})
    assert proc.returncode == 0, proc.stdout + proc.stderr
    servers = [s for s in capture.spans if s["kind"] == SERVER and s["attributes"].get("rpc.system") == "grpc"]
    clients = [s for s in capture.spans if s["kind"] == CLIENT and s["attributes"].get("rpc.system") == "grpc"]
    assert len(servers) == 2 and len(clients) == 2, [(s["name"], s["kind"]) for s in capture.spans]
    ok = next(s for s in servers if s["attributes"].get("rpc.grpc.status_code") == 0)
    assert ok["attributes"]["rpc.service"] == "shop.Catalog" and ok["attributes"]["rpc.method"] == "GetItem"
    assert any(s["parent_span_id"] in {c["span_id"] for c in clients} for s in servers)
    assert any(s["attributes"].get("rpc.grpc.status_code") == 5 for s in servers)


def test_celery_prefork(capture, db_env):
    env = agent_env(capture.url, "celery-worker", REDIS_URL=db_env["REDIS_URL"])
    port = free_port()
    worker = App(
        [
            "celery",
            "-A",
            "celery_app",
            "worker",
            "--pool",
            "prefork",
            "-c",
            "2",
            "--loglevel",
            "WARNING",
            "--without-heartbeat",
            "--without-gossip",
            "--without-mingle",
        ],
        env,
        port,
    )
    try:
        deadline = time.monotonic() + 60
        while "ready" not in worker.output() and time.monotonic() < deadline:
            assert worker.proc.poll() is None, worker.output()
            time.sleep(0.2)
        proc = run_script(
            "celery_producer.py", dict(db_env, OPENLOG_ENDPOINT=capture.url, OPENLOG_SERVICE_NAME="celery-producer")
        )
        assert proc.returncode == 0, proc.stdout + proc.stderr + worker.output()
        result = json.loads(next(line for line in proc.stdout.splitlines() if line.startswith("RESULT "))[7:])
        assert [r["sum"] for r in result["results"]] == [0, 2, 4, 6, 8, 10]
        consumers = capture.wait_for(
            "6 consumer spans",
            lambda: (
                [s for s in capture.spans if s["kind"] == CONSUMER and s["resource"]["service.name"] == "celery-worker"]
                if len([s for s in capture.spans if s["kind"] == CONSUMER]) >= 6
                else None
            ),
            timeout=30,
        )
        producers = [s for s in capture.spans if s["kind"] == PRODUCER]
        assert len(producers) == 6
        assert {s["trace_id"] for s in consumers} == {result["trace_id"]}
        assert all(s["name"].startswith("run/") for s in consumers)
        worker_pids = {r["pid"] for r in result["results"]}
        span_pids = {s["resource"]["process.pid"] for s in consumers}
        assert span_pids == worker_pids and worker.proc.pid not in span_pids, (span_pids, worker_pids, worker.proc.pid)
        logs = capture.wait_for(
            "task logs",
            lambda: (
                [l for l in capture.logs if "adding" in str(l["body"])]
                if len([l for l in capture.logs if "adding" in str(l["body"])]) >= 6
                else None
            ),
            timeout=30,
        )
        assert {l["trace_id"] for l in logs} == {result["trace_id"]}
    finally:
        worker.stop(signal.SIGTERM, 30)
        worker.close()
