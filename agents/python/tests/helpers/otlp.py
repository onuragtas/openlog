"""OTLP/HTTP capture server for tests: decodes traces, metrics and logs (gzip or plain) into plain dicts."""

from __future__ import annotations

import gzip
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Dict, List, Optional

from opentelemetry.proto.collector.logs.v1.logs_service_pb2 import ExportLogsServiceRequest
from opentelemetry.proto.collector.metrics.v1.metrics_service_pb2 import ExportMetricsServiceRequest
from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import ExportTraceServiceRequest

SERVER, CLIENT, INTERNAL, PRODUCER, CONSUMER = 2, 3, 1, 4, 5


def any_value(v: Any) -> Any:
    kind = v.WhichOneof("value")
    if kind is None:
        return None
    if kind == "array_value":
        return [any_value(x) for x in v.array_value.values]
    if kind == "kvlist_value":
        return {kv.key: any_value(kv.value) for kv in v.kvlist_value.values}
    return getattr(v, kind)


def attrs(kvs: Any) -> Dict[str, Any]:
    return {kv.key: any_value(kv.value) for kv in kvs}


class Capture:
    def __init__(self) -> None:
        self.requests: List[Dict[str, Any]] = []
        self.spans: List[Dict[str, Any]] = []
        self.metrics: List[Dict[str, Any]] = []
        self.logs: List[Dict[str, Any]] = []
        self.fail_with: Optional[int] = None
        self._lock = threading.Lock()
        capture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args: Any) -> None:  # quiet
                pass

            def do_POST(self) -> None:  # noqa: N802
                body = self.rfile.read(int(self.headers.get("content-length") or 0))
                with capture._lock:
                    capture.requests.append(
                        {"path": self.path, "headers": {k.lower(): v for k, v in self.headers.items()}}
                    )
                if capture.fail_with:
                    self.send_response(capture.fail_with)
                    self.send_header("retry-after", "0")
                    self.end_headers()
                    return
                try:
                    if self.headers.get("content-encoding") == "gzip":
                        body = gzip.decompress(body)
                    capture._decode(self.path, body)
                except Exception as exc:  # pragma: no cover
                    self.send_response(400)
                    self.end_headers()
                    self.wfile.write(str(exc).encode())
                    return
                self.send_response(200)
                self.send_header("content-type", "application/x-protobuf")
                self.send_header("content-length", "0")
                self.end_headers()

        self._server = ThreadingHTTPServer(("0.0.0.0", 0), Handler)
        self._server.daemon_threads = True
        self.port = self._server.server_address[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def _decode(self, path: str, body: bytes) -> None:
        if path == "/v1/traces":
            req = ExportTraceServiceRequest.FromString(body)
            out = []
            for rs in req.resource_spans:
                res = attrs(rs.resource.attributes)
                for ss in rs.scope_spans:
                    for s in ss.spans:
                        out.append(
                            {
                                "trace_id": s.trace_id.hex(),
                                "span_id": s.span_id.hex(),
                                "parent_span_id": s.parent_span_id.hex(),
                                "trace_state": s.trace_state,
                                "name": s.name,
                                "kind": s.kind,
                                "flags": s.flags,
                                "attributes": attrs(s.attributes),
                                "events": [{"name": e.name, "attributes": attrs(e.attributes)} for e in s.events],
                                "status": {"code": s.status.code, "message": s.status.message},
                                "resource": res,
                                "scope": ss.scope.name,
                                "links": [l.trace_id.hex() for l in s.links],
                            }
                        )
            with self._lock:
                self.spans.extend(out)
        elif path == "/v1/metrics":
            req = ExportMetricsServiceRequest.FromString(body)
            out = []
            for rm in req.resource_metrics:
                res = attrs(rm.resource.attributes)
                for sm in rm.scope_metrics:
                    for m in sm.metrics:
                        kind = m.WhichOneof("data")
                        data = getattr(m, kind)
                        points = []
                        for p in data.data_points:
                            pt: Dict[str, Any] = {"attributes": attrs(p.attributes)}
                            if kind in ("gauge", "sum"):
                                pt["value"] = p.as_double if p.WhichOneof("value") == "as_double" else p.as_int
                            elif kind == "histogram":
                                pt.update(count=p.count, sum=p.sum, bounds=list(p.explicit_bounds))
                            points.append(pt)
                        out.append(
                            {
                                "name": m.name,
                                "unit": m.unit,
                                "type": kind,
                                "monotonic": getattr(data, "is_monotonic", None),
                                "points": points,
                                "resource": res,
                                "scope": sm.scope.name,
                            }
                        )
            with self._lock:
                self.metrics.extend(out)
        elif path == "/v1/logs":
            req = ExportLogsServiceRequest.FromString(body)
            out = []
            for rl in req.resource_logs:
                res = attrs(rl.resource.attributes)
                for sl in rl.scope_logs:
                    for r in sl.log_records:
                        out.append(
                            {
                                "body": any_value(r.body),
                                "severity_number": r.severity_number,
                                "severity_text": r.severity_text,
                                "attributes": attrs(r.attributes),
                                "trace_id": r.trace_id.hex(),
                                "span_id": r.span_id.hex(),
                                "resource": res,
                                "scope": sl.scope.name,
                            }
                        )
            with self._lock:
                self.logs.extend(out)

    def wait_for(self, what: str, fn: Callable[[], Any], timeout: float = 20.0) -> Any:
        deadline = time.monotonic() + timeout
        while True:
            with self._lock:
                v = fn()
            if v:
                return v
            if time.monotonic() > deadline:
                raise AssertionError(
                    f"timed out waiting for {what}; spans: {[(s['name'], s['kind']) for s in self.spans]}; "
                    f"metrics: {sorted({m['name'] for m in self.metrics})}; logs: {len(self.logs)}"
                )
            time.sleep(0.05)

    def spans_named(self, name: str) -> List[Dict[str, Any]]:
        return [s for s in self.spans if s["name"] == name]

    def metric_names(self) -> set:
        return {m["name"] for m in self.metrics}

    def close(self) -> None:
        self._server.shutdown()
        self._server.server_close()
