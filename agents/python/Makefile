# Python agent (openlog-agent). Every target runs inside a python container, so no local Python is needed
# (PY=local runs the commands with the current interpreter, e.g. in CI).
PYTHON_VERSION ?= 3.12
VENV_VOLUME ?= openlog-m4-python-venv$(subst .,,$(PYTHON_VERSION))
COMPOSE := docker compose -p openlog-m4-python-test -f tests/integration/docker-compose.yml

ifeq ($(PY),local)
RUN := sh -c
PYBIN := python
else
RUN := docker run --rm -v "$(CURDIR)/..":/agents -v $(VENV_VOLUME):/venv -w /agents/python python:$(PYTHON_VERSION)-slim sh -c
PYBIN := /venv/bin/python
endif

.PHONY: install lint test test-unit test-e2e test-integration bench build clean

install:
ifeq ($(PY),local)
	$(RUN) '$(PYBIN) -m pip install -q -e . -r tests/requirements-test.txt'
else
	$(RUN) 'command -v gcc >/dev/null || (apt-get update -qq && apt-get install -y -qq --no-install-recommends gcc libc6-dev >/dev/null); test -x /venv/bin/python || python -m venv /venv; $(PYBIN) -m pip install -q -e . -r tests/requirements-test.txt'
endif

lint:
	$(RUN) '$(PYBIN) -m ruff check src tests && $(PYBIN) -m ruff format --check src'

# Unit tests (Go sampler fixtures, sanitizer cases shared with Go/Node) + end-to-end tests with Flask, Django,
# FastAPI, Tornado, Falcon, Pyramid, gunicorn and uWSGI pre-fork workers and gevent/eventlet against an OTLP capture server.
test:
	$(RUN) '$(PYBIN) -m pytest -q -p no:cacheprovider tests/unit tests/e2e'

test-unit:
	$(RUN) '$(PYBIN) -m pytest -q -p no:cacheprovider tests/unit'

test-e2e:
	$(RUN) '$(PYBIN) -m pytest -q -p no:cacheprovider tests/e2e'

# Real PostgreSQL, MySQL, Redis, RabbitMQ and Kafka (+ Celery prefork worker, gRPC) in compose project openlog-m4-python-test.
test-integration:
	$(COMPOSE) up -d --wait postgres mysql redis
	PYTHON_VERSION=$(PYTHON_VERSION) $(COMPOSE) --profile runner run --rm --build runner; status=$$?; $(COMPOSE) down -v; exit $$status

# Overhead micro-benchmark (Flask/gunicorn and FastAPI/uvicorn, with and without the agent).
bench:
	$(RUN) '$(PYBIN) tests/bench/overhead.py'

# Wheel + sdist into dist/ (pure Python).
build:
	$(RUN) '$(PYBIN) -m pip install -q build && $(PYBIN) -m build --outdir dist .'

clean:
	rm -rf dist build .pytest_cache src/*.egg-info
	find . -name __pycache__ -type d -prune -exec rm -rf {} +
