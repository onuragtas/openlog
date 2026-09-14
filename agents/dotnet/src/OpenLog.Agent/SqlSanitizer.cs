using System.Collections.Generic;
using System.Text;

namespace OpenLog.Agent;

/// <summary>
/// DB statement sanitization, a port of the Go agent's openlogsql.Sanitize (agents/go/openlogsql) and the Node.js
/// agent's sanitize.ts, so every openlog agent sends the same db.query.text for the same statement (apm.md §7
/// normalizes again on the backend). Shared test cases: agents/dotnet/test/OpenLog.Agent.Tests/SqlSanitizerTests.cs.
/// </summary>
public static class SqlSanitizer
{
    /// <summary>Upper bound of db.query.text in UTF-16 code units (statements are ASCII in practice), like the Go agent.</summary>
    public const int MaxQueryText = 4096;

    private static bool IsDigit(char c) => c >= '0' && c <= '9';
    private static bool IsHex(char c) => IsDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');
    private static bool IsIdentStart(char c) => c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z');
    private static bool IsIdent(char c) => IsIdentStart(c) || IsDigit(c) || c == '$';
    private static bool IsNonAscii(char c) => c >= 128;

    private static bool IdentBefore(string q, int i)
    {
        if (i == 0) return false;
        var p = q[i - 1];
        return IsIdent(p) || IsNonAscii(p);
    }

