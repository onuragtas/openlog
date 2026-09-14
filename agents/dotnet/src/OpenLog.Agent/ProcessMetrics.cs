using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Diagnostics.Metrics;

namespace OpenLog.Agent;

/// <summary>
/// Process metrics with OpenTelemetry semantic-convention names (the Go agent's policy), read on collection:
///   process.cpu.time {cpu.mode=user|system}  Counter        s
///   process.memory.usage                     UpDownCounter  By  (working set / RSS)
///   process.memory.virtual                   UpDownCounter  By
///   process.thread.count                     UpDownCounter  {thread}
/// Runtime metrics (GC, heap, JIT, thread pool, exceptions: dotnet.*) come from OpenTelemetry.Instrumentation.Runtime,
/// which on .NET 9+ enables the runtime's built-in System.Runtime meter.
/// </summary>
internal static class ProcessMetrics
{
    /// <summary>Meter (instrumentation scope) name.</summary>
    public const string MeterName = "OpenLog.Agent.Process";

    private static Meter? meter;
    private static readonly object Gate = new();

    public static void EnsureCreated()
    {
        lock (Gate)
        {
            if (meter != null) return;
            var m = new Meter(MeterName, AgentVersion.Version);
            m.CreateObservableCounter("process.cpu.time", CpuTime, "s", "Total CPU seconds broken down by different CPU modes.");
            m.CreateObservableUpDownCounter("process.memory.usage", () => Read(p => p.WorkingSet64), "By", "The amount of physical memory in use.");
            m.CreateObservableUpDownCounter("process.memory.virtual", () => Read(p => p.VirtualMemorySize64), "By", "The amount of committed virtual memory.");
            m.CreateObservableUpDownCounter("process.thread.count", () => Read(p => (long)p.Threads.Count), "{thread}", "Process threads count.");
            meter = m;
        }
    }

    private static long Read(Func<Process, long> f)
    {
        try
        {
            using var p = Process.GetCurrentProcess();
            return f(p);
        }
        catch (Exception)
        {
            return 0;
        }
    }

    private static IEnumerable<Measurement<double>> CpuTime()
    {
        double user, system;
        try
        {
            using var p = Process.GetCurrentProcess();
            user = p.UserProcessorTime.TotalSeconds;
            system = p.PrivilegedProcessorTime.TotalSeconds;
        }
        catch (Exception)
        {
            yield break;
        }
        yield return new Measurement<double>(user, new KeyValuePair<string, object?>("cpu.mode", "user"));
        yield return new Measurement<double>(system, new KeyValuePair<string, object?>("cpu.mode", "system"));
    }
}

/// <summary>Version of the agent (telemetry.distro.version).</summary>
public static class AgentVersion
{
    /// <summary>The product SemVer this assembly was built with (D-025).</summary>
    public static readonly string Version = ReadVersion();

    private static string ReadVersion()
    {
        var attr = (System.Reflection.AssemblyInformationalVersionAttribute?)Attribute.GetCustomAttribute(
            typeof(AgentVersion).Assembly, typeof(System.Reflection.AssemblyInformationalVersionAttribute));
        var v = attr?.InformationalVersion ?? "0.0.0";
        var plus = v.IndexOf('+');
        return plus >= 0 ? v.Substring(0, plus) : v;
    }
}
