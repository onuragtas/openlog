package io.github.onuragtas.openlog.testapp.quarkus;

import io.smallrye.common.annotation.Blocking;
import jakarta.ws.rs.GET;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.PathParam;
import jakarta.ws.rs.Produces;
import jakarta.ws.rs.core.MediaType;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;

/** GET /health, GET /users/{id} (JDBC on a worker thread), GET /fail. */
@Path("/")
public class UsersResource {
  @GET
  @Path("health")
  @Produces(MediaType.TEXT_PLAIN)
  public String health() {
    return "ok";
  }

  @GET
  @Path("users/{id}")
  @Produces(MediaType.TEXT_PLAIN)
  @Blocking
  public String user(@PathParam("id") int id) throws Exception {
    String url = "jdbc:postgresql://" + env("PG_HOST", "127.0.0.1") + ":" + env("PG_PORT", "23432") + "/openlog";
    // a literal statement on purpose: the agent must sanitize it
    try (Connection c = DriverManager.getConnection(url, "openlog", "openlog");
        Statement st = c.createStatement();
        ResultSet rs = st.executeQuery("SELECT 42 AS answer WHERE 'secret-" + id + "' = 'secret-" + id + "' AND 1 IN (1, 2)")) {
      rs.next();
      return Integer.toString(rs.getInt(1));
    }
  }

  @GET
  @Path("fail")
  @Produces(MediaType.TEXT_PLAIN)
  public String fail() {
    throw new IllegalStateException("boom from quarkus");
  }

  static String env(String name, String def) {
    String v = System.getenv(name);
    return v == null || v.isBlank() ? def : v;
  }
}
