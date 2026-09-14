package io.github.onuragtas.openlog.it;

import java.util.Map;

/**
 * Micronaut 4 (Netty HTTP server). The upstream agent has no Micronaut instrumentation: the SERVER span comes from
 * netty-4.1 and carries no http.route (name "GET"), JDBC and context propagation work.
 */
class MicronautIT extends FrameworkIT {
  @Override
  String service() {
    return "java-micronaut";
  }

  @Override
  AppProcess startApp(Map<String, String> env) throws Exception {
    return classpathApp("micronaut", "io.github.onuragtas.openlog.testapp.micronaut.MicronautApp", env);
  }

  @Override
  String usersRoute() {
    return null;
  }
}
