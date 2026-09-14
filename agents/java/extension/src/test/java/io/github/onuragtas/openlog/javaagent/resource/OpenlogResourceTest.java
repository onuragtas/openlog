package io.github.onuragtas.openlog.javaagent.resource;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.onuragtas.openlog.javaagent.Diag;
import io.github.onuragtas.openlog.javaagent.OpenlogSettings;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.sdk.resources.Resource;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class OpenlogResourceTest {
  static final String CID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

  @TempDir Path root;

  void write(String p, String content) throws IOException {
    Path f = root.resolve(p.substring(1));
    Files.createDirectories(f.getParent());
    Files.write(f, content.getBytes(StandardCharsets.UTF_8));
  }

  OpenlogResource.HostFs hfs() {
    return new OpenlogResource.HostFs(root.toString());
  }

  @Test
  void hostIdChain() throws IOException {
    Path state = root.resolve("own-state");
    // 4. generated and persisted (linux, nothing else)
    String[] gen = OpenlogResource.resolveHostId(hfs(), "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state.toString(), true);
    assertEquals("generated", gen[1]);
    assertEquals(gen[0], OpenlogResource.resolveHostId(hfs(), "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state.toString(), true)[0]);
    // 3. infra agent state
    write("/var/lib/openlog-infra-agent/host-id", "11111111-2222-3333-4444-555555555555\n");
    assertEquals("infra-agent-state", OpenlogResource.resolveHostId(hfs(), "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state.toString(), true)[1]);
    // all-zero machine id is invalid; product uuid is lower-cased
    write("/etc/machine-id", "00000000000000000000000000000000\n");
    write("/sys/class/dmi/id/product_uuid", "ABCDEF01-2345-6789-ABCD-EF0123456789\n");
    String[] dmi = OpenlogResource.resolveHostId(hfs(), "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state.toString(), true);
    assertEquals("abcdef01-2345-6789-abcd-ef0123456789", dmi[0]);
    assertEquals("/sys/class/dmi/id/product_uuid", dmi[1]);
    // 0. the id published by a running infra agent wins
    write("/run/openlog-infra-agent/host-id", "Infra-Published-Id-1\n");
    assertEquals("Infra-Published-Id-1", OpenlogResource.resolveHostId(hfs(), "/run/openlog-infra-agent", "/var/lib/openlog-infra-agent", state.toString(), true)[0]);
  }

  @Test
  void containerIdFromCgroupAndMountinfo() throws IOException {
    assertEquals("", OpenlogResource.containerId(hfs()));
    write("/proc/self/mountinfo", "1 2 0:1 / / rw - overlay overlay rw\n"
        + "3 4 0:5 /var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/" + CID.replace('0', 'f') + "/hostname /etc/hostname rw\n"
        + "5 6 0:7 /var/lib/docker/containers/" + CID + "/hostname /etc/hostname rw\n");
    assertEquals(CID, OpenlogResource.containerId(hfs()));
    String v1 = CID.replace('1', 'e');
    write("/proc/self/cgroup", "12:memory:/docker/" + v1 + "\n0::/\n");
    assertEquals(v1, OpenlogResource.containerId(hfs()));
    write("/proc/self/cgroup", "0::/system.slice/docker-" + v1 + ".scope\n");
    assertEquals(v1, OpenlogResource.containerId(hfs()));
  }

  @Test
  void archAndOsRelease() {
    assertEquals("amd64", OpenlogResource.normalizeArch("x86_64"));
    assertEquals("arm64", OpenlogResource.normalizeArch("aarch64"));
    assertEquals("x86", OpenlogResource.normalizeArch("x86"));
    assertEquals("riscv64", OpenlogResource.normalizeArch("riscv64"));
    Map<String, String> m = OpenlogResource.parseOsRelease("# c\nID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n");
    assertEquals("debian", m.get("ID"));
    assertEquals("12", m.get("VERSION_ID"));
    assertEquals("Debian GNU/Linux 12 (bookworm)", m.get("PRETTY_NAME"));
  }

  @Test
  void customizeRemovesArgumentsAndAddsOpenlogAttributes() throws IOException {
    write("/run/openlog-infra-agent/host-id", "published-host-0001\n");
    write("/proc/self/cgroup", "0::/docker/" + CID + "\n");
    write("/etc/os-release", "ID=alpine\nVERSION_ID=3.20.1\nPRETTY_NAME=\"Alpine Linux v3.20\"\n");
    write("/proc/sys/kernel/hostname", "node-7\n");
    write("/proc/sys/kernel/osrelease", "6.10.0\n");
    Map<String, String> env = new HashMap<>();
    env.put("OPENLOG_HOST_ROOT", root.toString());
    env.put("K8S_POD_NAME", "api-7d9");
    OpenlogSettings s = new OpenlogSettings(env, new Properties());
    Resource upstream =
        Resource.create(
            Attributes.builder()
                .put("service.name", "checkout")
                .put("host.id", "upstream-machine-id")
                .put("host.arch", "aarch64")
                .put("process.command_line", "java -Dpassword=secret -jar app.jar")
                .put(AttributeKey.stringArrayKey("process.command_args"), List.of("java", "-Dpassword=secret"))
                .put("process.executable.path", "/opt/java/bin/java")
                .put("telemetry.distro.name", "opentelemetry-java-instrumentation")
                .build());
    Resource r = OpenlogResource.customize(upstream, Map.of(), s, Diag.fromSettingsQuiet(), true);
    Attributes a = r.getAttributes();
    assertNull(a.get(AttributeKey.stringKey("process.command_line")));
    assertNull(a.get(AttributeKey.stringArrayKey("process.command_args")));
    assertEquals("/opt/java/bin/java", a.get(AttributeKey.stringKey("process.executable.path")));
    assertEquals("published-host-0001", a.get(AttributeKey.stringKey("host.id")));
    assertEquals("arm64", a.get(AttributeKey.stringKey("host.arch")));
    assertEquals("node-7", a.get(AttributeKey.stringKey("host.name")));
    assertEquals(CID, a.get(AttributeKey.stringKey("container.id")));
    assertEquals("alpine", a.get(AttributeKey.stringKey("os.name")));
    assertEquals("3.20.1", a.get(AttributeKey.stringKey("os.version")));
    assertEquals("6.10.0", a.get(AttributeKey.stringKey("openlog.os.kernel_release")));
    assertEquals("api-7d9", a.get(AttributeKey.stringKey("k8s.pod.name")));
    assertEquals("openlog", a.get(AttributeKey.stringKey("telemetry.distro.name")));
    assertTrue(a.get(AttributeKey.stringKey("telemetry.distro.version")) != null);
    assertEquals("checkout", a.get(AttributeKey.stringKey("service.name")));

    // user-set attributes are never replaced
    Resource withUser =
        upstream.merge(Resource.create(Attributes.builder().put("host.id", "explicit-id").put("container.id", "c1").build()));
    Attributes b = OpenlogResource.customize(withUser, Map.of("host.id", "explicit-id", "container.id", "c1"), s, Diag.fromSettingsQuiet(), true).getAttributes();
    assertEquals("explicit-id", b.get(AttributeKey.stringKey("host.id")));
    assertEquals("c1", b.get(AttributeKey.stringKey("container.id")));
  }
}
