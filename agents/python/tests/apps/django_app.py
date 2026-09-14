"""Django sample app (DJANGO_SETTINGS_MODULE=django_settings), served by wsgiref."""

import logging
import os
from socketserver import ThreadingMixIn
from wsgiref.simple_server import WSGIRequestHandler, WSGIServer, make_server

from django.http import HttpResponse, JsonResponse
from django.urls import include, path

PORT = int(os.environ.get("PORT", "8000"))
log = logging.getLogger("shop")


def ready(request):
    return HttpResponse("ok")


def user(request, user_id):
    log.warning("django user %s", user_id)
    return JsonResponse({"id": user_id})


def boom(request):
    raise ValueError("django boom")


api = [path("users/<int:user_id>/", user)]
urlpatterns = [path("ready", ready), path("api/", include(api)), path("boom/", boom)]


class Server(ThreadingMixIn, WSGIServer):
    daemon_threads = True


class QuietHandler(WSGIRequestHandler):
    def log_message(self, *args):
        pass


if __name__ == "__main__":
    import django
    from django.core.wsgi import get_wsgi_application

    logging.basicConfig(level=logging.INFO)
    django.setup()
    application = get_wsgi_application()
    make_server("127.0.0.1", PORT, application, server_class=Server, handler_class=QuietHandler).serve_forever()
