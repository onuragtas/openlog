using System;
using System.Collections;
using System.Collections.Generic;
using System.IO;
using System.Text;
using OpenLogUnity.Core;
using UnityEngine;
using UnityEngine.Networking;
using UnityEngine.SceneManagement;

namespace OpenLogUnity
{
    /// <summary>What a game passes to OpenLog.Init.</summary>
    public sealed class OpenLogOptions
    {
        /// <summary>The mobile key (olb_...). Public by construction: it ships inside the game.</summary>
        public string Key;
        /// <summary>The base URL of openlog ingest, e.g. https://apm.resoft.org</summary>
        public string Endpoint;
        /// <summary>Used until GET /v1/rum/config answers with the operator's value, which wins.</summary>
        public double SampleRate = 1;
        public long FlushIntervalMs = 5000;
        /// <summary>Report a screen for each scene loaded as a single scene.</summary>
        public bool TrackScenes = true;
        /// <summary>Report exceptions Unity logs. They do not count as crashes, because they do not end the game.</summary>
        public bool CaptureExceptions = true;
        /// <summary>
        /// Recognise a player across launches, for DAU, MAU and retention (analytics.md §1). This stores a persistent id on the
        /// device, and whether that needs consent is your question. If it does, pass false and call SetAnalyticsCollection(true).
        /// </summary>
        public bool AnalyticsCollection = true;
        /// <summary>
        /// Report native crashes, ANRs and out-of-memory kills on the next launch, and say this build can (mobile-crash.md §2).
        /// Android 11 and iOS 14 or later. It reads what the system recorded, so no handler of yours is replaced.
        /// </summary>
        public bool CaptureCrashes;
        /// <summary>Send from the editor too. Off, so that playing the game while working on it is not counted as players.</summary>
        public bool EnableInEditor;
    }

    /// <summary>The openlog SDK. Nothing here throws into the game: a monitoring library must not break what it monitors.</summary>
    public static class OpenLog
    {
        private static Client _client;
        private static OpenLogDriver _driver;

        public static string SessionId => _client?.SessionId ?? "";
        public static string VisitorId => _client?.VisitorId ?? "";

        /// <summary>Starts the SDK. A second call does nothing, because a second instance would double-count everything.</summary>
        public static void Init(OpenLogOptions options)
        {
            if (_client != null || options == null) return;
            if (Application.isEditor && !options.EnableInEditor) return;
            try
            {
                var dir = Path.Combine(Application.persistentDataPath, "openlog");
                Directory.CreateDirectory(dir);
                var go = new GameObject("openlog") { hideFlags = HideFlags.HideAndDontSave };
                UnityEngine.Object.DontDestroyOnLoad(go);
                _driver = go.AddComponent<OpenLogDriver>();
                _client = new Client(new ClientConfig
                {
                    Key = options.Key,
                    Endpoint = options.Endpoint,
                    AppId = Application.identifier,
                    Version = Application.version,
                    DeviceModel = SystemInfo.deviceModel,
                    DeviceType = SystemInfo.deviceType == DeviceType.Handheld ? "mobile" : "unknown",
                    OsName = Application.platform == RuntimePlatform.Android ? "Android" : Application.platform == RuntimePlatform.IPhonePlayer ? "iOS" : Application.platform.ToString(),
                    OsVersion = SystemInfo.operatingSystem,
                    SampleRate = options.SampleRate,
                    FlushIntervalMs = options.FlushIntervalMs,
                    AnalyticsCollection = options.AnalyticsCollection,
                    CaptureCrashes = options.CaptureCrashes,
                    Directory = dir,
                }, _driver, () => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds());
                _driver.Client = _client;
                if (options.CaptureCrashes)
                {
                    NativeCrashes.Collect(_client, dir);
                    _driver.WatchNativeCrashes(dir);
                }
                _client.Begin();
                if (options.CaptureExceptions) Application.logMessageReceivedThreaded += OnLog;
                if (options.TrackScenes)
                {
                    SceneManager.sceneLoaded += OnScene;
                    _client.RecordScreen(SceneManager.GetActiveScene().name, true);
                }
            }
            catch (Exception e)
            {
                _client = null;
                Debug.LogWarning("openlog: not started: " + e.Message);
            }
        }

        private static void OnLog(string condition, string stack, LogType type)
        {
            if (type != LogType.Exception) return;
            try
            {
                _client?.RecordLoggedException(condition, stack);
            }
            catch (Exception)
            {
                // Logging from here would come straight back.
            }
        }

