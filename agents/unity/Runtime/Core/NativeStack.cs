using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;

namespace OpenLogUnity.Core
{
    // What the operating system says about a native crash, written the way the server reads it (mobile-crash.md §3):
    //
    //   #00 libil2cpp.so 0123abcd... +0x1a2b Game::Update()
    //
    // An image id and an offset, because that is what symbols are found by. An address alone would mean nothing: where an image
    // loads changes each launch.
    public static class NativeStack
    {
        public static string Frame(int index, string binary, string imageId, ulong offset, string function)
        {
            var s = new StringBuilder();
            s.Append('#').Append(index.ToString("00", CultureInfo.InvariantCulture)).Append(' ').Append(Binary(binary)).Append(' ');
            if (!string.IsNullOrEmpty(imageId)) s.Append(imageId).Append(' ');
            s.Append("+0x").Append(offset.ToString("x", CultureInfo.InvariantCulture));
            if (!string.IsNullOrEmpty(function)) s.Append(' ').Append(function);
            return s.ToString();
        }

        /// <summary>The file name alone, since a path holds a random install directory, and with no spaces, since they part the fields.</summary>
        private static string Binary(string path)
        {
            if (string.IsNullOrEmpty(path)) return "unknown";
            var name = path.Substring(path.LastIndexOf('/') + 1);
            return name.Replace(' ', '_');
        }

        /// <summary>Flattens MetricKit's call stack tree to the crashed thread. What cannot be read is returned as it came.</summary>
        public static string FromMetricKit(string tree)
        {
            try
            {
                if (!(new MiniJson(tree).Parse() is Dictionary<string, object> root)) return tree ?? "";
                if (!(Get(root, "callStacks") is List<object> stacks) || stacks.Count == 0) return tree;
                // threadAttributed marks the thread that crashed. A report with none is still better read from its first.
                var chosen = stacks[0] as Dictionary<string, object>;
                foreach (var s in stacks)
                {
                    if (s is Dictionary<string, object> d && Get(d, "threadAttributed") is bool b && b)
                    {
                        chosen = d;
                        break;
                    }
                }
                if (chosen == null || !(Get(chosen, "callStackRootFrames") is List<object> roots) || roots.Count == 0) return tree;

                // The root is the innermost frame, and each frame's subFrames hold what called it.
                var lines = new StringBuilder();
                var frame = roots[0] as Dictionary<string, object>;
                for (var i = 0; frame != null && lines.Length < Limits.MaxStackBytes; i++)
                {
                    var offset = Get(frame, "offsetIntoBinaryTextSegment") is ulong o ? o : 0;
                    lines.Append(Frame(i, Get(frame, "binaryName") as string, Get(frame, "binaryUUID") as string, offset, null)).Append('\n');
                    frame = Get(frame, "subFrames") is List<object> subs && subs.Count > 0 ? subs[0] as Dictionary<string, object> : null;
                }
                return lines.ToString();
            }
            catch (Exception)
            {
                return tree ?? "";
            }
        }

        /// <summary>
        /// An ANR's dump opens with memory figures and a system event log, and a stack limit would be spent on them. The thread that
        /// did not respond is what the report is for, so it starts there.
        /// </summary>
        public static string FromAnrTrace(string trace)
        {
            if (string.IsNullOrEmpty(trace)) return "";
            var i = trace.IndexOf("\n\"main\"", StringComparison.Ordinal);
            return i < 0 ? trace : trace.Substring(i + 1);
        }

        private static object Get(Dictionary<string, object> d, string key) => d.TryGetValue(key, out var v) ? v : null;

        /// <summary>
        /// Enough JSON for a call stack tree. JsonUtility cannot read one: it does not recurse into a type of itself, and this tree
        /// is nothing but that. With an explicit stack and no recursion, because a tree is as deep as the crash was, and a stack
        /// overflow here would be a crash in the crash reporter.
        /// </summary>
        private sealed class MiniJson
        {
            private readonly string _s;
            private int _i;

            public MiniJson(string s)
            {
                _s = s ?? throw new FormatException("no document");
            }

