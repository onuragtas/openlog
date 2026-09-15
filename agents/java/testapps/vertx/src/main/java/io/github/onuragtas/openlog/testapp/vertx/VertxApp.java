package io.github.onuragtas.openlog.testapp.vertx;

import io.vertx.core.Vertx;
import io.vertx.ext.web.Router;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;

/** Vert.x Web router: GET /health, GET /users/:id (JDBC in executeBlocking), GET /fail. */
public final class VertxApp {
  public static void main(String[] args) {
    Vertx vertx = Vertx.vertx();
    Router router = Router.router(vertx);
    router.get("/health").handler(ctx -> ctx.response().end("ok"));
    router.get("/users/:id").handler(ctx -> {
      int id = Integer.parseInt(ctx.pathParam("id"));
      vertx.<Integer>executeBlocking(() -> query(id))
          .onSuccess(v -> ctx.response().end(Integer.toString(v)))
          .onFailure(ctx::fail);
    });
    router.get("/fail").handler(ctx -> {
      throw new IllegalStateException("boom from vertx");
    });
    int port = Integer.parseInt(env("APP_PORT", "18084"));
    vertx.createHttpServer().requestHandler(router).listen(port, "127.0.0.1")
        .onSuccess(s -> System.out.println("vertx listening on " + s.actualPort()))
        .onFailure(e -> {
          e.printStackTrace();
          System.exit(1);
        });
  }

  /** A literal statement on purpose: the agent must sanitize it. */
  static int query(int id) throws Exception {
    String url = "jdbc:postgresql://" + env("PG_HOST", "127.0.0.1") + ":" + env("PG_PORT", "23432") + "/openlog";
    try (Connection c = DriverManager.getConnection(url, "openlog", "openlog");
        Statement st = c.createStatement();
        ResultSet rs = st.executeQuery("SELECT 42 AS answer WHERE 'secret-" + id + "' = 'secret-" + id + "' AND 1 IN (1, 2)")) {
      rs.next();
      return rs.getInt(1);
    }
  }

  static String env(String name, String def) {
    String v = System.getenv(name);
    return v == null || v.isBlank() ? def : v;
  }
}
