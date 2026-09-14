"""Resource detection, a port of the Go agent's resource.go (and the Node.js agent's resource.ts).

The same host.id chain as the infra agent (semantic-conventions.md §1), container.id from cgroup/mountinfo, OS,
process and Kubernetes attributes.
"""

from __future__ import annotations

import getpass
import os
import platform as _platform
import posixpath
import re
import socket
import subprocess
import sys
import uuid
from typing import Dict, Mapping, Optional, Tuple, Union

from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.version import __version__ as SDK_VERSION

from .config import Config
from .version import DISTRO_NAME, __version__

AttrValue = Union[str, int]


class HostFS:
    """Reads host files below a root prefix (tests use a temp dir; containers may mount the host root at /host)."""

    def __init__(self, root: str = "/") -> None:
        self.root = root or "/"

    def path(self, p: str) -> str:
        clean = posixpath.normpath("/" + p)
        if clean.startswith("//"):
            clean = "/" + clean.lstrip("/")
        if self.root == "/":
            return clean
        return os.path.join(self.root, clean.lstrip("/"))

    def read(self, p: str) -> Optional[str]:
        try:
            with open(self.path(p), encoding="utf-8", errors="replace") as f:
                return f.read()
        except OSError:
            return None

    def read_trim(self, p: str) -> str:
        return (self.read(p) or "").strip()


#: The infra agent's validity rule (agents/infra/internal/resource).
VALID_HOST_ID = re.compile(r"^[0-9A-Za-z-]{8,}$")

#: The infra agent's resolution chain (semantic-conventions.md §1). Keep in sync with agents/go/resource.go.
HOST_ID_FILES = ("/etc/machine-id", "/var/lib/dbus/machine-id", "/sys/class/dmi/id/product_uuid")


def _valid(v: str) -> bool:
    return bool(VALID_HOST_ID.match(v))


def platform_host_id() -> str:
    """Platform machine id on non-Linux systems (macOS IOPlatformUUID, Windows MachineGuid)."""
    try:
        if sys.platform == "darwin":
            out = subprocess.run(
                ["ioreg", "-rd1", "-c", "IOPlatformExpertDevice"],
                capture_output=True,
                text=True,
                timeout=2,
                check=False,
            ).stdout
            m = re.search(r'"IOPlatformUUID"\s*=\s*"([^"]+)"', out)
            return m.group(1).lower() if m else ""
        if sys.platform == "win32":
            import winreg  # pylint: disable=import-outside-toplevel,import-error

            with winreg.OpenKey(winreg.HKEY_LOCAL_MACHINE, r"SOFTWARE\Microsoft\Cryptography") as key:  # type: ignore[attr-defined]
                return str(winreg.QueryValueEx(key, "MachineGuid")[0]).lower()  # type: ignore[attr-defined]
    except Exception:  # pylint: disable=broad-except
        pass
    return ""


def _user_cache_dir() -> str:
    home = os.path.expanduser("~")
    if sys.platform == "darwin":
        return os.path.join(home, "Library", "Caches")
    if sys.platform == "win32":
        return os.environ.get("LOCALAPPDATA") or os.path.join(home, "AppData", "Local")
    return os.environ.get("XDG_CACHE_HOME") or os.path.join(home, ".cache")


def resolve_host_id(
    hfs: HostFS,
    infra_runtime_dir: str,
    infra_state_dir: str,
    state_dir: str,
    linux: Optional[bool] = None,
    platform_id=platform_host_id,
) -> Tuple[str, str]:
    """Resolves host.id like the infra agent, so both report the same id on one machine.

    0. the id a running infra agent published in <infra_runtime_dir>/host-id (under the host root, else the plain path)
    1. /etc/machine-id → /var/lib/dbus/machine-id → /sys/class/dmi/id/product_uuid (valid, not all zeros; lower-cased)
    2. the UUID the infra agent generated in its state dir (<infra_state_dir>/host-id)
    3. non-Linux: the platform machine id
    4. a UUID generated and persisted by this agent (state_dir, default <user cache dir>/openlog/host-id)
    """
    if linux is None:
        linux = sys.platform.startswith("linux")
    if infra_runtime_dir:
        file = posixpath.join(infra_runtime_dir, "host-id")
        v = hfs.read_trim(file)
        if not _valid(v) and hfs.path(file) != posixpath.normpath(file):
            v = HostFS("/").read_trim(file)
        if _valid(v):
            return v, "infra-agent"
    for p in HOST_ID_FILES:
        v = hfs.read_trim(p)
        if _valid(v) and v.replace("0", "").replace("-", "") != "":
            return v.lower(), p
    if infra_state_dir:
        v = hfs.read_trim(posixpath.join(infra_state_dir, "host-id"))
        if _valid(v):
            return v, "infra-agent-state"
    if not linux:
        v = platform_id()
        if v:
            return v, "platform"
    directory = state_dir or os.path.join(_user_cache_dir(), "openlog")
    file = os.path.join(directory, "host-id")
    try:
        with open(file, encoding="utf-8") as f:
            v = f.read().strip()
        if _valid(v):
            return v, "generated"
    except OSError:
        pass
    gen = str(uuid.uuid4())
    try:
        os.makedirs(directory, mode=0o750, exist_ok=True)
        tmp = f"{file}.{os.getpid()}.tmp"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o640)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(gen + "\n")
        os.replace(tmp, file)
    except OSError:
        return "", ""
    return gen, "generated"


