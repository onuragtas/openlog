/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent.resource;

import io.github.onuragtas.openlog.javaagent.Diag;
import io.github.onuragtas.openlog.javaagent.OpenlogSettings;
import io.github.onuragtas.openlog.javaagent.OpenlogVersion;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.AttributesBuilder;
import io.opentelemetry.sdk.resources.Resource;
import java.io.BufferedReader;
import java.io.File;
import java.io.IOException;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardCopyOption;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.TimeUnit;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Resource detection with the Go and Node.js agents' rules: the infra agent's host.id chain (semantic-conventions.md
 * §1), container.id from cgroup/mountinfo, OS and Kubernetes attributes, the openlog distro name. Command-line
 * arguments are removed from the upstream process resource (they may contain secrets).
 */
public final class OpenlogResource {
  /** The infra agent's validity rule (agents/infra/internal/resource). */
  static final Pattern VALID_HOST_ID = Pattern.compile("[0-9A-Za-z-]{8,}");

  /** The infra agent's resolution chain. Keep in sync with agents/go/resource.go and agents/node/src/resource.ts. */
  static final String[] HOST_ID_FILES = {
    "/etc/machine-id", "/var/lib/dbus/machine-id", "/sys/class/dmi/id/product_uuid"
  };

  private static final Pattern CONTAINER_ID_IN_CGROUP = Pattern.compile("([0-9a-f]{64})(?:\\.scope)?$");
  private static final Pattern CONTAINER_ID_IN_MOUNTINFO = Pattern.compile("containers/([0-9a-f]{64})/");

  private OpenlogResource() {}

  /** Reads host files below a root prefix (tests use a temp dir; containers may mount the host root at /host). */
  public static final class HostFs {
    final String root;

    public HostFs(String root) {
      this.root = root == null || root.isEmpty() ? "/" : root;
    }

    String path(String p) {
      String clean = normalize("/" + p);
      if (root.equals("/")) {
        return clean;
      }
      String r = root.endsWith("/") ? root.substring(0, root.length() - 1) : root;
      return r + clean;
    }

    String read(String p) {
      try {
        return new String(Files.readAllBytes(Paths.get(path(p))), StandardCharsets.UTF_8);
      } catch (IOException | RuntimeException e) {
        return null;
      }
    }

    String readTrim(String p) {
      String s = read(p);
      return s == null ? "" : s.trim();
    }
  }

  static String normalize(String p) {
    String[] parts = p.split("/");
    java.util.ArrayDeque<String> stack = new java.util.ArrayDeque<>();
    for (String s : parts) {
      if (s.isEmpty() || s.equals(".")) {
        continue;
      }
      if (s.equals("..")) {
        stack.pollLast();
      } else {
        stack.addLast(s);
      }
    }
    StringBuilder b = new StringBuilder();
    for (String s : stack) {
      b.append('/').append(s);
    }
    return b.length() == 0 ? "/" : b.toString();
  }

  /** Platform machine id on non-Linux systems (macOS IOPlatformUUID, Windows MachineGuid). */
  interface Platform {
    String hostId();
  }

  static Platform platform =
      new Platform() {
        @Override
        public String hostId() {
          String os = System.getProperty("os.name", "").toLowerCase(Locale.ROOT);
          try {
            if (os.contains("mac")) {
              String out = exec("ioreg", "-rd1", "-c", "IOPlatformExpertDevice");
              Matcher m = Pattern.compile("\"IOPlatformUUID\"\\s*=\\s*\"([^\"]+)\"").matcher(out);
              return m.find() ? m.group(1).toLowerCase(Locale.ROOT) : "";
            }
            if (os.contains("windows")) {
              String out =
                  exec("reg", "query", "HKEY_LOCAL_MACHINE\\SOFTWARE\\Microsoft\\Cryptography", "/v", "MachineGuid");
              Matcher m = Pattern.compile("MachineGuid\\s+REG_SZ\\s+(\\S+)").matcher(out);
              return m.find() ? m.group(1).toLowerCase(Locale.ROOT) : "";
            }
          } catch (Exception ignored) {
            // not available
          }
          return "";
        }
      };

