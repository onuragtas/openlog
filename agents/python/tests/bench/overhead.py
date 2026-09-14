"""Overhead micro-benchmark.

Requests per second and latency of a single-process Flask (gunicorn, 1 sync worker) and FastAPI (uvicorn, 1 process)
app without the agent, with the agent (sampled, ratio 1) and with the agent at ratio 0 (spans not sampled), exporting
to a local OTLP capture server. The server is saturated by load generator processes (persistent connections), so its
service time per request is 1/rps and the added time per request is 1/rps − 1/rps(no agent).

    python tests/bench/overhead.py        (BENCH_SECONDS=8 BENCH_CONCURRENCY=8 BENCH_ROUNDS=3)
"""

from __future__ import annotations

import http.client
import multiprocessing as mp
import os
import platform
import statistics
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "helpers"))

from otlp import Capture  # noqa: E402

from apps import BIN, App, agent_env, free_port  # noqa: E402

SECONDS = float(os.environ.get("BENCH_SECONDS", "8"))
CONCURRENCY = int(os.environ.get("BENCH_CONCURRENCY", "8"))
ROUNDS = int(os.environ.get("BENCH_ROUNDS", "3"))


def _loader(port: int, path: str, seconds: float, keepalive: bool, out: mp.Queue) -> None:
    lat = []
    end = time.monotonic() + seconds
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    headers = {} if keepalive else {"Connection": "close"}
    while time.monotonic() < end:
        t0 = time.perf_counter()
        try:
            conn.request("GET", path, headers=headers)
            r = conn.getresponse()
            r.read()
            if not keepalive or r.getheader("connection", "").lower() == "close":
                conn.close()
                conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        except (OSError, http.client.HTTPException):
            conn.close()
            conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
            continue
        lat.append(time.perf_counter() - t0)
    conn.close()
    out.put(lat)


def load(port: int, path: str, seconds: float, keepalive: bool) -> dict:
    q: mp.Queue = mp.Queue()
    procs = [mp.Process(target=_loader, args=(port, path, seconds, keepalive, q)) for _ in range(CONCURRENCY)]
    for p in procs:
        p.start()
    lat = []
    for _ in procs:
        lat.extend(q.get())
    for p in procs:
        p.join()
    lat.sort()
    return {"rps": len(lat) / seconds, "p50": lat[len(lat) // 2] * 1000, "p99": lat[int(len(lat) * 0.99)] * 1000}


def rss_mb(pid: int) -> float:
    try:
        with open(f"/proc/{pid}/status") as f:
            for line in f:
                if line.startswith("VmRSS:"):
                    return int(line.split()[1]) / 1024
    except OSError:
        pass
    return 0.0


def worker_pid(app: App, path: str) -> int:
    _, body = app.get(path)
    return int(body.split('"pid":')[1].split("}")[0].split(",")[0].strip())


def measure(name: str, argv, env, instrument: bool, path: str, keepalive: bool) -> dict:
    results, rss = [], 0.0
    for _ in range(ROUNDS):
        port = free_port()
        full_argv = [a.replace("{port}", str(port)) for a in argv]
        app = App(full_argv, env, port, instrument=instrument).wait_ready()
        try:
            load(port, path, 2, keepalive)  # warm-up
            results.append(load(port, path, SECONDS, keepalive))
            rss = max(rss, rss_mb(worker_pid(app, path)))
        finally:
            app.stop()
            app.close()
    med = {k: statistics.median(r[k] for r in results) for k in ("rps", "p50", "p99")}
    med.update(name=name, rss=rss)
    return med


def main() -> None:
    cap = Capture()
    scenarios = [
        (
            "Flask / gunicorn (1 gthread worker, 1 thread, keep-alive)",
            [
                "gunicorn",
                "-w",
                "1",
                "-k",
                "gthread",
                "--threads",
                "1",
                "--keep-alive",
                "30",
                "-b",
                "127.0.0.1:{port}",
                "--log-level",
                "warning",
                "flask_app:app",
            ],
            "/bench/42",
            True,
        ),
        (
            "FastAPI / uvicorn (1 process)",
            [
                "uvicorn",
                "fastapi_app:app",
                "--host",
                "127.0.0.1",
                "--port",
                "{port}",
                "--log-level",
                "warning",
                "--no-access-log",
            ],
            "/bench/42",
            True,
        ),
    ]
    base = agent_env(cap.url, "bench", OPENLOG_METRIC_EXPORT_INTERVAL="10s", OPENLOG_LOG_LEVEL="warn")
    print(
        f"\nPython {platform.python_version()}, {os.cpu_count()} CPUs, concurrency {CONCURRENCY}, {SECONDS:g}s x {ROUNDS} rounds (median)"
    )
    for title, argv, path, keepalive in scenarios:
        plain_argv = [str(BIN / argv[0])] + argv[1:]
        rows = [
            measure("no agent", plain_argv, {"OPENLOG_ENABLED": "false"}, False, path, keepalive),
            measure("agent, sampled (ratio 1)", argv, base, True, path, keepalive),
            measure(
                "agent, not sampled (ratio 0)", argv, dict(base, OPENLOG_SAMPLING_RATIO="0"), True, path, keepalive
            ),
        ]
        b = rows[0]["rps"]
        print(f"\n{title}\n")
        print("| Scenario | req/s | vs. no agent | p50 ms | p99 ms | RSS MB | added per request |")
        print("|---|---|---|---|---|---|---|")
        for r in rows:
            delta = "—" if r is rows[0] else f"{(r['rps'] / b - 1) * 100:.1f} %"
            added = "—" if r is rows[0] else f"{(1 / r['rps'] - 1 / b) * 1000:.3f} ms"
            print(
                f"| {r['name']} | {r['rps']:.0f} | {delta} | {r['p50']:.2f} | {r['p99']:.2f} | {r['rss']:.0f} | {added} |"
            )
    print(f"\ncaptured: {len(cap.spans)} spans, {len(cap.metrics)} metric streams, {len(cap.logs)} logs")
    cap.close()


if __name__ == "__main__":
    main()