            public object Parse()
            {
                var open = new Stack<object>();
                var keys = new Stack<string>();
                while (true)
                {
                    Space();
                    object value;
                    var c = Peek();
                    if (c == '[' || c == '{')
                    {
                        _i++;
                        Space();
                        object container = c == '[' ? (object)new List<object>() : new Dictionary<string, object>();
                        if (Peek() != (c == '[' ? ']' : '}'))
                        {
                            open.Push(container);
                            if (c == '{') keys.Push(Key());
                            continue;
                        }
                        _i++;
                        value = container;
                    }
                    else
                    {
                        value = Scalar(c);
                    }

                    // A finished value goes into what holds it, which may finish that too.
                    var more = false;
                    while (!more)
                    {
                        if (open.Count == 0)
                        {
                            Space();
                            if (_i != _s.Length) throw new FormatException("trailing data");
                            return value;
                        }
                        Space();
                        var sep = Peek();
                        _i++;
                        if (open.Peek() is List<object> list)
                        {
                            list.Add(value);
                            if (sep == ',') more = true;
                            else if (sep == ']') value = open.Pop();
                            else throw new FormatException("expected , or ]");
                        }
                        else
                        {
                            ((Dictionary<string, object>)open.Peek())[keys.Pop()] = value;
                            if (sep == ',')
                            {
                                keys.Push(Key());
                                more = true;
                            }
                            else if (sep == '}') value = open.Pop();
                            else throw new FormatException("expected , or }");
                        }
                    }
                }
            }

            private string Key()
            {
                Space();
                if (Peek() != '"') throw new FormatException("expected a key");
                var key = Str();
                Space();
                if (Peek() != ':') throw new FormatException("expected :");
                _i++;
                return key;
            }

            private void Space()
            {
                while (_i < _s.Length && char.IsWhiteSpace(_s[_i])) _i++;
            }

            private char Peek()
            {
                if (_i >= _s.Length) throw new FormatException("ended early");
                return _s[_i];
            }

            private object Scalar(char c)
            {
                switch (c)
                {
                    case '"': return Str();
                    case 't': return Word("true", true);
                    case 'f': return Word("false", false);
                    case 'n': return Word("null", null);
                    default: return Num();
                }
            }

            private object Word(string word, object value)
            {
                if (string.CompareOrdinal(_s, _i, word, 0, word.Length) != 0) throw new FormatException("bad literal");
                _i += word.Length;
                return value;
            }

            private object Num()
            {
                var start = _i;
                while (_i < _s.Length && "+-.eE0123456789".IndexOf(_s[_i]) >= 0) _i++;
                if (start == _i) throw new FormatException("bad value");
                var text = _s.Substring(start, _i - start);
                if (ulong.TryParse(text, NumberStyles.None, CultureInfo.InvariantCulture, out var u)) return u;
                return double.Parse(text, NumberStyles.Float, CultureInfo.InvariantCulture);
            }

            private string Str()
            {
                _i++;
                var b = new StringBuilder();
                while (true)
                {
                    var c = Peek();
                    _i++;
                    if (c == '"') return b.ToString();
                    if (c != '\\')
                    {
                        b.Append(c);
                        continue;
                    }
                    var e = Peek();
                    _i++;
                    switch (e)
                    {
                        case 'n': b.Append('\n'); break;
                        case 't': b.Append('\t'); break;
                        case 'r': b.Append('\r'); break;
                        case 'b': b.Append('\b'); break;
                        case 'f': b.Append('\f'); break;
                        case 'u':
                            if (_i + 4 > _s.Length) throw new FormatException("ended early");
                            b.Append((char)int.Parse(_s.Substring(_i, 4), NumberStyles.HexNumber, CultureInfo.InvariantCulture));
                            _i += 4;
                            break;
                        default: b.Append(e); break;
                    }
                }
            }
        }
    }

    /// <summary>
    /// Android's record of a native crash, which ApplicationExitInfo hands over from API 31 as a protobuf (AOSP tombstone.proto).
    /// Read by hand for the few fields a report uses, so the game carries no protobuf library.
    /// </summary>
    public sealed class Tombstone
    {
        public string SignalName = "";
        public string CodeName = "";
        public int SignalNumber;
        public string AbortMessage = "";
        public string Stack = "";

        /// <summary>A short line for the crash message: the abort's own words when there are any.</summary>
        public string Message()
        {
            if (AbortMessage.Length > 0) return AbortMessage;
            return CodeName.Length > 0 ? SignalName + " (" + CodeName + ")" : SignalName;
        }

