using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Runtime.InteropServices;
using System.Text.RegularExpressions;
using OpenTelemetry.Resources;

namespace OpenLog.Agent;

/// <summary>Reads host files below a root prefix (tests use a temp dir; containers may mount the host root at /host).</summary>
internal sealed class HostFS
{
    public HostFS(string root = "/")
    {
        Root = string.IsNullOrEmpty(root) ? "/" : root;
    }

    public string Root { get; }

    public string PathOf(string p)
    {
        var clean = "/" + p.TrimStart('/');
        if (Root == "/") return clean;
        return System.IO.Path.Combine(Root, clean.TrimStart('/'));
    }

    public string? Read(string p)
    {
        try
        {
            return File.ReadAllText(PathOf(p));
        }
        catch (Exception)
        {
            return null;
        }
    }

    public string ReadTrim(string p) => (Read(p) ?? "").Trim();
}

/// <summary>
/// Resource detection, a port of the Go agent's resource.go: the same host.id chain as the infra agent
/// (semantic-conventions.md §1), container.id from cgroup/mountinfo, OS, process and Kubernetes attributes.
/// </summary>
public sealed class OpenLogResourceDetector : IResourceDetector
{
    /// <summary>telemetry.distro.name</summary>
    public const string DistroName = "openlog";

    /// <summary>The infra agent's validity rule (agents/infra/internal/resource).</summary>
    internal static readonly Regex ValidHostId = new("^[0-9A-Za-z-]{8,}$", RegexOptions.CultureInvariant);

    /// <summary>The infra agent's resolution chain (semantic-conventions.md §1). Keep in sync with agents/go/resource.go.</summary>
    internal static readonly string[] HostIdFiles = { "/etc/machine-id", "/var/lib/dbus/machine-id", "/sys/class/dmi/id/product_uuid" };

    private static readonly Regex ContainerIdInCgroup = new("([0-9a-f]{64})(?:\\.scope)?$", RegexOptions.CultureInvariant);
    private static readonly Regex ContainerIdInMountinfo = new("containers/([0-9a-f]{64})/", RegexOptions.CultureInvariant);

    private readonly OpenLogConfig cfg;
    private readonly IDictionary<string, string?> env;
    private readonly bool linux;

    public OpenLogResourceDetector(OpenLogConfig cfg) : this(cfg, ConfigLoader.ProcessEnvironment(), null) { }

    internal OpenLogResourceDetector(OpenLogConfig cfg, IDictionary<string, string?> env, bool? linux)
    {
        this.cfg = cfg;
        this.env = env;
        this.linux = linux ?? RuntimeInformation.IsOSPlatform(OSPlatform.Linux);
    }

    /// <summary>Where host.id came from: config, infra-agent, a machine-id file path, infra-agent-state, platform or generated.</summary>
    public string HostIdSource { get; private set; } = "";

    /// <summary>Platform machine id on non-Linux systems (macOS IOPlatformUUID, Windows MachineGuid). Replaceable in tests.</summary>
    internal static Func<string> PlatformHostId { get; set; } = DefaultPlatformHostId;

    public Resource Detect() => new(Attributes());

    /// <summary>Detected attributes &lt; user resource attributes &lt; explicit service/environment/host id settings.</summary>
    internal Dictionary<string, object> Attributes()
    {
        var a = DetectAttributes(out var source);
        HostIdSource = source;
        foreach (var kv in cfg.ResourceAttributes) a[kv.Key] = kv.Value;
        a["service.name"] = cfg.ServiceName;
        if (cfg.ServiceVersion.Length > 0) a["service.version"] = cfg.ServiceVersion;
        if (cfg.ServiceNamespace.Length > 0) a["service.namespace"] = cfg.ServiceNamespace;
        if (cfg.Environment.Length > 0) a["deployment.environment.name"] = cfg.Environment;
        if (cfg.HostId.Length > 0) a["host.id"] = cfg.HostId;
        a["telemetry.distro.name"] = DistroName;
        a["telemetry.distro.version"] = AgentVersion.Version;
        if (a.TryGetValue("process.pid", out var pid) && pid is string ps && long.TryParse(ps, out var pl)) a["process.pid"] = pl;
        return a;
    }