    /// <summary>
    /// Normalizes a SQL statement so it carries no literal values: string literals ('…', E'…', and "…" for MySQL) → ?;
    /// numeric and hex literals → ? (identifiers such as t1 or col_2 are kept); IN (?, ?) and VALUES (?), (?) lists
    /// collapse to (?); comments are removed and whitespace is collapsed. Placeholders ($1, ?, :name, @p1) are kept.
    /// <paramref name="system"/> is the db.system.name value; it only changes how double-quoted text (identifier except
    /// for mysql) and <c>#</c> comments (mysql) are treated.
    /// </summary>
    public static string Sanitize(string q, string system = "")
    {
        var mysql = system == "mysql" || system == "mariadb";
        var out_ = new StringBuilder(q.Length);
        var space = false;
        void Emit(string s)
        {
            if (space && out_.Length > 0) out_.Append(' ');
            space = false;
            out_.Append(s);
        }
        var n = q.Length;
        var i = 0;
        while (i < n)
        {
            var c = q[i];
            var next = i + 1 < n ? q[i + 1] : '\0';
            var hasNext = i + 1 < n;
            if (c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f')
            {
                space = true;
                i++;
            }
            else if (c == '-' && next == '-' && hasNext)
            {
                while (i < n && q[i] != '\n') i++;
                space = true;
            }
            else if (c == '#' && mysql)
            {
                while (i < n && q[i] != '\n') i++;
                space = true;
            }
            else if (c == '/' && hasNext && next == '*')
            {
                var j = q.IndexOf("*/", i + 2, System.StringComparison.Ordinal);
                i = j < 0 ? n : j + 2;
                space = true;
            }
            else if (c == '\'' || (c == '"' && mysql) || ("EeNnXxBb".IndexOf(c) >= 0 && hasNext && next == '\'' && !IdentBefore(q, i)))
            {
                var prefix = c != '\'' && c != '"' ? c : '\0';
                if (prefix != '\0') i++;
                var quote = q[i];
                i++;
                while (i < n)
                {
                    var d = q[i];
                    if (d == '\\' && i + 1 < n && quote == '\'' && (prefix == 'E' || prefix == 'e' || mysql))
                    {
                        i += 2;
                        continue;
                    }
                    if (d == quote)
                    {
                        if (i + 1 < n && q[i + 1] == quote)
                        {
                            i += 2;
                            continue;
                        }
                        i++;
                        break;
                    }
                    i++;
                }
                Emit("?");
            }
            else if (c == '$' && hasNext && next == '$')
            {
                var j = q.IndexOf("$$", i + 2, System.StringComparison.Ordinal);
                i = j < 0 ? n : j + 2;
                Emit("?");
            }
            else if (c == '"' || c == '`' || c == '[')
            {
                var closer = c == '[' ? ']' : c;
                var j = q.IndexOf(closer, i + 1);
                if (j < 0)
                {
                    Emit(q.Substring(i));
                    i = n;
                }
                else
                {
                    Emit(q.Substring(i, j + 1 - i));
                    i = j + 1;
                }
            }
            else if (IsDigit(c) || (c == '.' && hasNext && IsDigit(next)))
            {
                if (IdentBefore(q, i))
                {
                    Emit(c.ToString());
                    i++;
                    continue;
                }
                if (c == '0' && hasNext && (next == 'x' || next == 'X'))
                {
                    i += 2;
                    while (i < n && IsHex(q[i])) i++;
                }
                else
                {
                    while (i < n && (IsDigit(q[i]) || q[i] == '.')) i++;
                    if (i < n && (q[i] == 'e' || q[i] == 'E'))
                    {
                        var k = i + 1;
                        if (k < n && (q[k] == '+' || q[k] == '-')) k++;
                        if (k < n && IsDigit(q[k]))
                        {
                            i = k;
                            while (i < n && IsDigit(q[i])) i++;
                        }
                    }
                }
                Emit("?");
            }
            else if ((c == '$' || c == '@' || c == ':') && hasNext && (IsDigit(next) || IsIdentStart(next)) && !(c == ':' && i > 0 && q[i - 1] == ':'))
            {
                var j = i + 1;
                while (j < n && IsIdent(q[j])) j++;
                Emit(q.Substring(i, j - i));
                i = j;
            }
            else if (IsIdentStart(c) || IsNonAscii(c))
            {
                var j = i + 1;
                while (j < n && (IsIdent(q[j]) || IsNonAscii(q[j]))) j++;
                Emit(q.Substring(i, j - i));
                i = j;
            }
            else
            {
                if (c == ',' || c == ')') space = false;
                Emit(c.ToString());
                if (c == '(') space = false;
                i++;
            }
        }
        return CollapseLists(out_.ToString());
    }

    /// <summary>"(?, ?, ?)" → "(?)" and "VALUES (?), (?)" → "VALUES (?)".</summary>
    private static string CollapseLists(string s)
    {
        if (s.IndexOf("?,", System.StringComparison.Ordinal) < 0) return s;
        var b = new StringBuilder(s.Length);
        var i = 0;
        while (i < s.Length)
        {
            if (s[i] == '(')
            {
                var j = i + 1;
                var ok = false;
                while (j < s.Length)
                {
                    if (s[j] == '?')
                    {
                        j++;
                        ok = true;
                        if (j < s.Length && s[j] == ',')
                        {
                            j++;
                            if (j < s.Length && s[j] == ' ') j++;
                            continue;
                        }
                    }
                    break;
                }
                if (ok && j < s.Length && s[j] == ')')
                {
                    b.Append("(?)");
                    i = j + 1;
                    while (true)
                    {
                        var k = i;
                        if (k < s.Length && s[k] == ',')
                        {
                            k++;
                            if (k < s.Length && s[k] == ' ') k++;
                            if (k < s.Length && s[k] == '(')
                            {
                                var m = k + 1;
                                while (m < s.Length && (s[m] == '?' || s[m] == ',' || s[m] == ' ')) m++;
                                if (m < s.Length && s[m] == ')' && m > k + 1)
                                {
                                    i = m + 1;
                                    continue;
                                }
                            }
                        }
                        break;
                    }
                    continue;
                }
            }
            b.Append(s[i]);
            i++;
        }
        return b.ToString();
    }

    /// <summary>
    /// Key/value store statement (redis, memcached): the command word upper-cased and every argument replaced by
    /// <c>?</c> (apm.md §7: <c>GET products:42</c> → <c>GET ?</c>). Long argument lists keep at most 32 placeholders plus <c>…</c>.
    /// </summary>
    public static string SanitizeKeyValue(string command, IReadOnlyList<string>? args = null)
    {
        var sb = new StringBuilder(command.ToUpperInvariant());
        var count = args?.Count ?? 0;
        var shown = count < 32 ? count : 32;
        for (var k = 0; k < shown; k++) sb.Append(" ?");
        if (count > shown) sb.Append(" …");
        return sb.ToString();
    }

    /// <summary>Truncates to <see cref="MaxQueryText"/> without splitting a surrogate pair.</summary>
    public static string Truncate(string s, int max = MaxQueryText)
    {
        if (s.Length <= max) return s;
        var end = max;
        if (char.IsHighSurrogate(s[end - 1])) end--;
        return s.Substring(0, end);
    }
}