_CONTAINER_ID_IN_CGROUP = re.compile(r"([0-9a-f]{64})(?:\.scope)?$")
_CONTAINER_ID_IN_MOUNTINFO = re.compile(r"containers/([0-9a-f]{64})/")


def container_id(hfs: HostFS) -> str:
    """The id of the container this process runs in.

    The last 64-hex segment of /proc/self/cgroup (cgroup v1, or v2 without a cgroup namespace), otherwise the
    Docker/Podman container directory in /proc/self/mountinfo (cgroup v2 with a private cgroup namespace, where
    /proc/self/cgroup is just "0::/").
    """
    cgroup = hfs.read("/proc/self/cgroup")
    if cgroup:
        for line in cgroup.split("\n"):
            parts = line.split(":")
            if len(parts) < 3:
                continue
            segs = ":".join(parts[2:]).split("/")
            for seg in reversed(segs):
                m = _CONTAINER_ID_IN_CGROUP.search(seg)
                if m:
                    return m.group(1)
    mountinfo = hfs.read("/proc/self/mountinfo")
    if mountinfo:
        for line in mountinfo.split("\n"):
            if "/sandboxes/" in line:  # containerd pod sandbox, not this container
                continue
            m = _CONTAINER_ID_IN_MOUNTINFO.search(line)
            if m:
                return m.group(1)
    return ""


def normalize_arch(a: str) -> str:
    """Maps kernel/Python architecture names to OTel host.arch values (same as the infra and Go agents)."""
    a = a.lower()
    if a in ("x86_64", "amd64", "x64"):
        return "amd64"
    if a in ("aarch64", "arm64"):
        return "arm64"
    if a in ("i386", "i686", "386", "x86", "ia32"):
        return "x86"
    if a in ("armv7l", "armv6l", "arm"):
        return "arm32"
    if a in ("ppc64le", "ppc64"):
        return "ppc64"
    return a


def os_type(p: str = sys.platform) -> str:
    if p.startswith("linux"):
        return "linux"
    if p == "win32":
        return "windows"
    if p.startswith("freebsd"):
        return "freebsd"
    if p.startswith("sunos"):
        return "solaris"
    return p


def parse_os_release(s: str) -> Dict[str, str]:
    out: Dict[str, str] = {}
    for raw in s.split("\n"):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        i = line.find("=")
        if i < 0:
            continue
        v = line[i + 1 :]
        if len(v) >= 2 and ((v[0] == '"' and v[-1] == '"') or (v[0] == "'" and v[-1] == "'")):
            v = re.sub(r"\\([\"'\\$`])", r"\1", v[1:-1])
        out[line[:i]] = v
    return out


def k8s_attributes(hfs: HostFS, env: Mapping[str, str]) -> Dict[str, str]:
    """Kubernetes metadata from downward-API environment variables (inside a pod with fallbacks)."""
    out: Dict[str, str] = {}

    def e(*names: str) -> str:
        for n in names:
            v = (env.get(n) or "").strip()
            if v:
                return v
        return ""

    def put(k: str, v: str) -> None:
        if v:
            out[k] = v

    put("k8s.pod.name", e("K8S_POD_NAME", "POD_NAME"))
    put("k8s.pod.uid", e("K8S_POD_UID", "POD_UID"))
    put("k8s.namespace.name", e("K8S_NAMESPACE_NAME", "K8S_NAMESPACE", "POD_NAMESPACE"))
    put("k8s.node.name", e("K8S_NODE_NAME", "NODE_NAME"))
    put("k8s.container.name", e("K8S_CONTAINER_NAME", "CONTAINER_NAME"))
    put("k8s.deployment.name", e("K8S_DEPLOYMENT_NAME"))
    put("k8s.cluster.name", e("K8S_CLUSTER_NAME"))
    if "KUBERNETES_SERVICE_HOST" in env:
        if "k8s.namespace.name" not in out:
            put("k8s.namespace.name", hfs.read_trim("/var/run/secrets/kubernetes.io/serviceaccount/namespace"))
        if "k8s.pod.name" not in out:
            put("k8s.pod.name", e("HOSTNAME"))
    return out


