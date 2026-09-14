package io.github.onuragtas.openlog.it;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.net.ServerSocket;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeUnit;

/** A sample application started as a child JVM (optionally with the agent). */
public final class AppProcess implements AutoCloseable {
  public final String name;
  public final int port;
  public final Process process;
  public final List<String> output = new CopyOnWriteArrayList<>();
  private final HttpClient client =
      HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).connectTimeout(Duration.ofSeconds(5)).build();

  private AppProcess(String name, int port, Process process) {
    this.name = name;
    this.port = port;
    this.process = process;
  }

  public static Path agentJar() {
    return Paths.get(System.getProperty("openlog.agent.jar"));
  }

  public static Path appDir(String app) {
    return Paths.get(System.getProperty("openlog.app." + app));
  }

  public static int freePort() throws IOException {
    try (ServerSocket s = new ServerSocket(0)) {
      return s.getLocalPort();
    }
  }

  public static AppProcess start(
      String name, Path appDir, String mainClass, Path agentJar, Map<String, String> env, List<String> jvmArgs, boolean echo)
      throws IOException {
    return launch(name, List.of("-cp", appDir.resolve("lib") + "/*", mainClass), agentJar, env, jvmArgs, echo);
  }

  /** Starts {@code java -jar jar} (e.g. Quarkus' quarkus-run.jar). */
  public static AppProcess startJar(String name, Path jar, Path agentJar, Map<String, String> env, List<String> jvmArgs, boolean echo)
      throws IOException {
    if (!Files.isRegularFile(jar)) {
      throw new IOException("application jar not found: " + jar);
    }
    return launch(name, List.of("-jar", jar.toString()), agentJar, env, jvmArgs, echo);
  }

  private static AppProcess launch(
      String name, List<String> appArgs, Path agentJar, Map<String, String> env, List<String> jvmArgs, boolean echo)
      throws IOException {
    int port = freePort();
    List<String> cmd = new ArrayList<>();
    cmd.add(Paths.get(System.getProperty("java.home"), "bin", "java").toString());
    cmd.add("-Xmx512m");
    if (agentJar != null) {
      if (!Files.isRegularFile(agentJar)) {
        throw new IOException("agent jar not found: " + agentJar);
      }
      cmd.add("-javaagent:" + agentJar);
    }
    cmd.addAll(jvmArgs);
    cmd.addAll(appArgs);
    ProcessBuilder pb = new ProcessBuilder(cmd).redirectErrorStream(true);
    pb.environment().putAll(env);
    pb.environment().put("APP_PORT", Integer.toString(port));
    Process p = pb.start();
    AppProcess app = new AppProcess(name, port, p);
    Thread reader =
        new Thread(
            () -> {
              try (BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
                String line;
                while ((line = r.readLine()) != null) {
                  app.output.add(line);
                  if (echo || line.contains("[openlog]") || line.contains(" ERROR ") || line.startsWith("Exception")) {
                    System.out.println("[" + name + "] " + line);
                  }
                }
              } catch (IOException ignored) {
                // process ended
              }
            },
            name + "-output");
    reader.setDaemon(true);
    reader.start();
    return app;
  }

  public void waitHealthy(Duration timeout) throws Exception {
    long deadline = System.nanoTime() + timeout.toNanos();
    while (System.nanoTime() < deadline) {
      if (!process.isAlive()) {
        dumpOutput(200);
        throw new IllegalStateException(name + " exited with " + process.exitValue());
      }
      try {
        if (get("/health") == 200) {
          return;
        }
      } catch (IOException ignored) {
        // not listening yet
      }
      Thread.sleep(250);
    }
    dumpOutput(200);
    throw new IllegalStateException(name + " not healthy after " + timeout);
  }

  public void dumpOutput(int lines) {
    List<String> out = new ArrayList<>(output);
    for (String l : out.subList(Math.max(0, out.size() - lines), out.size())) {
      System.out.println("[" + name + "] " + l);
    }
  }

  public int get(String path, String... headers) throws IOException, InterruptedException {
    HttpRequest.Builder b = HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + port + path)).timeout(Duration.ofSeconds(60)).GET();
    if (headers.length > 0) {
      b.headers(headers);
    }
    return client.send(b.build(), HttpResponse.BodyHandlers.discarding()).statusCode();
  }

  public int post(String path) throws IOException, InterruptedException {
    HttpRequest req =
        HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + port + path))
            .timeout(Duration.ofSeconds(60))
            .POST(HttpRequest.BodyPublishers.noBody())
            .build();
    return client.send(req, HttpResponse.BodyHandlers.discarding()).statusCode();
  }

  /** Resident set size in MiB (Linux), -1 elsewhere. */
  public long rssMiB() {
    try {
      for (String line : Files.readAllLines(Paths.get("/proc/" + process.pid() + "/status"))) {
        if (line.startsWith("VmRSS:")) {
          return Long.parseLong(line.replaceAll("[^0-9]", "")) / 1024;
        }
      }
    } catch (IOException | RuntimeException ignored) {
      // not Linux
    }
    return -1;
  }

  @Override
  public void close() throws InterruptedException {
    process.destroy();
    if (!process.waitFor(30, TimeUnit.SECONDS)) {
      process.destroyForcibly().waitFor(10, TimeUnit.SECONDS);
    }
  }
}