    private Dictionary<string, object> DetectAttributes(out string hostIdSource)
    {
        var hfs = new HostFS(cfg.HostRoot);
        using var process = Process.GetCurrentProcess();
        var a = new Dictionary<string, object>(StringComparer.Ordinal)
        {
            ["host.name"] = HostName(hfs),
            ["host.arch"] = NormalizeArch(RuntimeInformation.OSArchitecture.ToString().ToLowerInvariant()),
            ["os.type"] = OsType(),
            ["process.pid"] = (long)process.Id,
            ["process.runtime.name"] = ".NET",
            ["process.runtime.version"] = Environment.Version.ToString(),
            ["process.runtime.description"] = RuntimeInformation.FrameworkDescription,
        };
        try
        {
            var exe = process.MainModule?.FileName;
            if (!string.IsNullOrEmpty(exe))
            {
                a["process.executable.path"] = exe!;
                a["process.executable.name"] = System.IO.Path.GetFileName(exe!);
            }
        }
        catch (Exception)
        {
            // not permitted (sandboxes)
        }
        // The entry point path only: command-line arguments are not sent, they may contain secrets.
        var args = Environment.GetCommandLineArgs();
        if (args.Length > 0 && args[0].Length > 0) a["process.command"] = args[0];
        if (linux)
        {
            var arch = hfs.ReadTrim("/proc/sys/kernel/arch");
            if (arch.Length > 0) a["host.arch"] = NormalizeArch(arch);
            foreach (var p in new[] { "/etc/os-release", "/usr/lib/os-release" })
            {
                var s = hfs.Read(p);
                if (s == null) continue;
                var osr = ParseOsRelease(s);
                if (osr.TryGetValue("ID", out var id)) a["os.name"] = id;
                if (osr.TryGetValue("VERSION_ID", out var ver)) a["os.version"] = ver;
                if (osr.TryGetValue("PRETTY_NAME", out var pretty)) a["os.description"] = pretty;
                break;
            }
            var kr = hfs.ReadTrim("/proc/sys/kernel/osrelease");
            if (kr.Length > 0) a["openlog.os.kernel_release"] = kr;
            var cid = ContainerId(hfs);
            if (cid.Length > 0) a["container.id"] = cid;
        }
        else
        {
            a["os.version"] = Environment.OSVersion.Version.ToString();
            a["os.description"] = RuntimeInformation.OSDescription;
        }
        try
        {
            var user = Environment.UserName;
            if (!string.IsNullOrEmpty(user)) a["process.owner"] = user;
        }
        catch (Exception)
        {
            // no passwd entry (arbitrary container uid)
        }
        foreach (var kv in K8sAttributes(hfs, env)) a[kv.Key] = kv.Value;
        hostIdSource = "config";
        if (cfg.HostId.Length == 0)
        {
            var (id, src) = ResolveHostId(hfs, cfg.InfraRuntimeDir, cfg.InfraStateDir, cfg.StateDir, linux);
            hostIdSource = src;
            if (id.Length > 0) a["host.id"] = id;
        }
        return a;
    }

