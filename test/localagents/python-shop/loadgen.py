"""Traffic generator for python-shop (LOADGEN_TARGET, LOADGEN_RPS). Not instrumented: plain httpx in a fresh process."""

import asyncio
import os
import random

import httpx

TARGET = os.environ.get("LOADGEN_TARGET", "http://python-shop:8000")
RPS = float(os.environ.get("LOADGEN_RPS", "4"))
ROUTES = [
    (5, "GET", "/api/recommendations"),
    (3, "POST", "/api/checkout"),
    (2, "GET", "/api/orders/{id}"),
    (1, "GET", "/api/flaky"),
]


async def one(client):
    _, method, path = random.choices(ROUTES, weights=[w for w, _, _ in ROUTES])[0]
    try:
        await client.request(method, TARGET + path.format(id=random.randint(1, 200)))
    except httpx.HTTPError as exc:
        print(f"loadgen: {method} {path}: {exc!r}", flush=True)


async def main():
    async with httpx.AsyncClient(timeout=15.0) as client:
        while True:
            try:
                (await client.get(TARGET + "/healthz")).raise_for_status()
                break
            except httpx.HTTPError:
                await asyncio.sleep(2)
        print(f"loadgen: {RPS} req/s against {TARGET}", flush=True)
        pending = set()
        while True:
            pending.add(asyncio.ensure_future(one(client)))
            pending = {t for t in pending if not t.done()}
            await asyncio.sleep(random.expovariate(RPS) if RPS > 0 else 1.0)


if __name__ == "__main__":
    asyncio.run(main())
