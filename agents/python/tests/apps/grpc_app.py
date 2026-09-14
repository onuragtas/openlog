"""gRPC server and client in one process (generic handler, no generated code)."""

from concurrent import futures

import grpc
from opentelemetry import trace


def get_item(request: bytes, context) -> bytes:
    if request == b"missing":
        context.abort(grpc.StatusCode.NOT_FOUND, "no such item")
    return b"item:" + request


if __name__ == "__main__":
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    handler = grpc.method_handlers_generic_handler(
        "shop.Catalog",
        {"GetItem": grpc.unary_unary_rpc_method_handler(get_item, request_deserializer=None, response_serializer=None)},
    )
    server.add_generic_rpc_handlers((handler,))
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
        call = channel.unary_unary("/shop.Catalog/GetItem", request_serializer=None, response_deserializer=None)
        with trace.get_tracer("grpc_app").start_as_current_span("grpc-job"):
            assert call(b"42") == b"item:42"
            try:
                call(b"missing")
            except grpc.RpcError:
                pass
    server.stop(0)
    print("done", flush=True)