    /// <summary>
    /// Resolves host.id like the infra agent, so both report the same id on one machine:
    /// 0. the id a running infra agent published in &lt;infraRuntimeDir&gt;/host-id (under the host root, else the plain path);
    /// 1. /etc/machine-id → /var/lib/dbus/machine-id → /sys/class/dmi/id/product_uuid (valid, not all zeros; lower-cased);
    /// 2. the UUID the infra agent generated in its state dir;
    /// 3. non-Linux: the platform machine id;
    /// 4. a UUID generated and persisted by this agent (stateDir, default &lt;user cache dir&gt;/openlog/host-id).
    /// </summary>
    internal static (string Id, string Source) ResolveHostId(HostFS hfs, string infraRuntimeDir, string infraStateDir, string stateDir, bool linux)
    {
        if (infraRuntimeDir.Length > 0)
        {
            var file = infraRuntimeDir.TrimEnd('/') + "/host-id";
            var v = hfs.ReadTrim(file);
            if (!ValidHostId.IsMatch(v) && hfs.Root != "/") v = new HostFS("/").ReadTrim(file);
            if (ValidHostId.IsMatch(v)) return (v, "infra-agent");
        }
        foreach (var p in HostIdFiles)
        {
            var v = hfs.ReadTrim(p);
            if (ValidHostId.IsMatch(v) && v.Any(ch => ch != '0' && ch != '-')) return (v.ToLowerInvariant(), p);
        }
        if (infraStateDir.Length > 0)
        {
            var v = hfs.ReadTrim(infraStateDir.TrimEnd('/') + "/host-id");
            if (ValidHostId.IsMatch(v)) return (v, "infra-agent-state");
        }
        if (!linux)
        {
            var v = "";
            try
            {
                v = PlatformHostId();
            }
            catch (Exception)
            {
                // not available
            }
            if (v.Length > 0) return (v, "platform");
        }
        var dir = stateDir.Length > 0 ? stateDir : System.IO.Path.Combine(UserCacheDir(), "openlog");
        var path = System.IO.Path.Combine(dir, "host-id");
        try
        {
            var existing = File.ReadAllText(path).Trim();
            if (ValidHostId.IsMatch(existing)) return (existing, "generated");
        }
        catch (Exception)
        {
            // generate below
        }
        var gen = Guid.NewGuid().ToString();
        try
        {
            Directory.CreateDirectory(dir);
            var tmp = path + "." + Process.GetCurrentProcess().Id + ".tmp";
            File.WriteAllText(tmp, gen + "\n");
            if (File.Exists(path)) File.Delete(path);
            File.Move(tmp, path);
        }
        catch (Exception)
        {
            return ("", "");
        }
        return (gen, "generated");
    }

    /// <summary>
    /// The id of the container this process runs in: the last 64-hex segment of /proc/self/cgroup (cgroup v1, or v2
    /// without a cgroup namespace), otherwise the Docker/Podman container directory in /proc/self/mountinfo (cgroup v2
    /// with a private cgroup namespace, where /proc/self/cgroup is just "0::/").
    /// </summary>
    internal static string ContainerId(HostFS hfs)
    {
        var cgroup = hfs.Read("/proc/self/cgroup");
        if (cgroup != null)
        {
            foreach (var line in cgroup.Split('\n'))
            {
                var parts = line.Split(':');
                if (parts.Length < 3) continue;
                var segs = string.Join(":", parts.Skip(2)).Split('/');
                for (var i = segs.Length - 1; i >= 0; i--)
                {
                    var m = ContainerIdInCgroup.Match(segs[i]);
                    if (m.Success) return m.Groups[1].Value;
                }
            }
        }
        var mountinfo = hfs.Read("/proc/self/mountinfo");
        if (mountinfo != null)
        {
            foreach (var line in mountinfo.Split('\n'))
            {
                if (line.IndexOf("/sandboxes/", StringComparison.Ordinal) >= 0) continue; // containerd pod sandbox, not this container
                var m = ContainerIdInMountinfo.Match(line);
                if (m.Success) return m.Groups[1].Value;
            }
        }
        return "";
    }

    /// <summary>Maps kernel/.NET architecture names to OTel host.arch values (same as the infra and Go agents).</summary>
    internal static string NormalizeArch(string a) => a switch
    {
        "x86_64" or "amd64" or "x64" => "amd64",
        "aarch64" or "arm64" => "arm64",
        "i386" or "i686" or "386" or "ia32" or "x86" => "x86",
        "armv7l" or "armv6l" or "arm" => "arm32",
        "ppc64le" or "ppc64" => "ppc64",
        "s390x" => "s390x",
        _ => a,
    };

