using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using OpenLogUnity.Core;
using UnityEngine;

namespace OpenLogUnity
{
    // What crashed an earlier launch, as the operating system recorded it (mobile-crash.md §2). Nothing here runs inside a
    // crashing process, which is the point: a signal handler in a game may only call async-signal-safe functions, Unity and
    // other SDKs install their own, and getting that wrong hides the crash.

    internal static class NativeCrashes
    {
        /// <summary>Records what is known so far. Called once at start, before the reports are delivered.</summary>
        public static void Collect(Client client, string dir)
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            AndroidExits.Collect(client, dir);
#elif UNITY_IOS && !UNITY_EDITOR
            OpenLogMetricKitStart(Path.Combine(dir, "metrickit"));
#endif
            CollectLater(client, dir);
        }

        /// <summary>Picks up what MetricKit has delivered. True when there is something new to send.</summary>
        public static bool CollectLater(Client client, string dir)
        {
            var found = false;
            try
            {
                var inbox = Path.Combine(dir, "metrickit");
                if (!Directory.Exists(inbox)) return false;
                foreach (var f in Directory.GetFiles(inbox, "*.json"))
                {
                    var r = JsonUtility.FromJson<MetricKitCrash>(File.ReadAllText(f));
                    client.RecordEarlierCrash("signal", SignalName(r.signal), string.IsNullOrEmpty(r.reason) ? SignalName(r.signal) : r.reason,
                        NativeStack.FromMetricKit(r.stack), r.version, r.at_ms);
                    File.Delete(f);
                    found = true;
                }
            }
            catch (Exception)
            {
                // A report that cannot be read now may be readable on the next look.
            }
            return found;
        }

        public static string SignalName(int signal)
        {
            switch (signal)
            {
                case 11: return "SIGSEGV";
                case 6: return "SIGABRT";
                case 10: return "SIGBUS";
                case 4: return "SIGILL";
                case 5: return "SIGTRAP";
                case 8: return "SIGFPE";
                case 9: return "SIGKILL";
                case 0: return "Crash";
                default: return "Signal " + signal;
            }
        }

        /// <summary>JsonUtility rather than a parser: it is what Unity keeps working under IL2CPP.</summary>
        [Serializable]
        private sealed class MetricKitCrash
        {
            public int signal;
            public string reason;
            public string stack;
            public string version;
            public long at_ms;
        }

#if UNITY_IOS && !UNITY_EDITOR
        [DllImport("__Internal")]
        private static extern void OpenLogMetricKitStart(string directory);
#endif
    }

