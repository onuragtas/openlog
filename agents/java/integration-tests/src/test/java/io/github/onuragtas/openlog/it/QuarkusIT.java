package io.github.onuragtas.openlog.it;

import java.util.List;
import java.util.Map;

/** Quarkus (JVM mode, quarkus-run.jar) with Quarkus REST. */
class QuarkusIT extends FrameworkIT {
  @Override
  String service() {
    return "java-quarkus";
  }

  @Override
  AppProcess startApp(Map<String, String> env) throws Exception {
    return AppProcess.startJar(
        "quarkus", AppProcess.appDir("quarkus").resolve("quarkus-run.jar"), AppProcess.agentJar(), env, List.of(), verbose());
  }

  @Override
  String usersRoute() {
    return "/users/{id}";
  }
}
