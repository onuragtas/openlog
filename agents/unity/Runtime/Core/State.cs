using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.RegularExpressions;

namespace OpenLogUnity.Core
{
    /// <summary>A visit, not a person (mobile-agent.md §4): random, 30 minutes idle, 4 hours at most.</summary>
    public sealed class SessionState
    {
        public const long IdleMs = 30 * 60 * 1000;
        public const long MaxMs = 4 * 60 * 60 * 1000;

        private readonly Func<long> _now;
        private readonly Func<double> _roll;
        private long _startedAt;
        private long _lastSeenAt;

        public double SampleRate;
        public string Id { get; private set; }
        public string TraceId { get; private set; }
        public string ScreenSpanId { get; private set; }
        public bool Sampled { get; private set; }

        public SessionState(double sampleRate, Func<long> now, Func<double> roll)
        {
            SampleRate = sampleRate;
            _now = now;
            _roll = roll;
            Start(_now());
        }

        private void Start(long at)
        {
            Id = Ids.Id32();
            TraceId = Ids.Id32();
            ScreenSpanId = Ids.Id16();
            _startedAt = _lastSeenAt = at;
            // Once per session, not per event: half a session is not a cheaper session, it is an unreadable one.
            Sampled = SampleRate >= 1 || (SampleRate > 0 && _roll() < SampleRate);
        }

        /// <summary>Records activity. True when a new session began.</summary>
        public bool Touch()
        {
            var at = _now();
            if (at - _lastSeenAt >= IdleMs || at - _startedAt >= MaxMs)
            {
                Start(at);
                return true;
            }
            _lastSeenAt = at;
            return false;
        }

        public void NewScreen()
        {
            TraceId = Ids.Id32();
            ScreenSpanId = Ids.Id16();
        }
    }

    /// <summary>
    /// The persistent id behind DAU, MAU and retention (analytics.md §1). It reaches the disk only once the server says the
    /// key is in visitors mode, a withdrawal outlives the process, and without a directory there is no id at all: a new one
    /// per launch would count every launch as a new person.
    /// </summary>
    public sealed class Visitor
    {
        private const string OptOut = "optout";
        private const long MaxAgeMs = 395L * 24 * 60 * 60 * 1000;
        private static readonly Regex Shape = new Regex("^[0-9a-f]{32}$");

        private readonly string _file;
        private readonly Func<long> _now;
        private readonly object _lock = new object();
        private long _createdAt;
        private bool _persistent;
        private volatile string _id = "";

        public string Id => _id;

        public Visitor(bool enabled, string directory, Func<long> now)
        {
            _file = string.IsNullOrEmpty(directory) ? null : Path.Combine(directory, "openlog_visitor");
            _now = now;
            // An id on disk survives a default of false: a game that asks first starts every launch disabled and enables
            // after reading its own consent, and the person it asked must not become new each time.
            if (enabled && !Read(out _, out _)) Adopt();
        }

        /// <summary>The roll of a visitor, from FNV-1a. Every SDK computes exactly this (analytics.md §2).</summary>
        public static double Roll(string id)
        {
            uint h = 2166136261;
            foreach (var c in id)
            {
                h ^= c;
                h = unchecked(h * 16777619);
            }
            return h / 4294967296.0;
        }

        /// <summary>Returns whether the person withdrew.</summary>
        private bool Read(out string id, out long createdAt)
        {
            id = "";
            createdAt = 0;
            try
            {
                if (_file == null || !File.Exists(_file)) return false;
                var lines = File.ReadAllLines(_file);
                if (lines.Length > 0 && lines[0] == OptOut) return true;
                if (lines.Length >= 2 && long.TryParse(lines[1], out createdAt)) id = lines[0];
            }
            catch (Exception)
            {
                // An unreadable file is no visitor, never an error in somebody's game.
            }
            return false;
        }

        private void Write(string content)
        {
            if (_file == null) return;
            try
            {
                if (content == null)
                {
                    File.Delete(_file);
                    return;
                }
                FileAtomic.Write(_file, content);
            }
            catch (Exception)
            {
                // Without storage the visitor is not remembered, and nothing else changes.
            }
        }

        private void Adopt()
        {
            if (_file == null) return;
            Read(out var stored, out var at);
            var now = _now();
            var fresh = Shape.IsMatch(stored) && now - at < MaxAgeMs;
            _createdAt = fresh ? at : now;
            _id = fresh ? stored : Ids.Id32();
        }

