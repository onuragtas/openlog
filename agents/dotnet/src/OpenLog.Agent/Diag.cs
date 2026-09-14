using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics.Tracing;
using System.Globalization;
using System.Linq;
using System.Text;

namespace OpenLog.Agent;

/// <summary>Level of the agent's own diagnostics.</summary>
public enum DiagnosticLevel
{
    Debug = 0,
    Info = 1,
    Warn = 2,
    Error = 3,
    Off = 100,
}

/// <summary>The agent's own diagnostics: logfmt-like lines on stderr, like the Go and Node.js agents.</summary>
internal sealed class Diag
{
    private readonly Action<string> write;

    public Diag(DiagnosticLevel level, Action<string>? write = null)
    {
        Level = level;
        this.write = write ?? (line => Console.Error.WriteLine(line));
    }

    public DiagnosticLevel Level { get; }

    public static DiagnosticLevel? ParseLevel(string v) => v.Trim().ToLowerInvariant() switch
    {
        "debug" or "verbose" or "all" => DiagnosticLevel.Debug,
        "info" => DiagnosticLevel.Info,
        "warn" or "warning" => DiagnosticLevel.Warn,
        "error" => DiagnosticLevel.Error,
        "off" or "none" => DiagnosticLevel.Off,
        _ => null,
    };

    public bool Enabled(DiagnosticLevel l) => Level != DiagnosticLevel.Off && l >= Level;

    public void Log(DiagnosticLevel l, string msg, params (string Key, object? Value)[] fields)
    {
        if (!Enabled(l)) return;
        var sb = new StringBuilder();
        sb.Append("time=").Append(DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ", CultureInfo.InvariantCulture));
        sb.Append(" level=").Append(l.ToString().ToUpperInvariant());
        sb.Append(" msg=").Append(Fmt(msg));
        sb.Append(" component=openlog-dotnet-agent");
        foreach (var (key, value) in fields)
        {
            if (value != null) sb.Append(' ').Append(key).Append('=').Append(Fmt(value));
        }
        try
        {
            write(sb.ToString());
        }
        catch (Exception)
        {
            // diagnostics never throw into the application
        }
    }

    public void Debug(string msg, params (string, object?)[] f) => Log(DiagnosticLevel.Debug, msg, f);
    public void Info(string msg, params (string, object?)[] f) => Log(DiagnosticLevel.Info, msg, f);
    public void Warn(string msg, params (string, object?)[] f) => Log(DiagnosticLevel.Warn, msg, f);
    public void Error(string msg, params (string, object?)[] f) => Log(DiagnosticLevel.Error, msg, f);

    private static string Fmt(object v)
    {
        var s = v switch
        {
            Exception e => e.Message,
            IFormattable f => f.ToString(null, CultureInfo.InvariantCulture),
            _ => v.ToString() ?? "",
        };
        if (s.Length == 0 || s.Any(ch => char.IsWhiteSpace(ch) || ch == '"' || ch == '='))
        {
            return "\"" + s.Replace("\\", "\\\\").Replace("\"", "\\\"").Replace("\n", "\\n").Replace("\r", "\\r") + "\"";
        }
        return s;
    }
}

/// <summary>
/// Forwards OpenTelemetry .NET self-diagnostics (EventSources named OpenTelemetry-*, e.g. export failures) to the
/// agent's diagnostics. Export problems are never fatal: warnings and errors are logged at most once per minute per
/// message, with the number of suppressed repeats (same policy as the Go and Node.js agents).
/// </summary>
internal sealed class OpenTelemetryEventForwarder : EventListener
{
    private static readonly TimeSpan Interval = TimeSpan.FromMinutes(1);
    private readonly ConcurrentDictionary<string, Seen> seen = new();
    private readonly List<EventSource> pending = new();
    private Diag? diag;

    private sealed class Seen
    {
        public DateTime Last;
        public int Suppressed;
    }

    public OpenTelemetryEventForwarder(Diag diag)
    {
        // EventListener's constructor calls OnEventSourceCreated for existing sources before this body runs.
        lock (pending)
        {
            this.diag = diag;
            foreach (var s in pending) Enable(s);
            pending.Clear();
        }
    }

    private EventLevel ListenLevel => diag!.Level switch
    {
        DiagnosticLevel.Debug => EventLevel.Verbose,
        DiagnosticLevel.Info => EventLevel.Informational,
        DiagnosticLevel.Warn => EventLevel.Warning,
        _ => EventLevel.Error,
    };

    private void Enable(EventSource source)
    {
        if (diag!.Level == DiagnosticLevel.Off) return;
        EnableEvents(source, ListenLevel, EventKeywords.All);
    }

    protected override void OnEventSourceCreated(EventSource eventSource)
    {
        if (!eventSource.Name.StartsWith("OpenTelemetry-", StringComparison.Ordinal)) return;
        if (pending == null)
        {
            return; // base constructor, before field initializers of this class ran (cannot happen with C# initializers, kept defensive)
        }
        lock (pending)
        {
            if (diag == null) pending.Add(eventSource);
            else Enable(eventSource);
        }
    }

    protected override void OnEventWritten(EventWrittenEventArgs e)
    {
        var d = diag;
        if (d == null) return;
        var level = e.Level switch
        {
            EventLevel.Critical or EventLevel.Error => DiagnosticLevel.Error,
            EventLevel.Warning => DiagnosticLevel.Warn,
            EventLevel.Informational => DiagnosticLevel.Info,
            _ => DiagnosticLevel.Debug,
        };
        if (!d.Enabled(level)) return;
        string message;
        try
        {
            message = e.Message != null && e.Payload != null
                ? string.Format(CultureInfo.InvariantCulture, e.Message, e.Payload.ToArray())
                : e.EventName ?? e.EventId.ToString(CultureInfo.InvariantCulture);
        }
        catch (FormatException)
        {
            message = e.Message ?? e.EventName ?? "";
        }
        var key = e.EventSource.Name + "/" + e.EventId.ToString(CultureInfo.InvariantCulture);
        var now = DateTime.UtcNow;
        int suppressed;
        var s = seen.GetOrAdd(key, _ => new Seen { Last = DateTime.MinValue });
        lock (s)
        {
            if (level >= DiagnosticLevel.Warn && now - s.Last < Interval)
            {
                s.Suppressed++;
                return;
            }
            suppressed = s.Suppressed;
            s.Suppressed = 0;
            s.Last = now;
        }
        d.Log(level, message, ("source", e.EventSource.Name), ("suppressed", suppressed > 0 ? suppressed : null));
    }
}
