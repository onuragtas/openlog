package io.github.onuragtas.openlog.testapp.micronaut;

import io.micronaut.http.MediaType;
import io.micronaut.http.annotation.Controller;
import io.micronaut.http.annotation.Get;
import io.micronaut.http.annotation.PathVariable;
import io.micronaut.http.annotation.Produces;
import io.micronaut.runtime.Micronaut;
import io.micronaut.scheduling.TaskExecutors;
import io.micronaut.scheduling.annotation.ExecuteOn;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.Map;

/** GET /health, GET /users/{id} (JDBC on the blocking executor), GET /fail. */
@Controller
public class MicronautApp {
  public static void main(String[] args) {
    Micronaut.build(args)
        .properties(Map.of("micronaut.server.port", env("APP_PORT", "18083"), "micronaut.server.host", "127.0.0.1"))
        .mainClass(MicronautApp.class)
        .start();
  }

  @Get("/health")
  @Produces(MediaType.TEXT_PLAIN)
  public String health() {
    return "ok";
  }

  @Get("/users/{id}")
  @Produces(MediaType.TEXT_PLAIN)
  @ExecuteOn(TaskExecutors.BLOCKING)
  public String user(@PathVariable int id) throws Exception {
    String url = "jdbc:postgresql://" + env("PG_HOST", "127.0.0.1") + ":" + env("PG_PORT", "23432") + "/openlog";
    // a literal statement on purpose: the agent must sanitize it
    try (Connection c = DriverManager.getConnection(url, "openlog", "openlog");
        Statement st = c.createStatement();
        ResultSet rs = st.executeQuery("SELECT 42 AS answer WHERE 'secret-" + id + "' = 'secret-" + id + "' AND 1 IN (1, 2)")) {
      rs.next();
      return Integer.toString(rs.getInt(1));
    }
  }

  @Get("/fail")
  @Produces(MediaType.TEXT_PLAIN)
  public String fail() {
    throw new IllegalStateException("boom from micronaut");
  }

  static String env(String name, String def) {
    String v = System.getenv(name);
    return v == null || v.isBlank() ? def : v;
  }
}