        /// <summary>The server says this key recognises visitors.</summary>
        public void Confirm()
        {
            lock (_lock)
            {
                _persistent = true;
                if (_id.Length > 0) Write(_id + "\n" + _createdAt + "\n");
            }
        }

        /// <summary>The server says it does not: nothing stays on the device, except a person's own withdrawal.</summary>
        public void Discard()
        {
            lock (_lock)
            {
                _persistent = false;
                _id = "";
                if (!Read(out _, out _)) Write(null);
            }
        }

        /// <summary>Disabling deletes the id, so enabling again starts a new one.</summary>
        public void SetEnabled(bool enabled)
        {
            lock (_lock)
            {
                if (!enabled)
                {
                    _id = "";
                    Write(OptOut + "\n");
                    return;
                }
                if (_id.Length > 0) return;
                var wasOptOut = Read(out _, out _);
                Adopt();
                if (_persistent && _id.Length > 0) Write(_id + "\n" + _createdAt + "\n");
                else if (wasOptOut) Write(null);
            }
        }
    }

    public static class FileAtomic
    {
        /// <summary>Renamed into place, so a process dying mid-write cannot leave half a file behind.</summary>
        public static void Write(string path, string content)
        {
            var tmp = path + ".tmp";
            File.WriteAllText(tmp, content, new UTF8Encoding(false));
            if (File.Exists(path)) File.Delete(path);
            File.Move(tmp, path);
        }
    }

    /// <summary>Reports waiting for a launch that can send them (mobile-crash.md §2.1).</summary>
    public sealed class CrashStore
    {
        /// <summary>A crash loop must not fill the device, and the first crashes of a loop say what the rest would.</summary>
        public const int MaxFiles = 10;
        private const string Prefix = "openlog-crash-";

        private readonly string _dir;
        private readonly Func<long> _now;

        public CrashStore(string directory, Func<long> now)
        {
            _dir = directory;
            _now = now;
        }

        public void Write(string id, string body)
        {
            try
            {
                Directory.CreateDirectory(_dir);
                if (Pending().Count >= MaxFiles) return;
                FileAtomic.Write(Path.Combine(_dir, Prefix + _now().ToString("D15") + "-" + id + ".json"), body);
            }
            catch (Exception)
            {
                // Nothing can be done from here.
            }
        }

        public List<string> Pending()
        {
            var o = new List<string>();
            try
            {
                if (Directory.Exists(_dir)) o.AddRange(Directory.GetFiles(_dir, Prefix + "*.json"));
            }
            catch (Exception)
            {
                return o;
            }
            o.Sort(StringComparer.Ordinal);
            return o;
        }
    }

    /// <summary>The session a launch ended in, for a report that names none, which is all a native one can be.</summary>
    public sealed class LastSession
    {
        public string Id;
        public string Version;

        private static string PathIn(string dir) => Path.Combine(dir, "openlog_last_session");

        public static LastSession Read(string dir)
        {
            try
            {
                if (string.IsNullOrEmpty(dir) || !File.Exists(PathIn(dir))) return null;
                var l = File.ReadAllLines(PathIn(dir));
                return l.Length >= 2 ? new LastSession { Id = l[0], Version = l[1] } : null;
            }
            catch (Exception)
            {
                return null;
            }
        }

        public static void Write(string dir, string id, string version)
        {
            try
            {
                if (string.IsNullOrEmpty(dir)) return;
                Directory.CreateDirectory(dir);
                FileAtomic.Write(PathIn(dir), id + "\n" + version + "\n");
            }
            catch (Exception)
            {
                // A report then says its session is unknown.
            }
        }
    }

    /// <summary>
    /// Keeps one exception in an Update from becoming sixty events a second. Games do this, and it would use up a key's rate
    /// limit and a player's data on a single line of code. The first of a kind always goes through.
    /// </summary>
    public sealed class ExceptionFilter
    {
        public const long WindowMs = 10000;
        private const int MaxTracked = 64;
        private readonly Dictionary<string, long> _lastSent = new Dictionary<string, long>();

        public bool Allow(string message, long now)
        {
            lock (_lastSent)
            {
                if (_lastSent.TryGetValue(message, out var last) && now - last < WindowMs) return false;
                if (_lastSent.Count >= MaxTracked && !_lastSent.ContainsKey(message)) _lastSent.Clear();
                _lastSent[message] = now;
                return true;
            }
        }
    }
}
