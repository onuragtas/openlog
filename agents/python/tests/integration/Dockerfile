# Test runner for the integration suite (docker-compose.yml service `runner`): Python + the build dependencies of
# mysqlclient + the pinned test requirements. The agent source is mounted and installed at run time.
ARG PYTHON_VERSION=3.12
FROM python:${PYTHON_VERSION}-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends gcc pkg-config default-libmysqlclient-dev \
 && rm -rf /var/lib/apt/lists/*
COPY requirements-test.txt requirements-integration.txt /tmp/req/
RUN pip install --no-cache-dir -r /tmp/req/requirements-integration.txt
WORKDIR /src/agents/python
