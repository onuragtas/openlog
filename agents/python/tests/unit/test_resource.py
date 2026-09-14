import os

from openlog_agent.config import load_config
from openlog_agent.resource import (
    HostFS,
    build_resource,
    container_id,
    k8s_attributes,
    normalize_arch,
    parse_os_release,
    resolve_host_id,
    update_process_pid,
)

CID = "a" * 64
CID2 = "b" * 64


def write(root, path, content):
    full = os.path.join(root, path.lstrip("/"))
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w") as f:
        f.write(content)


def test_host_id_chain(tmp_path):
    root = str(tmp_path / "host")
    state = str(tmp_path / "state")
    hfs = HostFS(root)
    # nothing → generated and persisted
    hid, src = resolve_host_id(hfs, "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state, linux=True)
    assert src == "generated" and len(hid) == 36
    assert resolve_host_id(hfs, "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state, linux=True) == (
        hid,
        "generated",
    )
    # infra agent state dir
    write(root, "/var/lib/openlog-infra-agent/host-id", "infra-generated-1234\n")
    assert resolve_host_id(hfs, "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state, linux=True) == (
        "infra-generated-1234",
        "infra-agent-state",
    )
    # all-zero machine-id is ignored, product_uuid is lower-cased
    write(root, "/etc/machine-id", "00000000000000000000000000000000\n")
    write(root, "/sys/class/dmi/id/product_uuid", "ABCDEF01-2345-6789-ABCD-EF0123456789\n")
    assert resolve_host_id(hfs, "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state, linux=True) == (
        "abcdef01-2345-6789-abcd-ef0123456789",
        "/sys/class/dmi/id/product_uuid",
    )
    write(root, "/etc/machine-id", "0123456789abcdef0123456789abcdef\n")
    assert resolve_host_id(hfs, "/run/openlog-infra-agent", "", state, linux=True)[1] == "/etc/machine-id"
    # a running infra agent's published id wins
    write(root, "/run/openlog-infra-agent/host-id", "published-by-infra-agent\n")
    assert resolve_host_id(hfs, "/run/openlog-infra-agent", "", state, linux=True) == (
        "published-by-infra-agent",
        "infra-agent",
    )
    # invalid content is skipped
    write(root, "/run/openlog-infra-agent/host-id", "short\n")
    assert resolve_host_id(hfs, "/run/openlog-infra-agent", "", state, linux=True)[1] == "/etc/machine-id"


def test_platform_id_on_non_linux(tmp_path):
    hfs = HostFS(str(tmp_path))
    assert resolve_host_id(hfs, "", "", str(tmp_path / "s"), linux=False, platform_id=lambda: "platform-uuid-1") == (
        "platform-uuid-1",
        "platform",
    )


def test_container_id():
    import tempfile

    with tempfile.TemporaryDirectory() as root:
        hfs = HostFS(root)
        assert container_id(hfs) == ""
        write(root, "/proc/self/cgroup", f"12:memory:/docker/{CID}\n0::/\n")
        assert container_id(hfs) == CID
        write(root, "/proc/self/cgroup", f"0::/system.slice/docker-{CID2}.scope\n")
        assert container_id(hfs) == CID2
        write(root, "/proc/self/cgroup", "0::/\n")
        write(
            root,
            "/proc/self/mountinfo",
            f"1 2 0:1 /var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/{CID2}/hostname /etc/hostname rw\n"
            f"3 4 0:5 /var/lib/docker/containers/{CID}/resolv.conf /etc/resolv.conf rw\n",
        )
        assert container_id(hfs) == CID


def test_helpers():
    assert normalize_arch("x86_64") == "amd64"
    assert normalize_arch("aarch64") == "arm64"
    assert normalize_arch("armv7l") == "arm32"
    assert parse_os_release('ID=debian\nPRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\n# c\nVERSION_ID="12"') == {
        "ID": "debian",
        "PRETTY_NAME": "Debian GNU/Linux 12 (bookworm)",
        "VERSION_ID": "12",
    }
    assert k8s_attributes(
        HostFS("/nonexistent"), {"KUBERNETES_SERVICE_HOST": "10.0.0.1", "HOSTNAME": "pod-1", "K8S_NODE_NAME": "n1"}
    ) == {
        "k8s.pod.name": "pod-1",
        "k8s.node.name": "n1",
    }


def test_build_resource(tmp_path):
    root = str(tmp_path)
    write(root, "/run/openlog-infra-agent/host-id", "infra-host-id-1\n")
    write(root, "/proc/self/cgroup", f"0::/docker/{CID}\n")
    write(root, "/etc/os-release", 'ID=alpine\nVERSION_ID=3.20\nPRETTY_NAME="Alpine Linux v3.20"\n')
    env = {
        "OPENLOG_HOST_ROOT": root,
        "OTEL_RESOURCE_ATTRIBUTES": "service.name=ignored,team=core,host.id=user",
        "OPENLOG_SERVICE_NAME": "checkout",
        "OPENLOG_SERVICE_VERSION": "1.2.3",
        "OPENLOG_ENVIRONMENT": "prod",
    }
    cfg, _ = load_config(env)
    res, src = build_resource(cfg, env, linux=True)
    a = res.attributes
    assert src == "infra-agent"
    assert a["service.name"] == "checkout"
    assert a["service.version"] == "1.2.3"
    assert a["deployment.environment.name"] == "prod"
    assert a["team"] == "core"
    assert a["host.id"] == "user"  # user resource attributes override detection
    assert a["container.id"] == CID
    assert a["os.name"] == "alpine"
    assert a["telemetry.distro.name"] == "openlog"
    assert a["telemetry.sdk.language"] == "python"
    assert a["process.runtime.name"] == "cpython"
    assert a["process.pid"] == os.getpid()
    assert "process.command_args" not in a
    cfg, _ = load_config(dict(env, OPENLOG_HOST_ID="explicit-host"))
    res, src = build_resource(cfg, env, linux=True)
    assert res.attributes["host.id"] == "explicit-host" and src == "config"


def test_update_process_pid():
    cfg, _ = load_config({"OPENLOG_HOST_ID": "explicit-host", "OPENLOG_SERVICE_NAME": "s"})
    res, _ = build_resource(cfg, {}, linux=False)
    update_process_pid(res, 424242)
    assert res.attributes["process.pid"] == 424242
