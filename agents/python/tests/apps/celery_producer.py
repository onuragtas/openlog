"""Publishes Celery tasks inside one trace and waits for their results."""

import json

from celery_app import add
from opentelemetry import trace

tracer = trace.get_tracer("producer")

if __name__ == "__main__":
    results = []
    with tracer.start_as_current_span("enqueue-job", kind=trace.SpanKind.SERVER) as span:
        pending = [add.delay(i, i) for i in range(6)]
        results = [p.get(timeout=60) for p in pending]
    print(
        "RESULT " + json.dumps({"trace_id": format(span.get_span_context().trace_id, "032x"), "results": results}),
        flush=True,
    )
