using System;
using System.Collections.Generic;
using System.IO;
using NUnit.Framework;
using OpenLogUnity.Core;

namespace OpenLogUnity.Tests
{
    // These run in the Unity Test Runner and, with no editor licence, under dotnet (Tests~/dotnet).
    public sealed class ClientTests
    {
        private sealed class FakeSender : ISender
        {
            public readonly List<string> Bodies = new List<string>();
            public Dictionary<string, string> GetHeaders;
            public int Status = 200;
            public string Config;
            public bool HoldPosts;
            public Action<int> Held;

            public void Post(string url, Dictionary<string, string> headers, string body, Action<int> done)
            {
                Bodies.Add(body);
                if (HoldPosts) Held = done;
                else done(Status);
            }

            public void Get(string url, Dictionary<string, string> headers, Action<int, string> done)
            {
                GetHeaders = headers;
                done(Config == null ? 500 : 200, Config);
            }
        }

        private string _dir;
        private FakeSender _sender;
        private long _now;

        [SetUp]
        public void SetUp()
        {
            _dir = Path.Combine(Path.GetTempPath(), "openlog-unity-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_dir);
            _sender = new FakeSender();
            _now = 1_700_000_000_000;
        }

        [TearDown]
        public void TearDown() => Directory.Delete(_dir, true);

        private string VisitorFile => Path.Combine(_dir, "openlog_visitor");

        private Client Start(bool collection = true, bool crashes = false, double rate = 1, string version = "1.2.3") => new Client(new ClientConfig
        {
            Key = "olb_x",
            Endpoint = "https://ingest.example.com/",
            AppId = "com.example.game",
            Version = version,
            AnalyticsCollection = collection,
            CaptureCrashes = crashes,
            SampleRate = rate,
            Directory = _dir,
        }, _sender, () => _now);

        // ---- the visitor

        [Test]
        public void LeavesNothingOnTheDeviceUntilTheServerConfirms()
        {
            var c = Start();
            Assert.That(c.VisitorId, Does.Match("^[0-9a-f]{32}$"));
            Assert.That(File.Exists(VisitorFile), Is.False);
            _sender.Config = "{\"sample_rate\":1,\"analytics_mode\":\"visitors\"}";
            c.Begin();
            Assert.That(File.Exists(VisitorFile), Is.True);
            Assert.That(Start().VisitorId, Is.EqualTo(c.VisitorId), "the same player on the next launch");
        }

        [Test]
        public void ForgetsTheVisitorOnAKeyThatIsOff()
        {
            _sender.Config = "{\"analytics_mode\":\"visitors\"}";
            Start().Begin();
            _sender.Config = "{\"analytics_mode\":\"off\"}";
            var c = Start();
            c.Begin();
            Assert.That(c.VisitorId, Is.Empty);
            Assert.That(File.Exists(VisitorFile), Is.False);
        }

        [Test]
        public void AWithdrawalOutlivesTheProcessAndAServerAnswer()
        {
            _sender.Config = "{\"analytics_mode\":\"visitors\"}";
            var c = Start();
            c.Begin();
            var before = c.VisitorId;
            c.SetAnalyticsCollection(false);
            Assert.That(File.ReadAllText(VisitorFile), Does.Not.Contain(before));
            var again = Start();
            Assert.That(again.VisitorId, Is.Empty, "the default must not undo a person saying no");
            again.Begin();
            again.SetAnalyticsCollection(true);
            Assert.That(again.VisitorId, Does.Match("^[0-9a-f]{32}$").And.Not.EqualTo(before));
        }

        [Test]
        public void HasNoVisitorWithoutADirectory()
        {
            var v = new Visitor(true, null, () => _now);
            Assert.That(v.Id, Is.Empty, "a new one per launch would count every launch as a person");
        }

        [Test]
        public void RotatesTheVisitorAfter13Months()
        {
            var a = new Visitor(true, _dir, () => _now);
            a.Confirm();
            _now += 394L * 24 * 3600 * 1000;
            Assert.That(new Visitor(true, _dir, () => _now).Id, Is.EqualTo(a.Id));
            _now += 2L * 24 * 3600 * 1000;
            Assert.That(new Visitor(true, _dir, () => _now).Id, Is.Not.EqualTo(a.Id));
        }

        [Test]
        public void MatchesTheVectorEverySdkIsTestedAgainst()
        {
            Assert.That(Visitor.Roll("fedcba9876543210fedcba9876543210"), Is.EqualTo(777805509 / 4294967296.0));
        }

        // ---- requests

        [Test]
        public void NamesTheApplicationWhenItReadsTheConfig()
        {
            Start().Begin();
            Assert.That(_sender.GetHeaders["openlog-app-id"], Is.EqualTo("com.example.game"));
        }

        [Test]
        public void SendsAScreenAndAnEventInOneBatchWhenDue()
        {
            var c = Start();
            c.RecordScreen("Menu", true);
            c.RecordEvent("level_completed", new Dictionary<string, object> { ["level"] = 12, ["Bad Key"] = "x" });
            c.Tick();
            Assert.That(_sender.Bodies, Is.Empty, "not yet due");
            _now += 6000;
            c.Tick();
            Assert.That(_sender.Bodies.Count, Is.EqualTo(1));
            var body = _sender.Bodies[0];
            Assert.That(body, Does.Contain("page_view").And.Contain("Menu").And.Contain("\"load\""));
            Assert.That(body, Does.Contain("openlog.rum.custom.param.level").And.Not.Contain("Bad Key"));
            Assert.That(body, Does.Contain("1.2.3").And.Contain(c.SessionId));
        }

        [Test]
        public void FlushSendsAtOnceAndOnlyOneRequestIsInFlight()
        {
            var c = Start();
            _sender.HoldPosts = true;
            c.RecordEvent("a");
            c.Flush();
            c.RecordEvent("b");
            c.Flush();
            Assert.That(_sender.Bodies.Count, Is.EqualTo(1));
            _sender.Held(200);
            c.Flush();
            Assert.That(_sender.Bodies.Count, Is.EqualTo(2));
        }

        [Test]
        public void RetriesWhatIsTemporaryAndStopsOnARefusedKey()
        {
            var c = Start();
            _sender.Status = 503;
            c.RecordEvent("kept");
            c.Flush();
            _sender.Status = 200;
            c.Flush();
            Assert.That(_sender.Bodies.Count, Is.EqualTo(2));
            Assert.That(_sender.Bodies[1], Does.Contain("kept"));

            _sender.Status = 403;
            c.RecordEvent("x");
            c.Flush();
            Assert.That(c.Refused, Is.True);
            var sent = _sender.Bodies.Count;
            c.RecordEvent("y");
            c.Flush();
            Assert.That(_sender.Bodies.Count, Is.EqualTo(sent));
        }

        [Test]
        public void ASampledOutSessionSendsNothing()
        {
            var c = Start(rate: 0);
            c.RecordEvent("x");
            c.Flush();
            Assert.That(_sender.Bodies, Is.Empty);
            Assert.That(c.SessionId, Is.Empty);
        }

        [Test]
        public void AQuietSessionEndsAndANewOneBegins()
        {
            var s = new SessionState(1, () => _now, () => 0);
            var first = s.Id;
            _now += 29 * 60 * 1000;
            Assert.That(s.Touch(), Is.False);
            _now += 31 * 60 * 1000;
            Assert.That(s.Touch(), Is.True);
            Assert.That(s.Id, Is.Not.EqualTo(first));
        }

        // ---- exceptions

        [Test]
        public void AnExceptionInAnUpdateDoesNotBecomeSixtyEventsASecond()
        {
            var c = Start();
            for (var i = 0; i < 120; i++) c.RecordLoggedException("NullReferenceException: nope", "at Foo.Update ()");
            c.Flush();
            Assert.That(_sender.Bodies.Count, Is.EqualTo(1));
            Assert.That(_sender.Bodies[0].Split(new[] { "\"spanId\"" }, StringSplitOptions.None).Length - 1, Is.EqualTo(1));
            _now += ExceptionFilter.WindowMs;
            c.RecordLoggedException("NullReferenceException: nope", "at Foo.Update ()");
            c.Flush();
            Assert.That(_sender.Bodies.Count, Is.EqualTo(2), "still happening, and it says so");
        }

        [Test]
        public void ALoggedExceptionIsAnErrorAndNotACrash()
        {
            var c = Start(crashes: true);
            c.RecordLoggedException("InvalidOperationException: bad state", "at Foo.Bar ()");
            c.Flush();
            var body = _sender.Bodies[0];
            Assert.That(body, Does.Contain("InvalidOperationException").And.Contain("bad state").And.Contain("at Foo.Bar"));
            Assert.That(body, Does.Not.Contain("openlog.rum.crash.kind"));
        }

        [TestCase("NullReferenceException: x: y", "NullReferenceException", "x: y")]
        [TestCase("something broke", "Exception", "something broke")]
        [TestCase(null, "Exception", "")]
        public void ReadsWhatUnityLogs(string condition, string type, string message)
        {
            Client.ParseCondition(condition, out var t, out var m);
            Assert.That(t, Is.EqualTo(type));
            Assert.That(m, Is.EqualTo(message));
        }

        // ---- crashes

        [Test]
        public void RecordFatalIsACrashAndGoesOnTheNextFrame()
        {
            var c = Start(crashes: true);
            c.RecordScreen("Level");
            c.RecordFatal("SaveCorrupted", "cannot continue", "at Save.Load ()");
            c.Tick();
            Assert.That(_sender.Bodies[0], Does.Contain("uncaught_exception").And.Contain("SaveCorrupted"));
        }

        [Test]
        public void AnEarlierCrashTakesTheSessionAndBuildOfThatLaunch()
        {
            var earlier = Start(crashes: true, version: "1.0.0");
            var session = earlier.SessionId;
            var next = Start(crashes: true, version: "2.0.0");
            next.RecordEarlierCrash("signal", "SIGSEGV", "bad access", "stack", "1.0.0", 1_699_000_000_000);
            Assert.That(_sender.Bodies, Is.Empty, "written first");
            next.DeliverCrashes();
            var body = _sender.Bodies[0];
            Assert.That(body, Does.Contain(session).And.Contain("1.0.0").And.Not.Contain("2.0.0"));
            Assert.That(body, Does.Contain("1699000000000000000"), "when it crashed");
            Assert.That(body, Does.Contain("\"signal\"").And.Not.Contain("session_unknown"));
        }

        [Test]
        public void ACrashOfAnotherBuildSaysItsSessionIsUnknown()
        {
            Start(crashes: true, version: "1.0.0");
            var next = Start(crashes: true, version: "2.0.0");
            next.RecordEarlierCrash("signal", "SIGSEGV", "m", "s", "0.9.0", _now);
            next.DeliverCrashes();
            Assert.That(_sender.Bodies[0], Does.Contain("session_unknown"));
        }

        [TestCase(-1, 1)]
        [TestCase(429, 1)]
        [TestCase(503, 1)]
        [TestCase(200, 0)]
        [TestCase(400, 0)]
        public void KeepsAReportUntilTheServerHasAnsweredForGood(int status, int left)
        {
            var c = Start(crashes: true);
            c.RecordEarlierCrash("signal", "T", "m", "s", "1.2.3", _now);
            _sender.Status = status;
            c.DeliverCrashes();
            Assert.That(new CrashStore(Path.Combine(_dir, "openlog-crashes"), () => _now).Pending().Count, Is.EqualTo(left));
        }

        [Test]
        public void ACrashLoopCannotFillTheDeviceAndKeepsItsFirstReports()
        {
            var store = new CrashStore(_dir, () => _now++);
            for (var i = 0; i < CrashStore.MaxFiles + 5; i++) store.Write("id" + i, "report " + i);
            var files = store.Pending();
            Assert.That(files.Count, Is.EqualTo(CrashStore.MaxFiles));
            Assert.That(File.ReadAllText(files[0]), Is.EqualTo("report 0"));
        }

        [Test]
        public void ABuildSaysItCanReportCrashesOnlyWhenItCan()
        {
            var c = Start(crashes: true);
            c.RecordEvent("x");
            c.Flush();
            Assert.That(_sender.Bodies[0], Does.Contain("openlog.rum.crash.capture"));

            var plain = Start();
            plain.RecordEvent("x");
            plain.Flush();
            Assert.That(_sender.Bodies[1], Does.Not.Contain("openlog.rum.crash.capture"));
        }

        [Test]
        public void RefusesAKeyThatIsNotAMobileKey()
        {
            Assert.Throws<ArgumentException>(() => new Client(new ClientConfig { Key = "olk_secret", Endpoint = "https://x", AppId = "a" }, _sender, () => _now));
        }

        // ---- encoding

        [Test]
        public void EscapesAndBoundsWhatItWrites()
        {
            Assert.That(Json.Encode("a\"b\\c\nd\u0001"), Is.EqualTo("\"a\\\"b\\\\c\\nd\\u0001\""));
            Assert.That(Limits.Truncate("çççç", 3), Is.EqualTo("ç"), "never half a character");
        }
    }
}
