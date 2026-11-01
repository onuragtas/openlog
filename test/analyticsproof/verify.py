#!/usr/bin/env python3
"""End to end check of product analytics and crash reporting against a running stack, with data whose answers are known (analytics.md).

    python3 test/analyticsproof/verify.py

It creates its own keys and applications, and exits non-zero when anything fails. The Go tests build these queries and
never run them, which is how a funnel that ClickHouse refused reached a release: this is the check that runs them. The
erasure step waits for the leader's job, so a pass takes about two minutes. Point it elsewhere with OPENLOG_API and
OPENLOG_INGEST. It signs in with the owner of deploy/compose/.env.example.
"""
import http.cookiejar, json, os, sys, time, urllib.error, urllib.parse, urllib.request
from datetime import datetime, timedelta, timezone

API = os.environ.get("OPENLOG_API", "http://127.0.0.1:8080")
INGEST = os.environ.get("OPENLOG_INGEST", "http://127.0.0.1:4318")
ORIGIN = "https://shop.example"
RUN = str(int(time.time()))
jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
csrf = ""
fails = 0


def check(name, ok, detail=""):
    global fails
    print(("PASS " if ok else "FAIL ") + name + ("" if ok else f"   -> {detail}"))
    if not ok:
        fails += 1


def call(method, path, body=None, base=API, headers=None):
    h = {"Content-Type": "application/json", **(headers or {})}
    if csrf and base == API:
        h["X-CSRF-Token"] = csrf
    req = urllib.request.Request(base + path, method=method, headers=h, data=None if body is None else json.dumps(body).encode())
    try:
        with opener.open(req, timeout=30) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode(errors="replace")


def hex_id(seed, n):
    import hashlib
    return hashlib.sha256(seed.encode()).hexdigest()[:n]


def attrs(d):
    return [{"key": k, "value": {"stringValue": str(v)}} for k, v in d.items()]


def span(event, at, session, **extra):
    ns = int(at.timestamp() * 1e9)
    seed = f"{RUN}{event}{at}{session}{sorted(extra.items())}"
    return {"traceId": hex_id(seed, 32), "spanId": hex_id(seed + "s", 16), "name": event, "kind": 1,
            "startTimeUnixNano": str(ns), "endTimeUnixNano": str(ns + 1000000),
            "attributes": attrs({"openlog.rum.event": event, "session.id": hex_id(session, 32), **extra})}


def send(key, spans):
    body = {"resourceSpans": [{"resource": {"attributes": attrs({"service.version": "1.2.3"})}, "scopeSpans": [{"spans": spans}]}]}
    return call("POST", "/v1/rum", body, base=INGEST, headers={"openlog-browser-key": key, "Origin": ORIGIN})


