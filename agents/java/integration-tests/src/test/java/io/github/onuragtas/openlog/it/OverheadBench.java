package io.github.onuragtas.openlog.it;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * Overhead micro-benchmark: the Spring MVC sample's /hello (one JSON route, Tomcat) saturated by a keep-alive load
 * generator in this JVM, without the agent, with the agent sampling everything and with the agent sampling nothing.
 * The agent exports to a local OTLP receiver that accepts and discards the data.
 */
public final class OverheadBench {
  record Run(double rps, double p50Ms, double p99Ms, long requests, long errors) {}

  record Scenario(String name, boolean agent, Map<String, String> env) {}

  public static void main(String[] args) throws Exception {
    int seconds = intEnv("BENCH_SECONDS", 10);
    int runs = intEnv("BENCH_RUNS", 5);
    int warmup = intEnv("BENCH_WARMUP_SECONDS", 45);
    int concurrency = intEnv("BENCH_CONCURRENCY", 16);
    List<Scenario> scenarios =
        List.of(
            new Scenario("no agent", false, Map.of()),
            new Scenario("agent, sampled (ratio 1)", true, Map.of("OPENLOG_SAMPLING_RATIO", "1")),
            new Scenario("agent, not sampled (ratio 0)", true, Map.of("OPENLOG_SAMPLING_RATIO", "0")));
    System.out.printf(Locale.ROOT, "JVM %s, %d CPUs, warm-up %d s each, %d interleaved rounds × %d s, %d connections%n",
        System.getProperty("java.version"), Runtime.getRuntime().availableProcessors(), warmup, runs, seconds, concurrency);
    // All three applications run for the whole benchmark; only one is loaded at a time, in interleaved rounds
    // (A B C, A B C, …), so host noise and drift affect every scenario alike instead of whichever ran last.
    int n = scenarios.size();
    OtlpCapture[] captures = new OtlpCapture[n];
    AppProcess[] apps = new AppProcess[n];
    Path[] infras = new Path[n];
    List<List<Run>> results = new ArrayList<>();
    try {
      for (int i = 0; i < n; i++) {
        Scenario sc = scenarios.get(i);
        captures[i] = OtlpCapture.startDiscarding();
        infras[i] = Files.createTempDirectory("bench-infra");
        Map<String, String> env = new HashMap<>(TestEnv.agent(captures[i], "bench", infras[i]));
        env.put("OPENLOG_LOG_LEVEL", "warn");
        env.put("KAFKA_ENABLED", "false");
        env.putAll(sc.env());
        apps[i] = AppProcess.start("bench-" + i, AppProcess.appDir("mvc"), "io.github.onuragtas.openlog.testapp.mvc.MvcApp",
            sc.agent() ? AppProcess.agentJar() : null, env, List.of("-Xms512m"), false);
        apps[i].waitHealthy(Duration.ofSeconds(180));
        results.add(new ArrayList<>());
      }
      for (int i = 0; i < n; i++) {
        load(apps[i].port, warmup, concurrency);
      }
      for (int round = 1; round <= runs; round++) {
        for (int i = 0; i < n; i++) {
          Run r = load(apps[i].port, seconds, concurrency);
          System.out.printf(Locale.ROOT, "  round %d %s: %.0f req/s p50 %.2f ms p99 %.2f ms (%d errors)%n",
              round, scenarios.get(i).name(), r.rps(), r.p50Ms(), r.p99Ms(), r.errors());
          results.get(i).add(r);
        }
      }
      StringBuilder table = new StringBuilder(
          "| Scenario | req/s median (min–max) | vs. no agent | p50 ms | p99 ms | RSS MiB |\n|---|---|---|---|---|---|\n");
      double base = median(results.get(0)).rps();
      boolean inconclusive = false;
      for (int i = 0; i < n; i++) {
        List<Run> rs = new ArrayList<>(results.get(i));
        rs.sort(Comparator.comparingDouble(Run::rps));
        Run med = median(rs);
        double min = rs.get(0).rps();
        double max = rs.get(rs.size() - 1).rps();
        // spread wider than 1.5× means one run says nothing about the next; an agent faster than no agent is noise
        if (max > 1.5 * min || (i > 0 && med.rps() > base * 1.05)) {
          inconclusive = true;
        }
        String delta = i == 0 ? "—" : String.format(Locale.ROOT, "%+.1f %%", (med.rps() / base - 1) * 100);
        table.append(String.format(Locale.ROOT, "| %s | %.0f (%.0f–%.0f) | %s | %.2f | %.2f | %d |%n",
            scenarios.get(i).name(), med.rps(), min, max, delta, med.p50Ms(), med.p99Ms(), apps[i].rssMiB()));
        System.out.printf("  %s: OTLP requests received %d%n", scenarios.get(i).name(), captures[i].requests.size());
      }
      System.out.println();
      System.out.print(table);
      System.out.println(inconclusive
          ? "RESULT: INCONCLUSIVE (run-to-run spread > 1.5× or agent faster than no agent: host noise dominates)"
          : "RESULT: stable");
    } finally {
      for (int i = 0; i < n; i++) {
        if (apps[i] != null) {
          apps[i].close();
        }
        if (captures[i] != null) {
          captures[i].close();
        }
        if (infras[i] != null) {
          Files.deleteIfExists(infras[i].resolve("host-id"));
          Files.deleteIfExists(infras[i]);
        }
      }
    }
  }

