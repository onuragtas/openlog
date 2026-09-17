"""Starts sample applications (tests/apps) as subprocesses, optionally under openlog-instrument."""

from __future__ import annotations

import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path
from typing import Dict, List, Optional

APPS = Path(__file__).resolve().parents[1] / "apps"
BIN = Path(sys.executable).parent


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class App:
    def __init__(self, argv: List[str], env: Dict[str, str], port: int, instrument: bool = True) -> None:
        self.port = port
        self.url = f"http://127.0.0.1:{port}"
        # True once stop() had to kill the process because it outlived the timeout.
        self.killed = False
        self._out = tempfile.NamedTemporaryFile(prefix="openlog-app-", suffix=".log", delete=False)
        full_env = {k: v for k, v in os.environ.items() if not k.startswith(("OPENLOG_", "OTEL_"))}
        full_env.update(
            {"PATH": f"{BIN}{os.pathsep}{os.environ.get('PATH', '')}", "PYTHONUNBUFFERED": "1", "PORT": str(port)}
        )
        full_env.update(env)
        cmd = ([str(BIN / "openlog-instrument")] if instrument else []) + argv
        self.proc = subprocess.Popen(cmd, cwd=str(APPS), env=full_env, stdout=self._out, stderr=subprocess.STDOUT)

    def output(self) -> str:
        with open(self._out.name, encoding="utf-8", errors="replace") as f:
            return f.read()

    def wait_ready(self, path: str = "/ready", timeout: float = 30.0) -> App:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                raise AssertionError(f"app exited with {self.proc.returncode}:\n{self.output()}")
            try:
                with urllib.request.urlopen(self.url + path, timeout=2) as r:
                    r.read()
                    return self
            except Exception:
                time.sleep(0.1)
        raise AssertionError(f"app not ready:\n{self.output()}")

    def get(self, path: str, headers: Optional[Dict[str, str]] = None) -> tuple:
        req = urllib.request.Request(self.url + path, headers=headers or {})
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                return r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    def stop(self, sig: int = signal.SIGTERM, timeout: float = 20.0) -> int:
        """Signal the process and wait for it. Sets killed when it had to be killed after timeout, so a caller
        asserting on the exit code can say "did not react to the signal" instead of reporting a bare -9."""
        self.killed = False
        if self.proc.poll() is None:
            self.proc.send_signal(sig)
            try:
                self.proc.wait(timeout)
            except subprocess.TimeoutExpired:
                self.killed = True
                self.proc.kill()
                self.proc.wait()
        return self.proc.returncode

    def exited_on(self, sig: int, timeout: float = 20.0) -> str:
        """Stops the process and returns "" when it exited on sig, else why it did not. A loaded CI runner can
        need longer than the timeout, which is not the same failure as a shutdown the agent blocks."""
        return self._stopped(sig, timeout, -sig)

    def exited_cleanly(self, sig: int, timeout: float = 20.0) -> str:
        """Like exited_on, for supervisors (gunicorn, uWSGI) that stop their workers and exit 0 themselves."""
        return self._stopped(sig, timeout, 0)

    def _stopped(self, sig: int, timeout: float, want: int) -> str:
        rc = self.stop(sig, timeout)
        if self.killed:
            return f"did not exit within {timeout:g}s of {signal.Signals(sig).name}, had to be killed"
        if rc != want:
            expected = f"-{signal.Signals(sig).name}" if want < 0 else str(want)
            return f"exited with {rc}, expected {expected}"
        return ""

    def close(self) -> None:
        self.stop(signal.SIGKILL, 5)
        try:
            os.unlink(self._out.name)
        except OSError:
            pass


def agent_env(capture_url: str, service: str, **extra: str) -> Dict[str, str]:
    env = {
        "OPENLOG_ENDPOINT": capture_url,
        "OPENLOG_LICENSE_KEY": "test-license-key",
        "OPENLOG_SERVICE_NAME": service,
        "OPENLOG_SERVICE_VERSION": "1.0.0",
        "OPENLOG_ENVIRONMENT": "test",
        "OPENLOG_METRIC_EXPORT_INTERVAL": "1s",
        "OPENLOG_LOG_LEVEL": "info",
        "OPENLOG_HOST_ID": "e2e-host-0001",
    }
    env.update(extra)
    return env
