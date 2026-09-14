using System.Linq;
using OpenLog.Agent;
using Xunit;

namespace OpenLog.Agent.Tests;

public class SqlSanitizerTests
{
    // Same cases as agents/go/openlogsql/sql_test.go TestSanitize and agents/node/test/unit/sanitize.test.ts:
    // every openlog agent must send identical db.query.text.
    public static readonly TheoryData<string, string, string> GoCases = new()
    {
        { "SELECT * FROM users WHERE email = 'a@b.c' AND age > 30", "postgresql", "SELECT * FROM users WHERE email = ? AND age > ?" },
        { "select  *\n from t1 where col_2 = 3.14e-2 -- trailing comment", "postgresql", "select * from t1 where col_2 = ?" },
        { "/* app=shop */ UPDATE t SET a = 'it''s', b = E'x\\'y', c = 0xFF WHERE id IN (1,2, 3)", "postgresql", "UPDATE t SET a = ?, b = ?, c = ? WHERE id IN (?)" },
        { "INSERT INTO t (\"from\", note) VALUES (1, 'a'), (2, 'b'), (3, 'c')", "postgresql", "INSERT INTO t (\"from\", note) VALUES (?)" },
        { "SELECT * FROM t WHERE name = \"bob\" # mysql comment", "mysql", "SELECT * FROM t WHERE name = ?" },
        { "SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2", "mysql", "SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2" },
        { "SELECT a::text, $$secret$$ FROM t", "postgresql", "SELECT a::text, ? FROM t" },
        { "SELECT -5, x-1 FROM t", "postgresql", "SELECT -?, x-? FROM t" },
        { "SELECT 'unterminated", "postgresql", "SELECT ?" },
        { "SELECT 'ünïcödé' AS naïve FROM tåble", "postgresql", "SELECT ? AS naïve FROM tåble" },
    };

    [Theory]
    [MemberData(nameof(GoCases))]
    public void MatchesTheGoAgent(string input, string system, string want)
    {
        Assert.Equal(want, SqlSanitizer.Sanitize(input, system));
    }

    [Theory]
    [MemberData(nameof(GoCases))]
    public void IsIdempotent(string input, string system, string _)
    {
        var once = SqlSanitizer.Sanitize(input, system);
        Assert.Equal(once, SqlSanitizer.Sanitize(once, system));
    }

    [Theory]
    [InlineData("SELECT * FROM \"users\" WHERE \"id\" = $1 AND flag = true", "postgresql", "SELECT * FROM \"users\" WHERE \"id\" = $1 AND flag = true")]
    [InlineData("SELECT x'DEADBEEF', b'0101', N'name'", "mssql", "SELECT ?, ?, ?")]
    [InlineData("SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)", "postgresql", "SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)")]
    [InlineData("SELECT * FROM t WHERE id IN (?, ?, ?)", "mysql", "SELECT * FROM t WHERE id IN (?)")]
    [InlineData("INSERT INTO t VALUES ('a\\'b', 2)", "mysql", "INSERT INTO t VALUES (?)")]
    [InlineData("SELECT [order] FROM [dbo].[t] WHERE a = 1.5", "mssql", "SELECT [order] FROM [dbo].[t] WHERE a = ?")]
    // EF Core style statements (parameters are kept, inline constants are not)
    [InlineData("SELECT p.\"Id\", p.\"Name\" FROM \"Products\" AS p WHERE p.\"Price\" > 10.5 LIMIT @__p_0", "postgresql", "SELECT p.\"Id\", p.\"Name\" FROM \"Products\" AS p WHERE p.\"Price\" > ? LIMIT @__p_0")]
    public void ExtraCases(string input, string system, string want)
    {
        Assert.Equal(want, SqlSanitizer.Sanitize(input, system));
    }

    [Fact]
    public void KeyValue()
    {
        Assert.Equal("GET ?", SqlSanitizer.SanitizeKeyValue("get", new[] { "products:42" }));
        Assert.Equal("HSET ? ? ?", SqlSanitizer.SanitizeKeyValue("HSET", new[] { "k", "f", "v" }));
        Assert.Equal("PING", SqlSanitizer.SanitizeKeyValue("ping"));
        var many = SqlSanitizer.SanitizeKeyValue("MSET", Enumerable.Repeat("x", 100).ToList());
        Assert.Equal(1 + 32 + 1, many.Split(' ').Length);
        Assert.EndsWith(" …", many);
    }

    [Fact]
    public void TruncateKeepsSurrogatePairs()
    {
        Assert.Equal("abc", SqlSanitizer.Truncate("abc", 10));
        var s = new string('a', SqlSanitizer.MaxQueryText - 1) + "😀";
        Assert.Equal(SqlSanitizer.MaxQueryText - 1, SqlSanitizer.Truncate(s).Length);
    }
}
