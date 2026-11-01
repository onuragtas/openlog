using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text.RegularExpressions;
using static OpenLogUnity.Core.Otlp;

namespace OpenLogUnity.Core
{
    /// <summary>
    /// Sends one request. UnityWebRequest belongs to the main thread and finishes later, hence the callback, and tests never
    /// open a socket. The status is -1 when the request never completed.
    /// </summary>
    public interface ISender
    {
        void Post(string url, Dictionary<string, string> headers, string body, Action<int> done);
        void Get(string url, Dictionary<string, string> headers, Action<int, string> done);
    }

    public sealed class ClientConfig
    {
        public string Key;
        public string Endpoint;
        public string AppId;
        public string Version = "";
        public string DeviceModel = "";
        public string DeviceType = "mobile";
        public string OsName = "";
        public string OsVersion = "";
        public double SampleRate = 1;
        public long FlushIntervalMs = 5000;
        public bool AnalyticsCollection = true;
        public bool CaptureCrashes;
        /// <summary>A directory private to the game. Without it nothing is remembered between launches.</summary>
        public string Directory;
    }

    /// <summary>
    /// The SDK with nothing of Unity in it: everything here runs under `dotnet test` with no editor licence. Every method but
    /// Tick may be called from any thread, because Unity reports exceptions from whichever thread threw them.
    /// </summary>
    public sealed class Client
    {
        private static readonly Regex SampleRe = new Regex("\"sample_rate\"\\s*:\\s*([0-9.eE+-]+)");
        private static readonly Regex ModeRe = new Regex("\"analytics_mode\"\\s*:\\s*\"([^\"]*)\"");
        private static readonly Regex ParamRe = new Regex("^[a-z0-9_.-]+$");

        private readonly ClientConfig _cfg;
        private readonly ISender _sender;
        private readonly Func<long> _now;
        private readonly SessionState _session;
        private readonly Visitor _visitor;
        private readonly CrashStore _crashes;
        private readonly LastSession _previous;
        private readonly ExceptionFilter _filter = new ExceptionFilter();
        private readonly object _lock = new object();
        private readonly List<object> _queue = new List<object>();
        private readonly string _rumUrl;
        private readonly string _configUrl;

        private string _userId = "";
        private string _route = "";
        private string _rememberedSession = "";
        private long _nextFlush;
        private bool _inFlight;
        private bool _refused;
        private bool _flushRequested;

        public string SessionId => _session.Sampled ? _session.Id : "";
        public string VisitorId => _visitor.Id;
        public bool Refused => _refused;
        public LastSession Previous => _previous;

        public Client(ClientConfig cfg, ISender sender, Func<long> now)
        {
            if (cfg.Key == null || !cfg.Key.StartsWith("olb_", StringComparison.Ordinal))
                throw new ArgumentException("openlog: Key must be a mobile key (olb_...)");
            if (cfg.Endpoint == null || !(cfg.Endpoint.StartsWith("http://", StringComparison.Ordinal) || cfg.Endpoint.StartsWith("https://", StringComparison.Ordinal)))
                throw new ArgumentException("openlog: Endpoint must be the http(s) URL of openlog ingest");
            if (string.IsNullOrEmpty(cfg.AppId))
                throw new ArgumentException("openlog: AppId is required: a mobile key is scoped by the applications it ships in");
            _cfg = cfg;
            _sender = sender;
            _now = now;
            var endpoint = cfg.Endpoint.TrimEnd('/');
            _rumUrl = endpoint + "/v1/rum";
            _configUrl = endpoint + "/v1/rum/config";
            _visitor = new Visitor(cfg.AnalyticsCollection, cfg.Directory, now);
            _session = new SessionState(cfg.SampleRate, now, () => _visitor.Id.Length > 0 ? Visitor.Roll(_visitor.Id) : Ids.NextUnit());
            _previous = LastSession.Read(cfg.Directory);
            if (cfg.CaptureCrashes && !string.IsNullOrEmpty(cfg.Directory))
                _crashes = new CrashStore(Path.Combine(cfg.Directory, "openlog-crashes"), now);
            Remember();
        }

        /// <summary>Reads the operator's settings and delivers what an earlier launch left. Call once, on the main thread.</summary>
        public void Begin()
        {
            _sender.Get(_configUrl, Headers(), (status, body) =>
            {
                if (status != 200 || body == null) return; // the compiled-in settings stand, and failure is silent by design
                var rate = SampleRe.Match(body);
                if (rate.Success && double.TryParse(rate.Groups[1].Value, NumberStyles.Float, CultureInfo.InvariantCulture, out var r))
                    _session.SampleRate = r;
                // Only this answer lets the id reach the disk. A server without the field is one without the feature.
                var mode = ModeRe.Match(body);
                if (mode.Success && mode.Groups[1].Value == "visitors") _visitor.Confirm();
                else _visitor.Discard();
            });
            DeliverCrashes();
        }

        private Dictionary<string, string> Headers() => new Dictionary<string, string>
        {
            ["content-type"] = "application/json",
            ["openlog-browser-key"] = _cfg.Key,
            // A mobile key refuses a request that names no application, on both endpoints.
            ["openlog-app-id"] = _cfg.AppId,
        };