        /// <summary>Null when the bytes are not a tombstone, as on a device that writes some other form.</summary>
        public static Tombstone Decode(byte[] data)
        {
            try
            {
                var t = new Tombstone();
                uint tid = 0;
                var threads = new Dictionary<uint, Reader>();
                var r = new Reader(data, 0, data.Length);
                while (r.Next(out var field, out var wire))
                {
                    switch (field)
                    {
                        case 6 when wire == 0: tid = (uint)r.Varint(); break;
                        case 10 when wire == 2: t.ReadSignal(r.Message()); break;
                        case 14 when wire == 2: t.AbortMessage = r.Text(); break;
                        case 16 when wire == 2:
                            var entry = r.Message();
                            uint key = 0;
                            Reader value = null;
                            while (entry.Next(out var ef, out var ew))
                            {
                                if (ef == 1 && ew == 0) key = (uint)entry.Varint();
                                else if (ef == 2 && ew == 2) value = entry.Message();
                                else entry.Skip(ew);
                            }
                            if (value != null) threads[key] = value;
                            break;
                        default: r.Skip(wire); break;
                    }
                }
                if (t.SignalName.Length == 0 && !threads.ContainsKey(tid)) return null;
                if (threads.TryGetValue(tid, out var crashed)) t.ReadThread(crashed);
                return t;
            }
            catch (FormatException)
            {
                return null;
            }
        }

        private void ReadSignal(Reader r)
        {
            while (r.Next(out var field, out var wire))
            {
                if (field == 1 && wire == 0) SignalNumber = (int)r.Varint();
                else if (field == 2 && wire == 2) SignalName = r.Text();
                else if (field == 4 && wire == 2) CodeName = r.Text();
                else r.Skip(wire);
            }
        }

        private void ReadThread(Reader r)
        {
            var lines = new StringBuilder();
            var index = 0;
            while (r.Next(out var field, out var wire))
            {
                if (field != 4 || wire != 2)
                {
                    r.Skip(wire);
                    continue;
                }
                var frame = r.Message();
                ulong relPc = 0;
                string function = "", file = "", buildId = "";
                while (frame.Next(out var ff, out var fw))
                {
                    if (ff == 1 && fw == 0) relPc = frame.Varint();
                    else if (ff == 4 && fw == 2) function = frame.Text();
                    else if (ff == 6 && fw == 2) file = frame.Text();
                    else if (ff == 8 && fw == 2) buildId = frame.Text();
                    else frame.Skip(fw);
                }
                if (lines.Length < Limits.MaxStackBytes) lines.Append(NativeStack.Frame(index, file, buildId, relPc, function)).Append('\n');
                index++;
            }
            Stack = lines.ToString();
        }

        private sealed class Reader
        {
            private readonly byte[] _b;
            private readonly int _end;
            private int _i;

            public Reader(byte[] b, int start, int end)
            {
                _b = b;
                _i = start;
                _end = end;
            }

            public bool Next(out int field, out int wire)
            {
                field = wire = 0;
                if (_i >= _end) return false;
                var tag = Varint();
                field = (int)(tag >> 3);
                wire = (int)(tag & 7);
                return true;
            }

            public ulong Varint()
            {
                ulong v = 0;
                for (var shift = 0; shift < 64; shift += 7)
                {
                    if (_i >= _end) throw new FormatException("ended in a varint");
                    var b = _b[_i++];
                    v |= (ulong)(b & 0x7f) << shift;
                    if ((b & 0x80) == 0) return v;
                }
                throw new FormatException("varint too long");
            }

            private int Length()
            {
                var n = Varint();
                if (n > (ulong)(_end - _i)) throw new FormatException("field runs past the end");
                return (int)n;
            }

            public Reader Message()
            {
                var n = Length();
                var m = new Reader(_b, _i, _i + n);
                _i += n;
                return m;
            }

            public string Text()
            {
                var n = Length();
                var s = Encoding.UTF8.GetString(_b, _i, n);
                _i += n;
                return s;
            }

            public void Skip(int wire)
            {
                switch (wire)
                {
                    case 0: Varint(); break;
                    case 1: Advance(8); break;
                    case 2: Advance(Length()); break;
                    case 5: Advance(4); break;
                    default: throw new FormatException("unknown wire type");
                }
            }

            private void Advance(int n)
            {
                if (n > _end - _i) throw new FormatException("field runs past the end");
                _i += n;
            }
        }
    }
}
