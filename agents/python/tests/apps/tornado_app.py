"""Tornado sample app (asyncio; AsyncHTTPClient for the chained call)."""

import asyncio
import logging
import os

import tornado.httpclient
import tornado.web

PORT = int(os.environ.get("PORT", "8000"))
log = logging.getLogger("shop")


class Ready(tornado.web.RequestHandler):
    def get(self):
        self.write("ok")


class User(tornado.web.RequestHandler):
    def get(self, user_id):
        log.warning("loading user %s", user_id)
        self.write({"id": int(user_id), "pid": os.getpid()})


class Boom(tornado.web.RequestHandler):
    def get(self):
        raise ValueError("tornado boom 42")


class Chain(tornado.web.RequestHandler):
    async def get(self):
        r = await tornado.httpclient.AsyncHTTPClient().fetch(f"http://127.0.0.1:{PORT}/users/7")
        self.write({"upstream": r.body.decode()})


def make_app():
    return tornado.web.Application(
        [(r"/ready", Ready), (r"/users/(\d+)", User), (r"/boom", Boom), (r"/chain", Chain)],
        log_function=lambda handler: None,
    )


async def main():
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s trace_id=%(trace_id)s")
    make_app().listen(PORT, address="127.0.0.1")
    await asyncio.Event().wait()


if __name__ == "__main__":
    asyncio.run(main())