def _host_name(hfs: HostFS, linux: bool) -> str:
    if linux:
        v = hfs.read_trim("/proc/sys/kernel/hostname") or hfs.read_trim("/etc/hostname")
        if v:
            return v
    return socket.gethostname()


def detect_attributes(
    cfg: Config, env: Mapping[str, str], linux: Optional[bool] = None
) -> Tuple[Dict[str, AttrValue], str]:
    """Host, OS, process, container and Kubernetes attributes, plus the host.id source."""
    if linux is None:
        linux = sys.platform.startswith("linux")
    hfs = HostFS(cfg.host_root)
    a: Dict[str, AttrValue] = {
        "host.name": _host_name(hfs, linux),
        "host.arch": normalize_arch(_platform.machine()),
        "os.type": "linux" if linux else os_type(),
        "process.pid": os.getpid(),
        "process.executable.name": os.path.basename(sys.executable or "python"),
        "process.executable.path": sys.executable or "",
        "process.runtime.name": sys.implementation.name,
        "process.runtime.version": _platform.python_version(),
        "process.runtime.description": sys.version.replace("\n", " "),
    }
    # The script path only: command-line arguments are not sent, they may contain secrets.
    if sys.argv and sys.argv[0]:
        a["process.command"] = sys.argv[0]
    if linux:
        arch = hfs.read_trim("/proc/sys/kernel/arch")
        if arch:
            a["host.arch"] = normalize_arch(arch)
        for p in ("/etc/os-release", "/usr/lib/os-release"):
            s = hfs.read(p)
            if s is not None:
                osr = parse_os_release(s)
                if osr.get("ID"):
                    a["os.name"] = osr["ID"]
                if osr.get("VERSION_ID"):
                    a["os.version"] = osr["VERSION_ID"]
                if osr.get("PRETTY_NAME"):
                    a["os.description"] = osr["PRETTY_NAME"]
                break
        kr = hfs.read_trim("/proc/sys/kernel/osrelease")
        if kr:
            a["openlog.os.kernel_release"] = kr
        cid = container_id(hfs)
        if cid:
            a["container.id"] = cid
    else:
        a["os.version"] = _platform.release()
    try:
        a["process.owner"] = getpass.getuser()
    except Exception:  # pylint: disable=broad-except  # no passwd entry (arbitrary container uid)
        pass
    a.update(k8s_attributes(hfs, env))
    source = "config"
    if not cfg.host_id:
        hid, source = resolve_host_id(hfs, cfg.infra_runtime_dir, cfg.infra_state_dir, cfg.state_dir, linux)
        if hid:
            a["host.id"] = hid
    return a, source


def build_resource(
    cfg: Config, env: Optional[Mapping[str, str]] = None, linux: Optional[bool] = None
) -> Tuple[Resource, str]:
    """Detected attributes < user resource attributes < explicit service/environment/host id settings."""
    attrs, source = detect_attributes(cfg, os.environ if env is None else env, linux)
    attrs.update(cfg.resource_attributes)
    attrs["service.name"] = cfg.service_name
    if cfg.service_version:
        attrs["service.version"] = cfg.service_version
    if cfg.service_namespace:
        attrs["service.namespace"] = cfg.service_namespace
    if cfg.environment:
        attrs["deployment.environment.name"] = cfg.environment
    if cfg.host_id:
        attrs["host.id"] = cfg.host_id
    pid = attrs.get("process.pid")
    if isinstance(pid, str) and pid.isdigit():
        attrs["process.pid"] = int(pid)
    attrs["telemetry.sdk.name"] = "opentelemetry"
    attrs["telemetry.sdk.language"] = "python"
    attrs["telemetry.sdk.version"] = SDK_VERSION
    attrs["telemetry.distro.name"] = DISTRO_NAME
    attrs["telemetry.distro.version"] = __version__
    return Resource(attrs), source


def update_process_pid(resource: Resource, pid: Optional[int] = None) -> None:
    """Sets process.pid of a resource shared by the providers after fork (pre-fork servers, Celery prefork pool).

    Resources are immutable by API; the providers, their exporters and every span reference the same object, so the
    attribute is replaced in place. Only process.pid changes: a forked worker is the same service on the same host.
    """
    attrs = getattr(resource, "_attributes", None)
    inner = getattr(attrs, "_dict", None)
    if inner is None or "process.pid" not in inner:
        return
    inner["process.pid"] = os.getpid() if pid is None else pid