    private string OsType()
    {
        if (linux) return "linux";
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows)) return "windows";
        if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX)) return "darwin";
        return "freebsd";
    }

    internal static Dictionary<string, string> ParseOsRelease(string s)
    {
        var d = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (var raw in s.Split('\n'))
        {
            var line = raw.Trim();
            if (line.Length == 0 || line[0] == '#') continue;
            var i = line.IndexOf('=');
            if (i < 0) continue;
            var v = line.Substring(i + 1);
            if (v.Length >= 2 && ((v[0] == '"' && v[v.Length - 1] == '"') || (v[0] == '\'' && v[v.Length - 1] == '\'')))
            {
                v = Regex.Replace(v.Substring(1, v.Length - 2), "\\\\([\"'\\\\$`])", "$1");
            }
            d[line.Substring(0, i)] = v;
        }
        return d;
    }

    /// <summary>Kubernetes metadata from downward-API environment variables (inside a pod with fallbacks).</summary>
    internal static Dictionary<string, string> K8sAttributes(HostFS hfs, IDictionary<string, string?> env)
    {
        var d = new Dictionary<string, string>(StringComparer.Ordinal);
        string E(params string[] names)
        {
            foreach (var n in names)
            {
                if (env.TryGetValue(n, out var v) && v != null && v.Trim().Length > 0) return v.Trim();
            }
            return "";
        }
        void Set(string k, string v)
        {
            if (v.Length > 0) d[k] = v;
        }
        Set("k8s.pod.name", E("K8S_POD_NAME", "POD_NAME"));
        Set("k8s.pod.uid", E("K8S_POD_UID", "POD_UID"));
        Set("k8s.namespace.name", E("K8S_NAMESPACE_NAME", "K8S_NAMESPACE", "POD_NAMESPACE"));
        Set("k8s.node.name", E("K8S_NODE_NAME", "NODE_NAME"));
        Set("k8s.container.name", E("K8S_CONTAINER_NAME", "CONTAINER_NAME"));
        Set("k8s.deployment.name", E("K8S_DEPLOYMENT_NAME"));
        Set("k8s.cluster.name", E("K8S_CLUSTER_NAME"));
        if (env.ContainsKey("KUBERNETES_SERVICE_HOST"))
        {
            if (!d.ContainsKey("k8s.namespace.name")) Set("k8s.namespace.name", hfs.ReadTrim("/var/run/secrets/kubernetes.io/serviceaccount/namespace"));
            if (!d.ContainsKey("k8s.pod.name")) Set("k8s.pod.name", E("HOSTNAME"));
        }
        return d;
    }

    private string HostName(HostFS hfs)
    {
        if (linux)
        {
            var v = hfs.ReadTrim("/proc/sys/kernel/hostname");
            if (v.Length == 0) v = hfs.ReadTrim("/etc/hostname");
            if (v.Length > 0) return v;
        }
        return Environment.MachineName;
    }

    private static string UserCacheDir()
    {
        var home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
        if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX)) return System.IO.Path.Combine(home, "Library", "Caches");
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows)) return Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        var xdg = Environment.GetEnvironmentVariable("XDG_CACHE_HOME");
        return string.IsNullOrEmpty(xdg) ? System.IO.Path.Combine(home.Length > 0 ? home : "/tmp", ".cache") : xdg!;
    }

    private static string DefaultPlatformHostId()
    {
        if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
        {
            var m = Regex.Match(Run("ioreg", "-rd1 -c IOPlatformExpertDevice"), "\"IOPlatformUUID\"\\s*=\\s*\"([^\"]+)\"");
            return m.Success ? m.Groups[1].Value.ToLowerInvariant() : "";
        }
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
        {
            var m = Regex.Match(Run("reg", "query HKEY_LOCAL_MACHINE\\SOFTWARE\\Microsoft\\Cryptography /v MachineGuid"), "MachineGuid\\s+REG_SZ\\s+(\\S+)");
            return m.Success ? m.Groups[1].Value.ToLowerInvariant() : "";
        }
        return "";
    }

    private static string Run(string file, string args)
    {
        using var p = Process.Start(new ProcessStartInfo(file, args) { RedirectStandardOutput = true, RedirectStandardError = true, UseShellExecute = false, CreateNoWindow = true });
        if (p == null) return "";
        var output = p.StandardOutput.ReadToEndAsync();
        if (!p.WaitForExit(2000))
        {
            try
            {
                p.Kill();
            }
            catch (Exception)
            {
                // already exited
            }
            return "";
        }
        return output.Result;
    }
}
