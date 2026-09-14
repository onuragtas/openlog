"""Runs statements with literal values through every bundled database/client instrumentation inside one trace."""

import asyncio
import os
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlsplit

from opentelemetry import trace

PG_URL = os.environ["PG_URL"]
MYSQL = dict(
    host=os.environ["MYSQL_HOST"],
    port=int(os.environ["MYSQL_PORT"]),
    user="openlog",
    password="openlog",
    database="openlog",
)
REDIS_URL = os.environ["REDIS_URL"]

tracer = trace.get_tracer("db_app")


def postgres():
    import psycopg
    import psycopg2

    conn = psycopg2.connect(PG_URL)
    conn.autocommit = True
    cur = conn.cursor()
    cur.execute("CREATE TABLE IF NOT EXISTS items (id int, name text, note text)")
    cur.execute("SELECT id, name FROM items WHERE name = 'secret-pg2' AND id > 10")
    cur.execute("SELECT * FROM items WHERE id IN (%s, %s, %s)", (1, 2, 3))
    try:
        cur.execute("SELECT * FROM missing_table_42")
    except psycopg2.Error:
        pass
    conn.close()

    with psycopg.connect(PG_URL, autocommit=True) as c3:
        c3.execute("SELECT 'secret-pg3' || %s, 42", ("x",)).fetchall()

    async def apg():
        import asyncpg

        c = await asyncpg.connect(PG_URL)
        await c.fetch("SELECT $1::int + 42, 'secret-asyncpg'", 1)
        await c.close()

    asyncio.run(apg())

    from sqlalchemy import create_engine, text

    engine = create_engine(PG_URL.replace("postgresql://", "postgresql+psycopg2://"))
    with engine.connect() as c:
        c.execute(text("SELECT 1 WHERE 'secret-sqlalchemy' = 'x' OR 7 = :v"), {"v": 7}).fetchall()
    engine.dispose()


def mysql():
    import MySQLdb
    import pymysql

    conn = pymysql.connect(**MYSQL)
    cur = conn.cursor()
    cur.execute('SELECT * FROM information_schema.tables WHERE table_name = "secret-pymysql" AND 1 IN (1, 2, 3)')
    conn.close()

    conn = MySQLdb.connect(
        host=MYSQL["host"], port=MYSQL["port"], user="openlog", password="openlog", database="openlog"
    )
    cur = conn.cursor()
    cur.execute("SELECT 'secret-mysqlclient', 0xFF, 3.14 # comment")
    conn.close()


def redis_calls():
    import redis

    r = redis.Redis.from_url(REDIS_URL)
    r.set("user:42", "secret-redis")
    r.get("user:42")
    r.hset("h:1", mapping={"f": "secret-hash"})


def http_clients():
    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("content-length", "2")
            self.end_headers()
            self.wfile.write(b"ok")

        def log_message(self, *a):
            pass

    srv = HTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{srv.server_address[1]}/ping?token=abc"

    async def aio():
        import aiohttp

        async with aiohttp.ClientSession() as s:
            async with s.get(url) as resp:
                await resp.read()

    asyncio.run(aio())
    import urllib.request

    urllib.request.urlopen(url).read()
    srv.shutdown()


if __name__ == "__main__":
    assert urlsplit(PG_URL).scheme.startswith("postgres")
    with tracer.start_as_current_span("db-job", kind=trace.SpanKind.SERVER):
        postgres()
        mysql()
        redis_calls()
        http_clients()
    print("done", flush=True)
