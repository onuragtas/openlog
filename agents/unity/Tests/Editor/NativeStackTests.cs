using System.Collections.Generic;
using System.IO;
using System.Text;
using NUnit.Framework;
using OpenLogUnity.Core;

namespace OpenLogUnity.Tests
{
    public sealed class NativeStackTests
    {
        private const string Tree = @"{""callStackPerThread"":true,""callStacks"":[
            {""threadAttributed"":false,""callStackRootFrames"":[{""binaryUUID"":""AAAAAAAA-0000-0000-0000-000000000000"",""offsetIntoBinaryTextSegment"":1,""binaryName"":""Other""}]},
            {""threadAttributed"":true,""callStackRootFrames"":[{""binaryUUID"":""A1B2C3D4-E5F6-0718-A1B2-C3D4E5F60718"",""offsetIntoBinaryTextSegment"":4096,""sampleCount"":1,""binaryName"":""My Game"",""address"":74565,
                ""subFrames"":[{""binaryUUID"":""FFFFFFFF-0000-0000-0000-000000000000"",""offsetIntoBinaryTextSegment"":16,""binaryName"":""libsystem_c.dylib""}]}]}]}";

        [Test]
        public void FlattensTheThreadThatCrashed()
        {
            var lines = NativeStack.FromMetricKit(Tree).TrimEnd().Split('\n');
            Assert.AreEqual(2, lines.Length);
            Assert.AreEqual("#00 My_Game A1B2C3D4-E5F6-0718-A1B2-C3D4E5F60718 +0x1000", lines[0]);
            Assert.AreEqual("#01 libsystem_c.dylib FFFFFFFF-0000-0000-0000-000000000000 +0x10", lines[1]);
        }

        [TestCase("not json at all")]
        [TestCase("{\"callStacks\":[]}")]
        [TestCase("{\"callStacks\":[{\"threadAttributed\":tr")]
        public void ReturnsWhatItCannotReadAsItCame(string doc)
        {
            Assert.AreEqual(doc, NativeStack.FromMetricKit(doc));
        }

        [Test]
        public void DoesNotOverflowOnANestedDocument()
        {
            var doc = new string('[', 200000);
            Assert.AreEqual(doc, NativeStack.FromMetricKit(doc));
        }

        [Test]
        public void AnAnrStartsAtTheThreadThatDidNotRespond()
        {
            Assert.AreEqual("\"main\" prio=5\n  at a.b.c(SourceFile:3)\n", NativeStack.FromAnrTrace("Subject: x\nRssKb: 1\n\n\"main\" prio=5\n  at a.b.c(SourceFile:3)\n"));
            Assert.AreEqual("no threads here", NativeStack.FromAnrTrace("no threads here"));
            Assert.AreEqual("", NativeStack.FromAnrTrace(""));
        }

        [Test]
        public void AStackIsNotCutToTheLimitOfAnAttribute()
        {
            var span = Otlp.Span("e", "error", "t", "s", null, 1, 0, new List<KeyValuePair<string, string>>(), true, "T", "m", new string('x', 9000));
            StringAssert.Contains(new string('x', 9000), Json.Encode(span));
        }

        // A protobuf written by hand, field for field from AOSP's tombstone.proto.
        private static void Varint(Stream s, ulong v)
        {
            while (v >= 0x80)
            {
                s.WriteByte((byte)(v | 0x80));
                v >>= 7;
            }
            s.WriteByte((byte)v);
        }

        private static void Int(Stream s, int field, ulong v)
        {
            Varint(s, (ulong)field << 3);
            Varint(s, v);
        }

        private static void Bytes(Stream s, int field, byte[] b)
        {
            Varint(s, (ulong)field << 3 | 2);
            Varint(s, (ulong)b.Length);
            s.Write(b, 0, b.Length);
        }

        private static void Text(Stream s, int field, string v) => Bytes(s, field, Encoding.UTF8.GetBytes(v));

        private static byte[] Frame(ulong relPc, string function, string file, string buildId)
        {
            var f = new MemoryStream();
            Int(f, 1, relPc);
            Int(f, 2, 0x7000000000 + relPc); // pc: moves each launch, and must not be what is read
            Text(f, 4, function);
            Int(f, 5, 12);
            Text(f, 6, file);
            Text(f, 8, buildId);
            return f.ToArray();
        }

        private static byte[] Thread(int id, params byte[][] frames)
        {
            var t = new MemoryStream();
            Int(t, 1, (ulong)id);
            Text(t, 2, "UnityMain");
            foreach (var f in frames) Bytes(t, 4, f);
            return t.ToArray();
        }

        private static byte[] Entry(int id, byte[] thread)
        {
            var e = new MemoryStream();
            Int(e, 1, (ulong)id);
            Bytes(e, 2, thread);
            return e.ToArray();
        }

        [Test]
        public void ReadsATombstone()
        {
            var signal = new MemoryStream();
            Int(signal, 1, 11);
            Text(signal, 2, "SIGSEGV");
            Int(signal, 3, 1);
            Text(signal, 4, "SEGV_MAPERR");
            var t = new MemoryStream();
            Int(t, 1, 1); // arch
            Int(t, 6, 42); // tid
            Bytes(t, 10, signal.ToArray());
            Bytes(t, 16, Entry(7, Thread(7, Frame(1, "other", "/x/other.so", "00"))));
            Bytes(t, 16, Entry(42, Thread(42,
                Frame(0x1a2b, "", "/data/app/~~x==/org.game/lib/arm64/libil2cpp.so", "0123456789abcdef0123456789abcdef"),
                Frame(0x50, "abort", "/apex/com.android.runtime/lib64/bionic/libc.so", "fedcba9876543210fedcba9876543210"))));

            var got = Tombstone.Decode(t.ToArray());

            Assert.AreEqual("SIGSEGV", got.SignalName);
            Assert.AreEqual("SIGSEGV (SEGV_MAPERR)", got.Message());
            var lines = got.Stack.TrimEnd().Split('\n');
            Assert.AreEqual(2, lines.Length, got.Stack);
            Assert.AreEqual("#00 libil2cpp.so 0123456789abcdef0123456789abcdef +0x1a2b", lines[0]);
            Assert.AreEqual("#01 libc.so fedcba9876543210fedcba9876543210 +0x50 abort", lines[1]);
        }

        [Test]
        public void AnAbortSpeaksForItself()
        {
            var signal = new MemoryStream();
            Text(signal, 2, "SIGABRT");
            var t = new MemoryStream();
            Bytes(t, 10, signal.ToArray());
            Text(t, 14, "the game gave up");
            Assert.AreEqual("the game gave up", Tombstone.Decode(t.ToArray()).Message());
        }

        [Test]
        public void RefusesWhatIsNotATombstone()
        {
            Assert.IsNull(Tombstone.Decode(Encoding.UTF8.GetBytes("*** *** *** *** a text trace")));
            Assert.IsNull(Tombstone.Decode(new byte[] { 0x82, 0x01, 0xff, 0xff, 0xff, 0x7f }));
            Assert.IsNull(Tombstone.Decode(new byte[0]));
        }
    }
}