        private List<KeyValuePair<string, string>> Resource(string version) => new List<KeyValuePair<string, string>>
        {
            A("service.version", version ?? _cfg.Version),
            A("device.model.identifier", _cfg.DeviceModel),
            A("os.name", _cfg.OsName),
            A("os.version", _cfg.OsVersion),
            // Without this a version that cannot report a crash would read as 100% crash-free (mobile-crash.md §5).
            A("openlog.rum.crash.capture", _crashes != null ? "1" : ""),
        };

        /// <summary>Kept on every new session, because a process killed by the system gets no chance to say anything.</summary>
        private void Remember()
        {
            if (_session.Id == _rememberedSession) return;
            _rememberedSession = _session.Id;
            LastSession.Write(_cfg.Directory, _session.Id, _cfg.Version);
        }

        private List<KeyValuePair<string, string>> Common()
        {
            var o = new List<KeyValuePair<string, string>>
            {
                A("session.id", _session.Id),
                A("openlog.rum.page_view.id", _session.ScreenSpanId),
                A("device.type", _cfg.DeviceType),
                A("user.id", _userId),
                A("openlog.rum.visitor.id", _visitor.Id),
            };
            return o;
        }

        private void Emit(string evt, string name, long durationMs, string spanId, List<KeyValuePair<string, string>> attrs, bool isError = false,
            string type = null, string message = null, string stack = null)
        {
            lock (_lock)
            {
                if (!_session.Sampled || _refused) return; // a sampled-out session sends nothing at all
                _session.Touch();
                Remember();
                var all = Common();
                all.AddRange(attrs);
                var span = Span(name, evt, _session.TraceId, spanId ?? Ids.Id16(), spanId == null ? _session.ScreenSpanId : null,
                    _now(), durationMs, all, isError, type, message, stack);
                if (_queue.Count >= Limits.MaxQueue) _queue.RemoveAt(0); // the newest events are the ones still worth having
                _queue.Add(span);
                if (_queue.Count == 1) _nextFlush = _now() + _cfg.FlushIntervalMs;
            }
        }

        /// <summary>A scene or screen the player opened. isColdStart marks the first of a launch.</summary>
        public void RecordScreen(string name, bool isColdStart = false, long durationMs = 0)
        {
            var route = Limits.Truncate((name ?? "").Trim(), Limits.MaxNameBytes);
            if (route.Length == 0) return;
            lock (_lock)
            {
                _route = route;
                if (!_session.Touch()) _session.NewScreen();
                Emit(EventScreen, "screen " + route, durationMs, _session.ScreenSpanId, new List<KeyValuePair<string, string>>
                {
                    A("openlog.rum.route", route),
                    A("openlog.rum.page_view.kind", isColdStart ? "load" : "route_change"),
                });
            }
        }

        public void RecordEvent(string name, Dictionary<string, object> parameters = null)
        {
            var n = Limits.Truncate((name ?? "").Trim(), Limits.MaxCustomNameBytes);
            if (n.Length == 0) return; // an unnamed event is an unqueryable row, and the server refuses it anyway
            var attrs = new List<KeyValuePair<string, string>> { A("openlog.rum.custom.name", n) };
            attrs.AddRange(Params(parameters));
            Emit(EventCustom, n, 0, null, attrs);
        }

        public void RecordTiming(string name, long milliseconds, Dictionary<string, object> parameters = null)
        {
            var n = Limits.Truncate((name ?? "").Trim(), Limits.MaxCustomNameBytes);
            if (n.Length == 0) return;
            var attrs = new List<KeyValuePair<string, string>>
            {
                A("openlog.rum.custom.name", n),
                A("openlog.rum.custom.value", milliseconds.ToString(CultureInfo.InvariantCulture)),
                A("openlog.rum.custom.unit", "ms"),
            };
            attrs.AddRange(Params(parameters));
            Emit(EventCustom, n, milliseconds, null, attrs);
        }

        /// <summary>Sorted before the cap, so which sixteen survive depends on the payload and not on map order.</summary>
        private static List<KeyValuePair<string, string>> Params(Dictionary<string, object> parameters)
        {
            var o = new List<KeyValuePair<string, string>>();
            if (parameters == null) return o;
            var keys = new List<string>(parameters.Keys);
            keys.Sort(StringComparer.Ordinal);
            foreach (var k in keys)
            {
                if (o.Count >= Limits.MaxCustomParams) break;
                var key = k.Trim();
                if (key.Length == 0 || key.Length > Limits.MaxCustomParamKeyBytes || !ParamRe.IsMatch(key)) continue;
                o.Add(A("openlog.rum.custom.param." + key, Convert.ToString(parameters[k], CultureInfo.InvariantCulture)));
            }
            return o;
        }

        public void Identify(string userId)
        {
            lock (_lock) _userId = Limits.Truncate((userId ?? "").Trim(), Limits.MaxUserIdBytes);
        }