def poll(path, ready, seconds=90):
    last = None
    for _ in range(seconds // 3):
        code, data = call("GET", path)
        last = (code, data)
        if code == 200 and ready(data):
            return data
        time.sleep(3)
    return last[1] if last else None


def q(app):
    return urllib.parse.urlencode({"app": app})


code, login = call("POST", "/api/v1/auth/login", {"email": "admin@openlog.local", "password": "openlog-dev-password"})
check("login", code == 200, login)
csrf = login.get("csrf_token", "")


def make_key(name, app, **extra):
    code, res = call("POST", "/api/v1/browser-keys", {"name": name, "service_name": app, "origins": [ORIGIN], "sample_rate": 1, **extra})
    check(f"create key {name}", code == 201, res)
    return res["browser_key"], res["key"]


APP, APP_OFF, APP_SESS = f"shop-{RUN}", f"off-{RUN}", f"sess-{RUN}"
visitors, key = make_key("visitors", APP)
check("a new key starts in visitors", visitors["analytics_mode"] == "visitors", visitors)
off, key_off = make_key("off", APP_OFF, analytics_mode="off")
sess, key_sess = make_key("sess", APP_SESS, analytics_mode="sessions")

code, res = call("PUT", f"/api/v1/browser-keys/{off['id']}", {"name": "renamed", "service_name": APP_OFF, "origins": [ORIGIN], "sample_rate": 1})
check("an update omitting the mode keeps it", code == 200 and res["analytics_mode"] == "off", res)

code, conf = call("GET", "/v1/rum/config", base=INGEST, headers={"openlog-browser-key": key, "Origin": ORIGIN})
check("config reports the mode", conf.get("analytics_mode") == "visitors", conf)

now = datetime.now(timezone.utc).replace(microsecond=0)
d0, d1, d2 = now - timedelta(minutes=1), now - timedelta(days=1), now - timedelta(days=2)
A, B, C = hex_id("a" + RUN, 32), hex_id("b" + RUN, 32), hex_id("c" + RUN, 32)

sp = []
# A: three days running, arriving from a referrer, and through the funnel.
for i, at in enumerate((d2, d1, d0)):
    extra = {"openlog.rum.visitor.id": A, "openlog.rum.route": "/home", "openlog.rum.page_view.kind": "load"}
    if i == 0:
        extra.update({"openlog.rum.referrer.host": "News.Example", "openlog.rum.utm.source": "mail"})
    sp.append(span("page_view", at, f"A{i}", **extra))
sp.append(span("custom", d2 + timedelta(seconds=5), "A0", **{"openlog.rum.visitor.id": A, "openlog.rum.custom.name": "cart", "openlog.rum.custom.param.plan": "pro"}))
sp.append(span("custom", d2 + timedelta(seconds=9), "A0", **{"openlog.rum.visitor.id": A, "openlog.rum.custom.name": "paid"}))
# The funnel across sessions: B carts today and never pays. C pays a day after carting, in another session.
sp.append(span("page_view", d0, "B0", **{"openlog.rum.visitor.id": B, "openlog.rum.route": "/home", "openlog.rum.page_view.kind": "load"}))
sp.append(span("custom", d0 + timedelta(seconds=1), "B0", **{"openlog.rum.visitor.id": B, "openlog.rum.custom.name": "cart", "openlog.rum.custom.param.plan": "free"}))
sp.append(span("page_view", d1, "C0", **{"openlog.rum.visitor.id": C, "openlog.rum.route": "/home", "openlog.rum.page_view.kind": "load"}))
sp.append(span("custom", d1 + timedelta(seconds=1), "C0", **{"openlog.rum.visitor.id": C, "openlog.rum.custom.name": "cart"}))
sp.append(span("custom", d0 - timedelta(hours=1), "C1", **{"openlog.rum.visitor.id": C, "openlog.rum.custom.name": "paid"}))
code, res = send(key, sp)
check("ingest accepts the spans", code == 200, res)

# Forged: a page claiming long-lived storage on an off key, and a visitor on a sessions key.
code, _ = send(key_off, [span("page_view", d0, "F0", **{"openlog.rum.visitor.id": A, "openlog.analytics.mode": "visitors", "openlog.rum.route": "/x"})])
check("off key ingests", code == 200)
code, _ = send(key_sess, [span("page_view", d0, "S0", **{"openlog.rum.visitor.id": A, "openlog.rum.route": "/x", "openlog.rum.utm.source": "ads"})])
check("sessions key ingests", code == 200)

activity = poll(f"/api/v1/analytics/activity?{q(APP)}", lambda d: d.get("collecting") and sum(x["dau"] for x in d["days"]) >= 5)
days = {d["date"]: d for d in activity.get("days", [])}
D = lambda t: t.strftime("%Y-%m-%d")
print("activity:", json.dumps({k: {x: v[x] for x in ("dau", "wau", "mau", "new_users")} for k, v in days.items()}))
check("DAU today is 3 (A, B, and C paying)", days.get(D(d0), {}).get("dau") == 3, days.get(D(d0)))
check("DAU yesterday is 2 (A, C)", days.get(D(d1), {}).get("dau") == 2, days.get(D(d1)))
check("DAU two days ago is 1 (A)", days.get(D(d2), {}).get("dau") == 1, days.get(D(d2)))
check("WAU today is 3", days.get(D(d0), {}).get("wau") == 3, days.get(D(d0)))
check("MAU today is 3", days.get(D(d0), {}).get("mau") == 3, days.get(D(d0)))
check("a person active three days counts once in MAU", days.get(D(d1), {}).get("mau") == 2, days.get(D(d1)))
check("new users add up to 3", sum(d["new_users"] for d in days.values()) == 3, days)
check("new users by day", days.get(D(d2), {}).get("new_users") == 1 and days.get(D(d1), {}).get("new_users") == 1 and days.get(D(d0), {}).get("new_users") == 1, days)
check("stickiness is DAU over MAU", days.get(D(d0), {}).get("stickiness") == 1, days.get(D(d0)))

_, ret = call("GET", f"/api/v1/analytics/retention?{q(APP)}&grain=day")
print("retention:", json.dumps(ret))
cohorts = {c["cohort"]: c for c in ret.get("cohorts", [])}
c2 = cohorts.get(D(d2), {})
check("cohort of A has one person", c2.get("size") == 1, c2)
check("A came back on days 1 and 2", [p["period"] for p in c2.get("periods", [])] == [0, 1, 2], c2)
check("period 0 is 100%", c2.get("periods", [{}])[0].get("rate") == 1, c2)
check("no sampling change reported", ret.get("sampling_changed") is False, ret)

_, ov = call("GET", f"/api/v1/analytics/overview?{q(APP)}")
print("overview:", json.dumps(ov.get("totals")))
check("overview page views", ov["totals"]["page_views"] == 5, ov["totals"])
check("overview events", ov["totals"]["events"] == 5, ov["totals"])

_, ev = call("GET", f"/api/v1/analytics/events?{q(APP)}")
print("events:", json.dumps(ev))
names = {e["name"]: e for e in ev["events"]}
check("cart counted 3 times by 3 people", names.get("cart", {}).get("count") == 3 and names.get("cart", {}).get("users") == 3, names)
check("paid counted 2 times", names.get("paid", {}).get("count") == 2, names)
_, ep = call("GET", f"/api/v1/analytics/events?{q(APP)}&name=cart&param=plan")
vals = {e["name"]: e["count"] for e in ep["events"]}
check("parameter breakdown", vals.get("pro") == 1 and vals.get("free") == 1, ep)

_, src = call("GET", f"/api/v1/analytics/acquisition?{q(APP)}&by=referrer")
print("acquisition:", json.dumps(src))
vals = {s["value"]: s for s in src["sources"]}
check("referrer lower-cased and attributed", vals.get("news.example", {}).get("sessions") == 1, vals)
check("the rest are direct", vals.get("", {}).get("sessions", 0) >= 3, vals)
_, src = call("GET", f"/api/v1/analytics/acquisition?{q(APP)}&by=source")
check("UTM source", any(s["value"] == "mail" for s in src["sources"]), src)

_, fu = call("GET", f"/api/v1/analytics/funnel?{q(APP)}&step=cart&step=paid&by=session")
print("funnel session:", json.dumps(fu))
check("by session: 3 cart, only A pays in the same session", [s["sessions"] for s in fu["steps"]] == [3, 1], fu)
_, fu = call("GET", f"/api/v1/analytics/funnel?{q(APP)}&step=cart&step=paid&by=user&window=72h")
print("funnel user:", json.dumps(fu))
check("by user: 3 cart, A and C pay", [s["sessions"] for s in fu["steps"]] == [3, 2], fu)

_, rf = call("GET", f"/api/v1/rum/funnel?{q(APP)}&step=cart&step=paid&from={int((now - timedelta(days=3)).timestamp() * 1000)}")
print("rum funnel:", json.dumps(rf))
check("the RUM funnel runs, within the spans", [s["sessions"] for s in rf.get("steps", [])] == [3, 1], rf)

code, oql = call("POST", "/api/v1/query", {"query": f"SELECT sum(weight) FROM AnalyticsEvent WHERE app = '{APP}' AND kind = 'custom' SINCE 3 days ago"})
print("oql:", code, json.dumps(oql)[:300])
check("OQL reads AnalyticsEvent", code == 200 and "5" in json.dumps(oql.get("results", oql)), oql)

_, a_off = call("GET", f"/api/v1/analytics/activity?{q(APP_OFF)}")
_, o_off = call("GET", f"/api/v1/analytics/overview?{q(APP_OFF)}")
check("an off key stores nothing, whatever it claims", a_off["collecting"] is False and o_off["collecting"] is False, (a_off, o_off))
o_sess = poll(f"/api/v1/analytics/overview?{q(APP_SESS)}", lambda d: d.get("collecting"), 45)
_, a_sess = call("GET", f"/api/v1/analytics/activity?{q(APP_SESS)}")
check("a sessions key stores events", o_sess.get("collecting") is True, o_sess)
check("a sessions key recognises nobody", a_sess["collecting"] is False, a_sess)

code, er = call("POST", "/api/v1/analytics/erase", {"app": APP, "visitor_id": A})
print("erase:", code, json.dumps(er))
check("erase is accepted", code == 202 and er.get("status") == "pending", er)
code, bad = call("POST", "/api/v1/analytics/erase", {"app": APP, "visitor_id": "not-hex"})
check("erase refuses a malformed id", code == 400, bad)
st = poll(f"/api/v1/analytics/erase/{er['id']}", lambda d: d.get("status") == "submitted", 120)
check("the job submits it", st.get("status") == "submitted", st)
after = poll(f"/api/v1/analytics/activity?{q(APP)}", lambda d: {x["date"]: x for x in d["days"]}.get(D(d0), {}).get("dau") == 2, 120)
got = {x["date"]: x for x in after.get("days", [])}.get(D(d0), {})
check("A is gone from DAU", got.get("dau") == 2, got)
check("A is gone from MAU", got.get("mau") == 2, got)
_, ret2 = call("GET", f"/api/v1/analytics/retention?{q(APP)}&grain=day")
check("A's cohort is gone", D(d2) not in {c["cohort"] for c in ret2["cohorts"]}, ret2)
_, ov2 = call("GET", f"/api/v1/analytics/events?{q(APP)}")
check("B and C are untouched", {e["name"]: e for e in ov2["events"]}.get("cart", {}).get("users") == 2, ov2)

# ---- crashes (mobile-crash.md)
def send_res(key, spans, version, capture):
    res = {"service.version": version}
    if capture:
        res["openlog.rum.crash.capture"] = "1"
    body = {"resourceSpans": [{"resource": {"attributes": attrs(res)}, "scopeSpans": [{"spans": spans}]}]}
    return call("POST", "/v1/rum", body, base=INGEST, headers={"openlog-browser-key": key, "Origin": ORIGIN})


def crash(at, session, kind="signal", typ="SIGSEGV", message="bad access", frame="a.b.c"):
    sp = span("error", at, session, **{"openlog.rum.crash.kind": kind, "openlog.rum.crash.id": hex_id(session + frame + RUN, 32)})
    sp["events"] = [{"name": "exception", "timeUnixNano": sp["startTimeUnixNano"], "attributes": attrs({
        "exception.type": typ, "exception.message": message, "exception.stacktrace": f"{typ}: {message}\n\tat {frame}(SourceFile:1)"})}]
    sp["status"] = {"code": 2}
    return sp


APP_CR = f"crash-{RUN}"
_, res = call("POST", "/api/v1/browser-keys", {"name": "crash", "service_name": APP_CR, "origins": [ORIGIN], "sample_rate": 1})
key_cr = res["key"]
two_days = now - timedelta(days=2)
page = {"openlog.rum.route": "/x"}
# 1.0 can report: four sessions, one crashed two days ago (and on another build's frames), one with a handled error.
code, r = send_res(key_cr, [
    span("page_view", two_days, "c1", **page), span("page_view", two_days, "c2", **page), span("page_view", two_days, "c3", **page), span("page_view", two_days, "c4", **page),
    crash(two_days + timedelta(seconds=5), "c1"),
    crash(two_days + timedelta(seconds=6), "c1", frame="x.y.z"),
    span("error", two_days + timedelta(seconds=5), "c2"),
], "1.0", True)
check("crash ingests", code == 200, r)
# 0.9 cannot: an error, and no claim about crashes.
send_res(key_cr, [span("page_view", two_days, "o1", **page), span("page_view", two_days, "o2", **page), span("error", two_days, "o1")], "0.9", False)
code, r = send_res(key_cr, [crash(now - timedelta(days=8), "late")], "1.0", True)
check("a crash past the retention is refused", "crash_too_old" in json.dumps(r), r)
send_res(key_cr, [crash(now + timedelta(days=2), "c5", typ="ClockSkew")], "1.0", True)

frm = int((now - timedelta(days=3)).timestamp() * 1000)
rel = poll(f"/api/v1/rum/releases?{q(APP_CR)}&from={frm}", lambda d: {x["version"]: x for x in d.get("releases", [])}.get("1.0", {}).get("crashed_sessions") == 2, 60)
print("releases:", json.dumps(rel.get("releases"))[:600])
v = {x["version"]: x for x in rel.get("releases", [])}
one, old = v.get("1.0", {}), v.get("0.9", {})
check("sessions per version", one.get("sessions") == 5 and old.get("sessions") == 2, v)
check("crash-free counts crashes only", one.get("session_crash_free_rate") == 3 / 5, one)
check("error-free counts handled errors too", one.get("error_free_rate") == 2 / 5, one)
check("a version that cannot report has no crash-free rate", old.get("session_crash_free_rate") is None and old.get("error_free_rate") == 0.5, old)
check("the old field keeps its old meaning", one.get("crash_free_rate") == one.get("error_free_rate"), one)

cr = poll(f"/api/v1/rum/crashes?{q(APP_CR)}&from={frm}", lambda d: len(d.get("crashes", [])) >= 2, 45)
print("crashes:", json.dumps(cr)[:500])
by = {c["type"]: c for c in cr.get("crashes", [])}
seg = by.get("SIGSEGV", {})
check("two builds of one crash are one group", seg.get("count") == 2 and seg.get("sessions") == 1, by)
check("a crash carries kind, version and stack", seg.get("kind") == "signal" and seg.get("last_version") == "1.0" and "SourceFile" in seg.get("stack", ""), seg)
skew = by.get("ClockSkew", {})
check("a crash from the future is moved to now", skew != {} and skew.get("count") == 1, by)
if skew:
    seen = datetime.fromisoformat(skew["last_seen"].replace("Z", "+00:00")[:26] + "+00:00" if "." in skew["last_seen"] else skew["last_seen"].replace("Z", "+00:00"))
    check("and is not ahead of the clock", seen <= datetime.now(timezone.utc), skew)
_, inbox = call("GET", f"/api/v1/apm/errors?service={APP_CR}&from={frm}")
check("the crash is in the error inbox", "SIGSEGV" in json.dumps(inbox), inbox)
code, oq = call("POST", "/api/v1/query", {"query": f"SELECT count(*) FROM Span WHERE service.name = '{APP_CR}' AND openlog.rum.crash.kind != '' SINCE 3 days ago"})
print("crash oql:", code, json.dumps(oq.get("rows", oq))[:200])
check("crashes are alertable through OQL", code == 200 and oq["rows"][0]["values"][0] >= 2, oq)

print(f"\n{fails} failed")
sys.exit(1 if fails else 0)