        private static void OnScene(Scene scene, LoadSceneMode mode)
        {
            if (mode == LoadSceneMode.Single) RecordScreen(scene.name);
        }

        private static void Guard(Action a)
        {
            try
            {
                a();
            }
            catch (Exception e)
            {
                Debug.LogWarning("openlog: dropped an event: " + e.Message);
            }
        }

        /// <summary>A screen the player opened, when a scene does not say enough.</summary>
        public static void RecordScreen(string name) => Guard(() => _client?.RecordScreen(name));

        /// <summary>e.g. RecordEvent("level_completed", new Dictionary&lt;string, object&gt; { ["level"] = 12 })</summary>
        public static void RecordEvent(string name, Dictionary<string, object> parameters = null) => Guard(() => _client?.RecordEvent(name, parameters));

        public static void RecordTiming(string name, long milliseconds, Dictionary<string, object> parameters = null) => Guard(() => _client?.RecordTiming(name, milliseconds, parameters));

        /// <summary>An exception the game caught itself.</summary>
        public static void RecordError(Exception error) => Guard(() => _client?.RecordError(error.GetType().Name, error.Message, error.ToString()));

        /// <summary>An error that ended the game for the player, counted as a crash. Unity keeps running after an exception, so only the game knows.</summary>
        public static void RecordFatal(Exception error) => Guard(() => _client?.RecordFatal(error.GetType().Name, error.Message, error.ToString()));

        /// <summary>The game's own id for the player, on every later event. An opaque id, never an e-mail address or a name. "" clears it.</summary>
        public static void Identify(string userId) => Guard(() => _client?.Identify(userId));

        /// <summary>Off deletes the stored id and is remembered across launches. It erases nothing already sent: see POST /api/v1/analytics/erase.</summary>
        public static void SetAnalyticsCollection(bool enabled) => Guard(() => _client?.SetAnalyticsCollection(enabled));

        public static void Flush() => Guard(() => _client?.Flush());
    }

    /// <summary>Runs the SDK on the main thread and makes its requests.</summary>
    [AddComponentMenu("")]
    public sealed class OpenLogDriver : MonoBehaviour, ISender
    {
        internal Client Client;
        private string _watchDir;
        private float _nextWatch;

        internal void WatchNativeCrashes(string dir) => _watchDir = dir;

        private void Update()
        {
            Client?.Tick();
            // MetricKit delivers when the system decides, which can be well into a session.
            if (_watchDir != null && Time.unscaledTime >= _nextWatch)
            {
                _nextWatch = Time.unscaledTime + 30;
                if (NativeCrashes.CollectLater(Client, _watchDir)) Client?.DeliverCrashes();
            }
        }

        // On a phone this is the last reliable moment, and often the only one a player gives.
        private void OnApplicationPause(bool paused)
        {
            if (paused) Client?.Flush();
        }

        private void OnApplicationQuit() => Client?.Flush();

        public void Post(string url, Dictionary<string, string> headers, string body, Action<int> done) => StartCoroutine(PostRoutine(url, headers, body, done));

        public void Get(string url, Dictionary<string, string> headers, Action<int, string> done) => StartCoroutine(GetRoutine(url, headers, done));

        private static IEnumerator PostRoutine(string url, Dictionary<string, string> headers, string body, Action<int> done)
        {
            using (var req = new UnityWebRequest(url, UnityWebRequest.kHttpVerbPOST))
            {
                req.uploadHandler = new UploadHandlerRaw(Encoding.UTF8.GetBytes(body)) { contentType = "application/json" };
                req.downloadHandler = new DownloadHandlerBuffer();
                req.timeout = 15;
                foreach (var h in headers) req.SetRequestHeader(h.Key, h.Value);
                yield return req.SendWebRequest();
                done(req.responseCode > 0 ? (int)req.responseCode : -1);
            }
        }

        private static IEnumerator GetRoutine(string url, Dictionary<string, string> headers, Action<int, string> done)
        {
            using (var req = UnityWebRequest.Get(url))
            {
                req.timeout = 15;
                foreach (var h in headers) req.SetRequestHeader(h.Key, h.Value);
                yield return req.SendWebRequest();
                done(req.responseCode > 0 ? (int)req.responseCode : -1, req.downloadHandler?.text);
            }
        }
    }
}