  static String exec(String... cmd) throws IOException, InterruptedException {
    Process p = new ProcessBuilder(cmd).redirectErrorStream(true).start();
    StringBuilder b = new StringBuilder();
    try (BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
      String line;
      while ((line = r.readLine()) != null && b.length() < 1 << 16) {
        b.append(line).append('\n');
      }
    }
    if (!p.waitFor(2, TimeUnit.SECONDS)) {
      p.destroyForcibly();
    }
    return b.toString();
  }

  static String userCacheDir() {
    String home = System.getProperty("user.home", "");
    String os = System.getProperty("os.name", "").toLowerCase(Locale.ROOT);
    if (os.contains("mac")) {
      return home + File.separator + "Library" + File.separator + "Caches";
    }
    if (os.contains("windows")) {
      String v = System.getenv("LOCALAPPDATA");
      return v != null && !v.isEmpty() ? v : home + File.separator + "AppData" + File.separator + "Local";
    }
    String xdg = System.getenv("XDG_CACHE_HOME");
    return xdg != null && !xdg.isEmpty() ? xdg : home + File.separator + ".cache";
  }

  /**
   * Resolves host.id like the infra agent, so both report the same id on one machine; returns {id, source}:
   *
   * <ol>
   *   <li>the id a running infra agent published in &lt;infraRuntimeDir&gt;/host-id (under the host root, else the
   *       plain path)
   *   <li>/etc/machine-id → /var/lib/dbus/machine-id → /sys/class/dmi/id/product_uuid (valid, not all zeros;
   *       lower-cased)
   *   <li>the UUID the infra agent generated in its state dir (&lt;infraStateDir&gt;/host-id)
   *   <li>non-Linux: the platform machine id
   *   <li>a UUID generated and persisted by this agent (stateDir, default &lt;user cache dir&gt;/openlog/host-id)
   * </ol>
   */
  public static String[] resolveHostId(
      HostFs hfs, String infraRuntimeDir, String infraStateDir, String stateDir, boolean linux) {
    if (infraRuntimeDir != null && !infraRuntimeDir.isEmpty()) {
      String file = normalize(infraRuntimeDir + "/host-id");
      String v = hfs.readTrim(file);
      if (!VALID_HOST_ID.matcher(v).matches() && !hfs.path(file).equals(file)) {
        v = new HostFs("/").readTrim(file);
      }
      if (VALID_HOST_ID.matcher(v).matches()) {
        return new String[] {v, "infra-agent"};
      }
    }
    for (String p : HOST_ID_FILES) {
      String v = hfs.readTrim(p);
      if (VALID_HOST_ID.matcher(v).matches() && !v.replaceAll("[0-]", "").isEmpty()) {
        return new String[] {v.toLowerCase(Locale.ROOT), p};
      }
    }
    if (infraStateDir != null && !infraStateDir.isEmpty()) {
      String v = hfs.readTrim(normalize(infraStateDir + "/host-id"));
      if (VALID_HOST_ID.matcher(v).matches()) {
        return new String[] {v, "infra-agent-state"};
      }
    }
    if (!linux) {
      String v = platform.hostId();
      if (v != null && !v.isEmpty()) {
        return new String[] {v, "platform"};
      }
    }
    String dir = stateDir != null && !stateDir.isEmpty() ? stateDir : userCacheDir() + File.separator + "openlog";
    Path file = Paths.get(dir, "host-id");
    try {
      String v = new String(Files.readAllBytes(file), StandardCharsets.UTF_8).trim();
      if (VALID_HOST_ID.matcher(v).matches()) {
        return new String[] {v, "generated"};
      }
    } catch (IOException | RuntimeException ignored) {
      // generate below
    }
    String gen = UUID.randomUUID().toString();
    try {
      Files.createDirectories(file.getParent());
      Path tmp = file.resolveSibling("host-id." + UUID.randomUUID() + ".tmp");
      Files.write(tmp, (gen + "\n").getBytes(StandardCharsets.UTF_8));
      try {
        Files.move(tmp, file, StandardCopyOption.ATOMIC_MOVE);
      } catch (IOException e) {
        Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING);
      }
    } catch (IOException | RuntimeException e) {
      return new String[] {"", ""};
    }
    return new String[] {gen, "generated"};
  }

  /**
   * The id of the container this process runs in: the last 64-hex segment of /proc/self/cgroup (cgroup v1, or v2
   * without a cgroup namespace), otherwise the Docker/Podman container directory in /proc/self/mountinfo (cgroup v2
   * with a private cgroup namespace, where /proc/self/cgroup is just "0::/").
   */
  public static String containerId(HostFs hfs) {
    String cgroup = hfs.read("/proc/self/cgroup");
    if (cgroup != null) {
      for (String line : cgroup.split("\n")) {
        String[] parts = line.split(":", 3);
        if (parts.length < 3) {
          continue;
        }
        String[] segs = parts[2].split("/");
        for (int i = segs.length - 1; i >= 0; i--) {
          Matcher m = CONTAINER_ID_IN_CGROUP.matcher(segs[i]);
          if (m.find()) {
            return m.group(1);
          }
        }
      }
    }
    String mountinfo = hfs.read("/proc/self/mountinfo");
    if (mountinfo != null) {
      for (String line : mountinfo.split("\n")) {
        if (line.contains("/sandboxes/")) {
          continue; // containerd pod sandbox, not this container
        }
        Matcher m = CONTAINER_ID_IN_MOUNTINFO.matcher(line);
        if (m.find()) {
          return m.group(1);
        }
      }
    }
    return "";
  }

  /** Maps kernel/JVM architecture names to OTel host.arch values (same as the infra, Go and Node.js agents). */
  public static String normalizeArch(String a) {
    switch (a) {
      case "x86_64":
      case "amd64":
      case "x64":
        return "amd64";
      case "aarch64":
      case "arm64":
        return "arm64";
      case "i386":
      case "i686":
      case "386":
      case "x86":
      case "ia32":
        return "x86";
      case "armv7l":
      case "armv6l":
      case "arm":
        return "arm32";
      case "ppc64le":
      case "ppc64":
        return "ppc64";
      case "s390x":
        return "s390x";
      default:
        return a;
    }
  }

  static Map<String, String> parseOsRelease(String s) {
    Map<String, String> out = new LinkedHashMap<>();
    for (String raw : s.split("\n")) {
      String line = raw.trim();
      if (line.isEmpty() || line.startsWith("#")) {
        continue;
      }
      int i = line.indexOf('=');
      if (i < 0) {
        continue;
      }
      String v = line.substring(i + 1);
      if (v.length() >= 2
          && ((v.startsWith("\"") && v.endsWith("\"")) || (v.startsWith("'") && v.endsWith("'")))) {
        v = v.substring(1, v.length() - 1).replaceAll("\\\\([\"'\\\\$`])", "$1");
      }
      out.put(line.substring(0, i), v);
    }
    return out;
  }

  /** Kubernetes metadata from downward-API environment variables (inside a pod with fallbacks). */
  static Map<String, String> k8sAttributes(HostFs hfs, OpenlogSettings s) {
    Map<String, String> out = new LinkedHashMap<>();
    put(out, "k8s.pod.name", first(s, "K8S_POD_NAME", "POD_NAME"));
    put(out, "k8s.pod.uid", first(s, "K8S_POD_UID", "POD_UID"));
    put(out, "k8s.namespace.name", first(s, "K8S_NAMESPACE_NAME", "K8S_NAMESPACE", "POD_NAMESPACE"));
    put(out, "k8s.node.name", first(s, "K8S_NODE_NAME", "NODE_NAME"));
    put(out, "k8s.container.name", first(s, "K8S_CONTAINER_NAME", "CONTAINER_NAME"));
    put(out, "k8s.deployment.name", first(s, "K8S_DEPLOYMENT_NAME"));
    put(out, "k8s.cluster.name", first(s, "K8S_CLUSTER_NAME"));
    if (s.hasEnv("KUBERNETES_SERVICE_HOST")) {
      if (!out.containsKey("k8s.namespace.name")) {
        put(out, "k8s.namespace.name", hfs.readTrim("/var/run/secrets/kubernetes.io/serviceaccount/namespace"));
      }
      if (!out.containsKey("k8s.pod.name")) {
        put(out, "k8s.pod.name", first(s, "HOSTNAME"));
      }
    }
    return out;
  }

  private static String first(OpenlogSettings s, String... names) {
    for (String n : names) {
      String v = s.env(n);
      if (v != null) {
        return v;
      }
    }
    return "";
  }

  private static void put(Map<String, String> m, String k, String v) {
    if (v != null && !v.isEmpty()) {
      m.put(k, v);
    }
  }

  static boolean isLinux() {
    return System.getProperty("os.name", "").toLowerCase(Locale.ROOT).startsWith("linux");
  }

  /**
   * Detected attributes applied over the upstream resource; keys the user set in {@code otel.resource.attributes}
   * (including OPENLOG_RESOURCE_ATTRIBUTES, OPENLOG_HOST_ID, … mapped there) are never replaced.
   */
  public static Resource customize(
      Resource resource, Map<String, String> userAttributes, OpenlogSettings s, Diag diag, boolean linux) {
    AttributesBuilder b = resource.getAttributes().toBuilder();
    // Command-line arguments are not sent, they may contain secrets (-Dpassword=…); the executable path stays.
    b.removeIf(k -> k.getKey().equals("process.command_args") || k.getKey().equals("process.command_line"));
    Map<String, String> detected = new LinkedHashMap<>();
    String hostRoot = s.get("OPENLOG_HOST_ROOT");
    HostFs hfs = new HostFs(hostRoot == null ? "/" : hostRoot);
    String arch = resource.getAttribute(AttributeKey.stringKey("host.arch"));
    if (arch != null) {
      detected.put("host.arch", normalizeArch(arch));
    }
    if (linux) {
      String hn = hfs.readTrim("/proc/sys/kernel/hostname");
      if (hn.isEmpty()) {
        hn = hfs.readTrim("/etc/hostname");
      }
      put(detected, "host.name", hn);
      String karch = hfs.readTrim("/proc/sys/kernel/arch");
      if (!karch.isEmpty()) {
        detected.put("host.arch", normalizeArch(karch));
      }
      for (String p : new String[] {"/etc/os-release", "/usr/lib/os-release"}) {
        String osr = hfs.read(p);
        if (osr != null) {
          Map<String, String> m = parseOsRelease(osr);
          put(detected, "os.name", m.get("ID"));
          put(detected, "os.version", m.get("VERSION_ID"));
          put(detected, "os.description", m.get("PRETTY_NAME"));
          break;
        }
      }
      put(detected, "openlog.os.kernel_release", hfs.readTrim("/proc/sys/kernel/osrelease"));
      put(detected, "container.id", containerId(hfs));
    }
    detected.putAll(k8sAttributes(hfs, s));
    if (!userAttributes.containsKey("host.id")) {
      String[] id =
          resolveHostId(
              hfs,
              orDefault(s.get("OPENLOG_INFRA_RUNTIME_DIR"), "/run/openlog-infra-agent"),
              orDefault(s.get("OPENLOG_INFRA_STATE_DIR"), "/var/lib/openlog-infra-agent"),
              s.get("OPENLOG_STATE_DIR"),
              linux);
      if (!id[0].isEmpty()) {
        detected.put("host.id", id[0]);
      }
      diag.debug("host.id " + id[0] + " (source " + id[1] + ")");
    }
    for (Map.Entry<String, String> e : detected.entrySet()) {
      if (!userAttributes.containsKey(e.getKey())) {
        b.put(e.getKey(), e.getValue());
      }
    }
    b.put("telemetry.distro.name", OpenlogVersion.DISTRO_NAME);
    b.put("telemetry.distro.version", OpenlogVersion.VERSION);
    return Resource.create(b.build(), resource.getSchemaUrl());
  }

  private static String orDefault(String v, String d) {
    return v == null ? d : v;
  }
}
