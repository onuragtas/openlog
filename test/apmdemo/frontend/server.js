'use strict';

const express = require('express');
const { context, trace, SpanStatusCode } = require('@opentelemetry/api');
const { logs, SeverityNumber } = require('@opentelemetry/api-logs');
const { getRPCMetadata } = require('@opentelemetry/core');

const PORT = Number(process.env.PORT || 3000);
const CATALOG_URL = process.env.CATALOG_URL || 'http://catalog:8080';
const ORDERS_URL = process.env.ORDERS_URL || 'http://orders:8080';

const logger = logs.getLogger('frontend', '1.0.0');

class UpstreamError extends Error {
  constructor(service, status) {
    super(`${service} responded with HTTP ${status}`);
    this.name = 'UpstreamError';
    this.status = status;
  }
}

// The HTTP SERVER span of the current request (express child spans may be active instead).
function serverSpan() {
  return getRPCMetadata(context.active())?.span ?? trace.getActiveSpan();
}

// fetch() is instrumented by @opentelemetry/instrumentation-undici: CLIENT span with
// server.address/server.port and W3C traceparent injection.
async function callJSON(service, url, init = {}) {
  const res = await fetch(url, {
    ...init,
    headers: { accept: 'application/json', ...(init.body ? { 'content-type': 'application/json' } : {}) },
    signal: AbortSignal.timeout(10_000),
  });
  const text = await res.text();
  let body;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = { raw: text };
  }
  return { status: res.status, body, service };
}

// Pass 2xx/4xx through; an upstream 5xx becomes 502 and marks the server span as an error.
function relay(res, upstream) {
  if (upstream.status >= 500) {
    throw new UpstreamError(upstream.service, upstream.status);
  }
  res.status(upstream.status).json(upstream.body);
}

const app = express();
app.use(express.json());

// One OTLP log record per request, correlated with the server span.
app.use((req, res, next) => {
  const ctx = context.active();
  const started = process.hrtime.bigint();
  res.on('finish', () => {
    const ms = Number(process.hrtime.bigint() - started) / 1e6;
    const isError = res.statusCode >= 500;
    logger.emit({
      context: ctx,
      severityNumber: isError ? SeverityNumber.ERROR : SeverityNumber.INFO,
      severityText: isError ? 'ERROR' : 'INFO',
      body: `${req.method} ${req.originalUrl} -> ${res.statusCode} (${ms.toFixed(1)} ms)`,
      attributes: {
        'http.request.method': req.method,
        'url.path': req.path,
        'http.response.status_code': res.statusCode,
        ...(req.route ? { 'http.route': req.baseUrl + req.route.path } : {}),
      },
    });
  });
  next();
});

app.get('/healthz', (req, res) => res.json({ status: 'ok' }));

app.get('/api/products', async (req, res) => {
  relay(res, await callJSON('catalog', `${CATALOG_URL}/products`));
});

app.get('/api/products/:id', async (req, res) => {
  relay(res, await callJSON('catalog', `${CATALOG_URL}/products/${encodeURIComponent(req.params.id)}`));
});

app.post('/api/orders', async (req, res) => {
  const { customer_id, product_id, quantity } = req.body ?? {};
  relay(
    res,
    await callJSON('orders', `${ORDERS_URL}/orders`, {
      method: 'POST',
      body: JSON.stringify({ customer_id, product_id, quantity }),
    }),
  );
});

// Static paths before /:id.
app.get('/api/orders/report', async (req, res) => {
  relay(res, await callJSON('orders', `${ORDERS_URL}/orders/report`));
});

app.get('/api/orders/recent', async (req, res) => {
  relay(res, await callJSON('orders', `${ORDERS_URL}/orders/recent`));
});

app.get('/api/orders/:id', async (req, res) => {
  relay(res, await callJSON('orders', `${ORDERS_URL}/orders/${encodeURIComponent(req.params.id)}`));
});

app.get('/api/checkout', async (req, res) => {
  const productId = 1 + Math.floor(Math.random() * 20);
  const product = await callJSON('catalog', `${CATALOG_URL}/products/${productId}`);
  if (product.status >= 500) throw new UpstreamError('catalog', product.status);
  if (product.status !== 200) return res.status(product.status).json(product.body);
  const order = await callJSON('orders', `${ORDERS_URL}/orders`, {
    method: 'POST',
    body: JSON.stringify({
      customer_id: 1 + Math.floor(Math.random() * 50),
      product_id: productId,
      quantity: 1 + Math.floor(Math.random() * 3),
    }),
  });
  if (order.status >= 500) throw new UpstreamError('orders', order.status);
  res.status(order.status).json({ product: product.body, order: order.body });
});

function priceOf(cart) {
  return cart.item.price;
}

// ~20% of requests fail with a genuine TypeError from application code.
app.get('/api/flaky', (req, res) => {
  const cart = Math.random() < 0.2 ? {} : { item: { price: 42.5 } };
  res.json({ total: priceOf(cart) });
});

// eslint-disable-next-line no-unused-vars
app.use((err, req, res, next) => {
  const status = err instanceof UpstreamError ? 502 : err.status && err.status < 500 ? err.status : 500;
  if (status >= 500) {
    const span = serverSpan();
    if (span) {
      span.recordException(err);
      span.setStatus({ code: SpanStatusCode.ERROR, message: err.message });
    }
    console.error(`${req.method} ${req.originalUrl} failed:`, err.stack || err);
  }
  res.status(status).json({ error: err.message });
});

app.listen(PORT, () => {
  console.log(`frontend listening on :${PORT} (catalog=${CATALOG_URL}, orders=${ORDERS_URL})`);
});
