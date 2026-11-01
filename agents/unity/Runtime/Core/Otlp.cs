using System;
using System.Collections.Generic;
using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace OpenLogUnity.Core
{
    // The wire format (docs/contracts/mobile-agent.md §3, §5). Written by hand, and with no reflection anywhere: IL2CPP
    // strips what it cannot see, and a telemetry library that needs a link.xml is one more thing to get wrong in a build.

    public static class Limits
    {
        public const int MaxBatchSize = 32;
        public const int MaxQueue = 1000;
        public const int MaxAttributesPerSpan = 64;
        public const int MaxAttrValueBytes = 2048;
        public const int MaxNameBytes = 256;
        public const int MaxMessageBytes = 1024;
        public const int MaxStackBytes = 16384;
        public const int MaxUserIdBytes = 128;
        public const int MaxCustomNameBytes = 128;
        public const int MaxCustomParams = 16;
        public const int MaxCustomParamKeyBytes = 64;

        /// <summary>Cuts to at most n UTF-8 bytes without splitting a character.</summary>
        public static string Truncate(string value, int n)
        {
            if (string.IsNullOrEmpty(value) || Encoding.UTF8.GetByteCount(value) <= n) return value ?? "";
            var bytes = Encoding.UTF8.GetBytes(value);
            while (n > 0 && (bytes[n] & 0xc0) == 0x80) n--;
            return Encoding.UTF8.GetString(bytes, 0, n);
        }
    }

    public static class Ids
    {
        private static readonly RandomNumberGenerator Rng = RandomNumberGenerator.Create();
        private static readonly Random Unit = new Random();

        private static string Hex(int bytes)
        {
            var b = new byte[bytes];
            lock (Rng) Rng.GetBytes(b);
            var s = new StringBuilder(bytes * 2);
            foreach (var x in b) s.Append(x.ToString("x2", CultureInfo.InvariantCulture));
            return s.ToString();
        }

        public static string Id32() => Hex(16);
        public static string Id16() => Hex(8);

        public static double NextUnit()
        {
            lock (Unit) return Unit.NextDouble();
        }
    }

    public static class Json
    {
        public static string Encode(object value)
        {
            var s = new StringBuilder();
            Write(s, value);
            return s.ToString();
        }

        private static void Write(StringBuilder s, object v)
        {
            switch (v)
            {
                case null: s.Append("null"); break;
                case string t: Quote(s, t); break;
                case int i: s.Append(i.ToString(CultureInfo.InvariantCulture)); break;
                case Dictionary<string, object> d:
                    s.Append('{');
                    var first = true;
                    foreach (var e in d)
                    {
                        if (!first) s.Append(',');
                        first = false;
                        Quote(s, e.Key);
                        s.Append(':');
                        Write(s, e.Value);
                    }
                    s.Append('}');
                    break;
                case List<object> l:
                    s.Append('[');
                    for (var n = 0; n < l.Count; n++)
                    {
                        if (n > 0) s.Append(',');
                        Write(s, l[n]);
                    }
                    s.Append(']');
                    break;
                default: Quote(s, Convert.ToString(v, CultureInfo.InvariantCulture)); break;
            }
        }

        private static void Quote(StringBuilder s, string t)
        {
            s.Append('"');
            foreach (var c in t)
            {
                switch (c)
                {
                    case '"': s.Append("\\\""); break;
                    case '\\': s.Append("\\\\"); break;
                    case '\n': s.Append("\\n"); break;
                    case '\r': s.Append("\\r"); break;
                    case '\t': s.Append("\\t"); break;
                    default:
                        if (c < 0x20) s.Append("\\u").Append(((int)c).ToString("x4", CultureInfo.InvariantCulture));
                        else s.Append(c);
                        break;
                }
            }
            s.Append('"');
        }
    }

    public static class Otlp
    {
        public const string EventScreen = "page_view";
        public const string EventError = "error";
        public const string EventCustom = "custom";

        /// <summary>Nanoseconds as text, because multiplying by 1e6 would round past 2^53.</summary>
        private static string Nano(long ms) => (ms < 0 ? 0 : ms).ToString(CultureInfo.InvariantCulture) + "000000";

        public static List<object> Attributes(List<KeyValuePair<string, string>> attrs)
        {
            var o = new List<object>();
            foreach (var a in attrs)
            {
                if (string.IsNullOrEmpty(a.Value)) continue; // an empty attribute is not a value
                if (o.Count >= Limits.MaxAttributesPerSpan) break;
                o.Add(new Dictionary<string, object>
                {
                    ["key"] = a.Key,
                    ["value"] = new Dictionary<string, object> { ["stringValue"] = Limits.Truncate(a.Value, a.Key == "exception.stacktrace" ? Limits.MaxStackBytes : Limits.MaxAttrValueBytes) },
                });
            }
            return o;
        }

        public static KeyValuePair<string, string> A(string key, string value) => new KeyValuePair<string, string>(key, value);

        public static Dictionary<string, object> Span(string name, string evt, string traceId, string spanId, string parentSpanId,
            long startMs, long durationMs, List<KeyValuePair<string, string>> attributes, bool isError = false,
            string exceptionType = null, string exceptionMessage = null, string exceptionStack = null)
        {
            var all = new List<KeyValuePair<string, string>> { A("openlog.rum.event", evt) };
            all.AddRange(attributes);
            var span = new Dictionary<string, object>
            {
                ["traceId"] = traceId,
                ["spanId"] = spanId,
                ["name"] = Limits.Truncate(name, Limits.MaxNameBytes),
                ["kind"] = 1,
                ["startTimeUnixNano"] = Nano(startMs),
                ["endTimeUnixNano"] = Nano(startMs + Math.Max(0, durationMs)),
                ["attributes"] = Attributes(all),
            };
            if (!string.IsNullOrEmpty(parentSpanId)) span["parentSpanId"] = parentSpanId;
            if (exceptionType != null)
            {
                span["events"] = new List<object>
                {
                    new Dictionary<string, object>
                    {
                        ["timeUnixNano"] = Nano(startMs),
                        ["name"] = "exception",
                        ["attributes"] = Attributes(new List<KeyValuePair<string, string>>
                        {
                            A("exception.type", exceptionType),
                            A("exception.message", Limits.Truncate(exceptionMessage, Limits.MaxMessageBytes)),
                            A("exception.stacktrace", Limits.Truncate(exceptionStack, Limits.MaxStackBytes)),
                        }),
                    },
                };
            }
            if (isError) span["status"] = new Dictionary<string, object> { ["code"] = 2 };
            return span;
        }

        public static string Payload(List<object> spans, List<KeyValuePair<string, string>> resource) => Json.Encode(
            new Dictionary<string, object>
            {
                ["resourceSpans"] = new List<object>
                {
                    new Dictionary<string, object>
                    {
                        ["resource"] = new Dictionary<string, object> { ["attributes"] = Attributes(resource) },
                        ["scopeSpans"] = new List<object> { new Dictionary<string, object> { ["spans"] = spans } },
                    },
                },
            });
    }
}