#if UNITY_ANDROID && !UNITY_EDITOR
    /// <summary>ApplicationExitInfo, through JNI, so the game needs no Android library. API 30 and up: below that there is nothing to read.</summary>
    internal static class AndroidExits
    {
        private const int ReasonLowMemory = 3;
        private const int ReasonCrash = 4;
        private const int ReasonCrashNative = 5;
        private const int ReasonAnr = 6;
        private const int ImportanceForeground = 100;
        private const int MaxExits = 8;
        private const int MaxTraceChars = 256 * 1024; // the main thread is well inside this, past the part a report leaves out
        private const int MaxTombstoneBytes = 4 << 20; // past the backtrace is memory dumps, which a report does not use

        public static void Collect(Client client, string dir)
        {
            try
            {
                using (var build = new AndroidJavaClass("android.os.Build$VERSION"))
                {
                    if (build.GetStatic<int>("SDK_INT") < 30) return;
                }
                // The system keeps exits from before this SDK was installed, and those are not ours to report.
                var mark = Path.Combine(dir, "openlog_last_exit");
                var hadMark = long.TryParse(File.Exists(mark) ? File.ReadAllText(mark).Trim() : "", out var since);
                var newest = since;

                // Not disposed: on current Unity this object belongs to the runtime, and disposing it throws.
                var activity = CurrentActivity();
                using (var manager = activity.Call<AndroidJavaObject>("getSystemService", "activity"))
                using (var list = manager.Call<AndroidJavaObject>("getHistoricalProcessExitReasons", Application.identifier, 0, MaxExits))
                {
                    var n = list.Call<int>("size");
                    for (var i = 0; i < n; i++)
                    {
                        using (var info = list.Call<AndroidJavaObject>("get", i))
                        {
                            var at = info.Call<long>("getTimestamp");
                            if (at > newest) newest = at;
                            if (hadMark && at > since) Report(client, info, at);
                        }
                    }
                }
                FileAtomic.Write(mark, (newest > 0 ? newest : DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()).ToString());
            }
            catch (Exception e)
            {
                Debug.LogWarning("openlog: cannot read exit reasons: " + e.Message);
            }
        }

        private static AndroidJavaObject CurrentActivity()
        {
#if UNITY_2023_1_OR_NEWER
            return UnityEngine.Android.AndroidApplication.currentActivity;
#else
            using (var player = new AndroidJavaClass("com.unity3d.player.UnityPlayer")) return player.GetStatic<AndroidJavaObject>("currentActivity");
#endif
        }

        private static void Report(Client client, AndroidJavaObject info, long at)
        {
            var reason = info.Call<int>("getReason");
            var description = info.Call<string>("getDescription") ?? "";
            var version = client.Previous?.Version ?? Application.version;
            switch (reason)
            {
                case ReasonCrashNative:
                    // Below API 31 there is no tombstone, and what is known is that it happened.
                    var tombstone = Tombstone.Decode(TraceBytes(info));
                    if (tombstone != null)
                        client.RecordEarlierCrash("signal", NativeCrashes.SignalName(tombstone.SignalNumber), tombstone.Message(), tombstone.Stack, version, at);
                    else
                        client.RecordEarlierCrash("signal", "NativeCrash", string.IsNullOrEmpty(description) ? "Native crash" : description, "", version, at);
                    break;
                case ReasonCrash:
                    client.RecordEarlierCrash("uncaught_exception", "JavaCrash", string.IsNullOrEmpty(description) ? "Java exception" : description, "", version, at);
                    break;
                case ReasonAnr:
                    client.RecordEarlierCrash("anr", "ANR", string.IsNullOrEmpty(description) ? "Application not responding" : description, Trace(info), version, at);
                    break;
                case ReasonLowMemory:
                    // The system also reclaims games sitting in the background. That is Android working, not a crash.
                    if (info.Call<int>("getImportance") == ImportanceForeground)
                        client.RecordEarlierCrash("out_of_memory", "OutOfMemory", string.IsNullOrEmpty(description) ? "Killed for low memory" : description, "", version, at);
                    break;
            }
        }

        /// <summary>
        /// A tombstone is binary, and a Java byte[] passed through JNI to be filled does not come back: Unity passes a copy. So Java
        /// fills a buffer of its own, and its array is then asked for.
        /// </summary>
        private static byte[] TraceBytes(AndroidJavaObject info)
        {
            try
            {
                using (var stream = info.Call<AndroidJavaObject>("getTraceInputStream"))
                {
                    if (stream == null) return new byte[0];
                    using (var channels = new AndroidJavaClass("java.nio.channels.Channels"))
                    using (var channel = channels.CallStatic<AndroidJavaObject>("newChannel", stream))
                    using (var buffers = new AndroidJavaClass("java.nio.ByteBuffer"))
                    using (var buffer = buffers.CallStatic<AndroidJavaObject>("allocate", MaxTombstoneBytes))
                    {
                        var total = 0;
                        while (total < MaxTombstoneBytes)
                        {
                            var n = channel.Call<int>("read", buffer);
                            if (n < 0) break;
                            total += n;
                        }
                        var bytes = new byte[total];
                        Buffer.BlockCopy(buffer.Call<sbyte[]>("array"), 0, bytes, 0, total);
                        return bytes;
                    }
                }
            }
            catch (Exception)
            {
                return new byte[0];
            }
        }

        /// <summary>An ANR's trace is text, and the stacks of the threads are the whole value of the report.</summary>
        private static string Trace(AndroidJavaObject info)
        {
            try
            {
                using (var stream = info.Call<AndroidJavaObject>("getTraceInputStream"))
                {
                    if (stream == null) return "";
                    using (var streamReader = new AndroidJavaObject("java.io.InputStreamReader", stream))
                    using (var reader = new AndroidJavaObject("java.io.BufferedReader", streamReader))
                    {
                        var s = new StringBuilder();
                        string line;
                        while (s.Length < MaxTraceChars && (line = reader.Call<string>("readLine")) != null) s.Append(line).Append('\n');
                        return NativeStack.FromAnrTrace(s.ToString());
                    }
                }
            }
            catch (Exception)
            {
                return "";
            }
        }
    }
#endif
}
