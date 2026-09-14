package io.github.onuragtas.openlog.it;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.HashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

final class TestEnv {
  static final String LICENSE_KEY = "test-license-key";
  static final String HOST_ID = "java-it-host-0001";

  private TestEnv() {}

  /** Agent settings shared by the integration tests: capture endpoint, fast exports, a fake infra agent host id. */
  static Map<String, String> agent(OtlpCapture capture, String service, Path infraRuntimeDir) throws IOException {
    Files.writeString(infraRuntimeDir.resolve("host-id"), HOST_ID + "\n");
    Map<String, String> env = new HashMap<>();
    env.put("OPENLOG_ENDPOINT", capture.endpoint());
    env.put("OPENLOG_LICENSE_KEY", LICENSE_KEY);
    env.put("OPENLOG_SERVICE_NAME", service);
    env.put("OPENLOG_SERVICE_VERSION", "1.2.3");
    env.put("OPENLOG_ENVIRONMENT", "test");
    env.put("OPENLOG_INFRA_RUNTIME_DIR", infraRuntimeDir.toString());
    env.put("OPENLOG_METRIC_EXPORT_INTERVAL", "2s");
    env.put("OPENLOG_LOG_LEVEL", "info");
    env.put("OTEL_BSP_SCHEDULE_DELAY", "200");
    env.put("OTEL_BLRP_SCHEDULE_DELAY", "200");
    return env;
  }

  /** The container id this JVM (and its child processes) runs in, like the agent detects it; null outside containers. */
  static String expectedContainerId() {
    Pattern cg = Pattern.compile("([0-9a-f]{64})(?:\\.scope)?$");
    Pattern mi = Pattern.compile("containers/([0-9a-f]{64})/");
    try {
      for (String line : Files.readAllLines(Path.of("/proc/self/cgroup"))) {
        String[] parts = line.split(":", 3);
        if (parts.length < 3) {
          continue;
        }
        String[] segs = parts[2].split("/");
        for (int i = segs.length - 1; i >= 0; i--) {
          Matcher m = cg.matcher(segs[i]);
          if (m.find()) {
            return m.group(1);
          }
        }
      }
      for (String line : Files.readAllLines(Path.of("/proc/self/mountinfo"))) {
        if (line.contains("/sandboxes/")) {
          continue;
        }
        Matcher m = mi.matcher(line);
        if (m.find()) {
          return m.group(1);
        }
      }
    } catch (IOException ignored) {
      // not Linux
    }
    return null;
  }
}