        public void SetAnalyticsCollection(bool enabled) => _visitor.SetEnabled(enabled);

        /// <summary>An error the game caught itself, or one Unity logged. It does not count as a crash.</summary>
        public void RecordError(string type, string message, string stack)
        {
            Emit(EventError, "error " + type, 0, null, new List<KeyValuePair<string, string>> { A("openlog.rum.error.source", "error") }, true, type, message, stack);
        }

        /// <summary>What Application.logMessageReceivedThreaded reports as an exception. "Type: message", as Unity formats it.</summary>
        public void RecordLoggedException(string condition, string stack)
        {
            if (!_filter.Allow(condition ?? "", _now())) return;
            ParseCondition(condition, out var type, out var message);
            RecordError(type, message, stack);
        }

        public static void ParseCondition(string condition, out string type, out string message)
        {
            condition = condition ?? "";
            var at = condition.IndexOf(": ", StringComparison.Ordinal);
            type = at > 0 ? condition.Substring(0, at) : "Exception";
            message = at > 0 ? condition.Substring(at + 2) : condition;
        }

        /// <summary>An error that ended the game for the player, counted as a crash. The game decides what that means.</summary>
        public void RecordFatal(string type, string message, string stack)
        {
            Emit(EventError, "error " + type, 0, null, new List<KeyValuePair<string, string>>
            {
                A("openlog.rum.route", _route),
                A("openlog.rum.error.source", "error"),
                A("openlog.rum.crash.kind", "uncaught_exception"),
                A("openlog.rum.crash.id", Ids.Id32()),
            }, true, type, message, stack);
            lock (_lock) _flushRequested = true;
        }

        /// <summary>
        /// A crash the operating system recorded about an earlier launch. It carries no session, so it takes the one that launch
        /// ended in when the build matches, and says so otherwise. Written first and sent after, like every report.
        /// </summary>
        public void RecordEarlierCrash(string kind, string type, string message, string stack, string version, long atMs)
        {
            if (_crashes == null || !_session.Sampled) return; // a sampled-out session sends nothing, crash included
            var known = _previous != null && _previous.Version == version ? _previous.Id : null;
            var id = Ids.Id32();
            var attrs = new List<KeyValuePair<string, string>>
            {
                A("session.id", known ?? Ids.Id32()),
                A("device.type", _cfg.DeviceType),
                A("openlog.rum.error.source", "error"),
                A("openlog.rum.crash.kind", kind),
                A("openlog.rum.crash.id", id),
                A("openlog.rum.crash.session_unknown", known == null ? "true" : ""),
                A("openlog.rum.visitor.id", _visitor.Id),
            };
            var span = Span("error " + type, EventError, Ids.Id32(), Ids.Id16(), null, atMs, 0, attrs, true, type, message, stack);
            _crashes.Write(id, Payload(new List<object> { span }, Resource(version)));
        }

        /// <summary>Sends reports oldest first. One stays for a later launch when the server said to wait or never answered.</summary>
        public void DeliverCrashes()
        {
            if (_crashes == null) return;
            var files = _crashes.Pending();
            void Next(int i)
            {
                if (i >= files.Count) return;
                string body;
                try
                {
                    body = File.ReadAllText(files[i]);
                }
                catch (Exception)
                {
                    return;
                }
                _sender.Post(_rumUrl, Headers(), body, status =>
                {
                    if (!((status >= 200 && status < 300) || (status >= 400 && status < 500 && status != 429))) return;
                    try
                    {
                        File.Delete(files[i]);
                    }
                    catch (Exception)
                    {
                        return;
                    }
                    Next(i + 1);
                });
            }
            Next(0);
        }

        /// <summary>The next launch would otherwise be the first chance: the last batch of a session is the most likely to be lost.</summary>
        public void Flush()
        {
            lock (_lock) _flushRequested = true;
            Tick();
        }

        /// <summary>Called every frame on the main thread, which is the only place a request may start.</summary>
        public void Tick()
        {
            List<object> batch;
            lock (_lock)
            {
                var due = _flushRequested || _queue.Count >= Limits.MaxBatchSize || (_queue.Count > 0 && _now() >= _nextFlush);
                if (!due || _inFlight || _refused || _queue.Count == 0) return;
                _flushRequested = false;
                batch = new List<object>(_queue);
                _queue.Clear();
                _inFlight = true;
            }
            _sender.Post(_rumUrl, Headers(), Payload(batch, Resource(null)), status =>
            {
                lock (_lock)
                {
                    _inFlight = false;
                    if (status == 401 || status == 403)
                    {
                        // Nothing about the next attempt would differ, so stop spending the player's battery on it.
                        _refused = true;
                        _queue.Clear();
                        return;
                    }
                    // 429, 503 and no answer at all are temporary. Anything else is dropped rather than retried forever.
                    if (status != 429 && status != 503 && status != -1) return;
                    _queue.InsertRange(0, batch.GetRange(0, Math.Min(batch.Count, Limits.MaxQueue - _queue.Count)));
                    _nextFlush = _now() + _cfg.FlushIntervalMs;
                }
            });
        }
    }
}
