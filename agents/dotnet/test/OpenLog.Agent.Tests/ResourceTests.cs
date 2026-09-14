using System;
using System.Collections.Generic;
using System.IO;
using OpenLog.Agent;
using Xunit;

namespace OpenLog.Agent.Tests;

public sealed class ResourceTests : IDisposable
{
    private readonly string root = Path.Combine(Path.GetTempPath(), "openlog-res-" + Guid.NewGuid().ToString("N"));

    public ResourceTests()
    {
        Directory.CreateDirectory(root);
    }

    public void Dispose()
    {
        try
        {
            Directory.Delete(root, true);
        }
        catch (IOException)
        {
        }
    }

    private void Write(string path, string content)
    {
        var full = Path.Combine(root, path.TrimStart('/'));
        Directory.CreateDirectory(Path.GetDirectoryName(full)!);
        File.WriteAllText(full, content);
    }

    [Fact]
    public void HostIdChainPrefersTheRunningInfraAgent()
    {
        var hfs = new HostFS(root);
        Write("/etc/machine-id", "ABCDEF0123456789abcdef0123456789\n");
        Write("/var/lib/openlog-infra-agent/host-id", "state-generated-id\n");
        Assert.Equal(("abcdef0123456789abcdef0123456789", "/etc/machine-id"), OpenLogResourceDetector.ResolveHostId(hfs, "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", Path.Combine(root, "state"), true));
        Write("/run/openlog-infra-agent/host-id", "a0de0000000000000000000000000001\n");
        Assert.Equal(("a0de0000000000000000000000000001", "infra-agent"), OpenLogResourceDetector.ResolveHostId(hfs, "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", Path.Combine(root, "state"), true));
    }

    [Fact]
    public void HostIdSkipsInvalidAndZeroIdsThenInfraStateThenGenerated()
    {
        var hfs = new HostFS(root);
        Write("/etc/machine-id", "00000000000000000000000000000000");
        Write("/var/lib/dbus/machine-id", "short");
        Write("/var/lib/openlog-infra-agent/host-id", "3f2c1d7e-0000-4000-8000-000000000001");
        Assert.Equal(("3f2c1d7e-0000-4000-8000-000000000001", "infra-agent-state"), OpenLogResourceDetector.ResolveHostId(hfs, "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", Path.Combine(root, "state"), true));

        var empty = new HostFS(Path.Combine(root, "empty"));
        var state = Path.Combine(root, "state");
        var (id, source) = OpenLogResourceDetector.ResolveHostId(empty, "/nonexistent-openlog-runtime", "", state, true);
        Assert.Equal("generated", source);
        Assert.Matches("^[0-9a-f-]{36}$", id);
        Assert.Equal((id, "generated"), OpenLogResourceDetector.ResolveHostId(empty, "/nonexistent-openlog-runtime", "", state, true));
    }

    [Fact]
    public void PlatformHostIdOnNonLinux()
    {
        var previous = OpenLogResourceDetector.PlatformHostId;
        try
        {
            OpenLogResourceDetector.PlatformHostId = () => "platform-uuid-1234";
            var empty = new HostFS(Path.Combine(root, "empty"));
            Assert.Equal(("platform-uuid-1234", "platform"), OpenLogResourceDetector.ResolveHostId(empty, "/nonexistent-openlog-runtime", "", Path.Combine(root, "state"), false));
        }
        finally
        {
            OpenLogResourceDetector.PlatformHostId = previous;
        }
    }

    [Fact]
    public void ContainerIdFromCgroupV1AndMountinfo()
    {
        const string id = "3b1f9e7c2d4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c";
        Write("/proc/self/cgroup", $"12:memory:/docker/{id}\n11:cpu:/docker/{id}\n");
        Assert.Equal(id, OpenLogResourceDetector.ContainerId(new HostFS(root)));

        Write("/proc/self/cgroup", "0::/\n");
        Write("/proc/self/mountinfo",
            "1 2 0:1 /containers/sandboxes/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/shm /dev/shm rw\n" +
            $"3 2 254:1 /docker/containers/{id}/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw\n");
        Assert.Equal(id, OpenLogResourceDetector.ContainerId(new HostFS(root)));

        Write("/proc/self/cgroup", "0:cpu:/kubepods.slice/kubepods-pod1.slice/cri-containerd-" + id + ".scope\n");
        Assert.Equal(id, OpenLogResourceDetector.ContainerId(new HostFS(root)));
    }

    [Fact]
    public void ResourceAttributesPrecedence()
    {
        Write("/etc/machine-id", "abcdef0123456789abcdef0123456789");
        Write("/etc/os-release", "ID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n");
        Write("/proc/sys/kernel/hostname", "web-1\n");
        Write("/proc/sys/kernel/arch", "aarch64\n");
        var cfg = ConfigLoader.Load(new Dictionary<string, string?>
        {
            ["OPENLOG_SERVICE_NAME"] = "checkout",
            ["OPENLOG_ENVIRONMENT"] = "prod",
            ["OPENLOG_HOST_ROOT"] = root,
            ["OPENLOG_INFRA_RUNTIME_DIR"] = "/nonexistent-openlog-runtime",
            ["OPENLOG_RESOURCE_ATTRIBUTES"] = "service.name=ignored,team=payments,host.name=override",
        }, null, out _);
        var env = new Dictionary<string, string?> { ["K8S_POD_NAME"] = "checkout-7d9", ["KUBERNETES_SERVICE_HOST"] = "10.0.0.1", ["HOSTNAME"] = "fallback" };
        var detector = new OpenLogResourceDetector(cfg, env, linux: true);
        var a = detector.Attributes();
        Assert.Equal("checkout", a["service.name"]);
        Assert.Equal("prod", a["deployment.environment.name"]);
        Assert.Equal("payments", a["team"]);
        Assert.Equal("override", a["host.name"]);
        Assert.Equal("abcdef0123456789abcdef0123456789", a["host.id"]);
        Assert.Equal("/etc/machine-id", detector.HostIdSource);
        Assert.Equal("arm64", a["host.arch"]);
        Assert.Equal("linux", a["os.type"]);
        Assert.Equal("debian", a["os.name"]);
        Assert.Equal("12", a["os.version"]);
        Assert.Equal("checkout-7d9", a["k8s.pod.name"]);
        Assert.Equal("openlog", a["telemetry.distro.name"]);
        Assert.Equal(AgentVersion.Version, a["telemetry.distro.version"]);
        Assert.IsType<long>(a["process.pid"]);
        Assert.Equal(".NET", a["process.runtime.name"]);

        var explicitHost = ConfigLoader.Load(new Dictionary<string, string?> { ["OPENLOG_HOST_ID"] = "explicit-host-id", ["OPENLOG_HOST_ROOT"] = root }, null, out _);
        var d2 = new OpenLogResourceDetector(explicitHost, new Dictionary<string, string?>(), linux: true);
        Assert.Equal("explicit-host-id", d2.Attributes()["host.id"]);
        Assert.Equal("config", d2.HostIdSource);
    }

    [Theory]
    [InlineData("x86_64", "amd64")]
    [InlineData("x64", "amd64")]
    [InlineData("aarch64", "arm64")]
    [InlineData("armv7l", "arm32")]
    [InlineData("i686", "x86")]
    [InlineData("riscv64", "riscv64")]
    public void Arch(string input, string want) => Assert.Equal(want, OpenLogResourceDetector.NormalizeArch(input));
}
