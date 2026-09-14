package io.github.onuragtas.openlog.it;

import java.util.Map;

/** Vert.x Web 4.5 router. */
class VertxWebIT extends FrameworkIT {
  @Override
  String service() {
    return "java-vertx";
  }

  @Override
  AppProcess startApp(Map<String, String> env) throws Exception {
    return classpathApp("vertx", "io.github.onuragtas.openlog.testapp.vertx.VertxApp", env);
  }

  @Override
  String usersRoute() {
    return "/users/:id";
  }
}