  static Run median(List<Run> runs) {
    List<Run> rs = new ArrayList<>(runs);
    rs.sort(Comparator.comparingDouble(Run::rps));
    return rs.get(rs.size() / 2);
  }

  static int intEnv(String name, int def) {
    String v = System.getenv(name);
    return v == null || v.isBlank() ? def : Integer.parseInt(v.trim());
  }

  static Run load(int port, int seconds, int concurrency) throws InterruptedException {
    HttpClient client = HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).build();
    HttpRequest req = HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + port + "/hello")).GET().build();
    AtomicBoolean stop = new AtomicBoolean();
    long[][] lat = new long[concurrency][];
    int[] counts = new int[concurrency];
    long[] errors = new long[concurrency];
    Thread[] threads = new Thread[concurrency];
    for (int t = 0; t < concurrency; t++) {
      final int idx = t;
      lat[t] = new long[1 << 16];
      threads[t] =
          new Thread(() -> {
            while (!stop.get()) {
              long start = System.nanoTime();
              try {
                HttpResponse<Void> r = client.send(req, HttpResponse.BodyHandlers.discarding());
                if (r.statusCode() != 200) {
                  errors[idx]++;
                }
              } catch (Exception e) {
                errors[idx]++;
              }
              long d = System.nanoTime() - start;
              if (counts[idx] == lat[idx].length) {
                lat[idx] = Arrays.copyOf(lat[idx], lat[idx].length * 2);
              }
              lat[idx][counts[idx]++] = d;
            }
          });
    }
    long begin = System.nanoTime();
    for (Thread th : threads) {
      th.start();
    }
    Thread.sleep(seconds * 1000L);
    stop.set(true);
    for (Thread th : threads) {
      th.join();
    }
    double elapsed = (System.nanoTime() - begin) / 1e9;
    int total = 0;
    long errs = 0;
    for (int t = 0; t < concurrency; t++) {
      total += counts[t];
      errs += errors[t];
    }
    long[] all = new long[total];
    int k = 0;
    for (int t = 0; t < concurrency; t++) {
      System.arraycopy(lat[t], 0, all, k, counts[t]);
      k += counts[t];
    }
    Arrays.sort(all);
    double p50 = total == 0 ? 0 : all[(int) (total * 0.50)] / 1e6;
    double p99 = total == 0 ? 0 : all[Math.min(total - 1, (int) (total * 0.99))] / 1e6;
    return new Run(total / elapsed, p50, p99, total, errs);
  }
}
