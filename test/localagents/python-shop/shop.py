"""python-shop: FastAPI demo service instrumented with the openlog Python agent (openlog-instrument, no code changes).

Calls the demo catalog (PHP) and orders (Go) services with httpx, so its spans join their traces:

  GET  /api/recommendations   catalog /products + orders /orders/recent
  POST /api/checkout          catalog /products/{id} -> orders POST /orders
  GET  /api/orders/{order_id} orders /orders/{id}
  GET  /api/flaky             ~20% ValueError from application code
"""

import asyncio
import logging
import os
import random

import httpx
from fastapi import FastAPI, HTTPException

CATALOG_URL = os.environ.get("CATALOG_URL", "http://catalog:8080")
ORDERS_URL = os.environ.get("ORDERS_URL", "http://orders:8080")

log = logging.getLogger("python-shop")
app = FastAPI(title="python-shop")
client = httpx.AsyncClient(timeout=10.0)


async def upstream(method: str, url: str, **kwargs):
    r = await client.request(method, url, **kwargs)
    if r.status_code >= 500:
        raise HTTPException(status_code=502, detail=f"{url} answered {r.status_code}")
    return r


@app.get("/healthz")
async def healthz():
    return {"status": "ok"}


@app.get("/api/recommendations")
async def recommendations():
    products, recent = await asyncio.gather(
        upstream("GET", f"{CATALOG_URL}/products"), upstream("GET", f"{ORDERS_URL}/orders/recent")
    )
    items = products.json() if products.status_code == 200 else []
    picks = random.sample(items, k=min(3, len(items))) if isinstance(items, list) else []
    log.info("recommendations computed", extra={"picks": len(picks)})
    return {"picks": picks, "recent_orders": recent.json() if recent.status_code == 200 else []}


@app.post("/api/checkout")
async def checkout():
    product_id = random.randint(1, 20)
    product = await upstream("GET", f"{CATALOG_URL}/products/{product_id}")
    if product.status_code != 200:
        raise HTTPException(status_code=product.status_code, detail="product not found")
    order = await upstream(
        "POST",
        f"{ORDERS_URL}/orders",
        json={"customer_id": random.randint(1, 50), "product_id": product_id, "quantity": random.randint(1, 3)},
    )
    log.warning("checkout finished with status %s", order.status_code)
    return {"product": product.json(), "order": order.json()}


@app.get("/api/orders/{order_id}")
async def get_order(order_id: int):
    r = await upstream("GET", f"{ORDERS_URL}/orders/{order_id}")
    if r.status_code != 200:
        raise HTTPException(status_code=r.status_code, detail=r.text)
    return r.json()


@app.get("/api/flaky")
async def flaky():
    if random.random() < 0.2:
        raise ValueError("flaky python-shop dependency")
    return {"ok": True}
