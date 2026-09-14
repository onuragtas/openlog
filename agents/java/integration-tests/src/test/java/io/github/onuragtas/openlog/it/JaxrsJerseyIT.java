package io.github.onuragtas.openlog.it;

import java.util.Map;

/** JAX-RS: Jersey 3.1 servlet on embedded Jetty 12 (ee10). */
class JaxrsJerseyIT extends FrameworkIT {
  @Override
  String service() {
    return "java-jaxrs";
  }

  @Override
  AppProcess startApp(Map<String, String> env) throws Exception {
    return classpathApp("jaxrs", "io.github.onuragtas.openlog.testapp.jaxrs.JaxrsApp", env);
  }

  @Override
  String usersRoute() {
    return "/users/{id}";
  }
}
