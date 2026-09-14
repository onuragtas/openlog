"""FastAPI sample app: openlog-instrument uvicorn fastapi_app:app --port $PORT"""

import logging
import os

import httpx
from fastapi import FastAPI

PORT = int(os.environ.get("PORT", "8000"))
log = logging.getLogger("shop")
log.setLevel(logging.INFO)

app = FastAPI()


@app.get("/ready")
async def ready():
    return "ok"


@app.get("/users/{user_id}")
async def user(user_id: int):
    log.warning("fastapi user %s", user_id)
    return {"id": user_id, "pid": os.getpid()}


@app.get("/bench/{n}")
async def bench(n: int):
    # overhead benchmark route: no logging, so only request instrumentation is measured
    return {"n": n, "pid": os.getpid()}


@app.get("/boom")
async def boom():
    raise ValueError("fastapi boom")


@app.get("/chain")
async def chain():
    async with httpx.AsyncClient() as client:
        r = await client.get(f"http://127.0.0.1:{PORT}/users/7")
    return {"upstream": r.json()}
