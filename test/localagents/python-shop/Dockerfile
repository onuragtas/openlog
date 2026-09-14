# python-shop (FastAPI) instrumented with the openlog Python agent, installed like the published package: a wheel
# built from agents/python, then `pip install openlog-agent` from that wheel.
# Build contexts (test/localagents/docker-compose.yml): . = test/localagents/python-shop, pyagent = ../../agents/python.

FROM python:3.13-slim AS agent
WORKDIR /src
COPY --from=pyagent pyproject.toml README.md LICENSE ./
COPY --from=pyagent src ./src
RUN pip install --no-cache-dir --root-user-action=ignore build==1.6.1 \
 && python -m build --wheel --outdir /out .

FROM python:3.13-slim
ENV PYTHONUNBUFFERED=1 PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /app
COPY --from=agent /out/ /tmp/wheels/
RUN pip install --no-cache-dir --root-user-action=ignore /tmp/wheels/openlog_agent-*.whl \
      fastapi==0.141.1 uvicorn==0.52.4 httpx==0.28.1 \
 && rm -rf /tmp/wheels
COPY shop.py loadgen.py ./

USER nobody
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/healthz', timeout=2)"
# zero code changes: openlog-instrument in front of the server command (each uvicorn worker initializes the agent)
CMD ["openlog-instrument", "uvicorn", "shop:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "2", "--no-access-log"]
