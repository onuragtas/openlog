package io.github.onuragtas.openlog.testapp.jaxrs;

import jakarta.ws.rs.GET;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.PathParam;
import jakarta.ws.rs.Produces;
import jakarta.ws.rs.core.MediaType;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.concurrent.CountDownLatch;
import org.eclipse.jetty.server.Server;
import org.eclipse.jetty.ee10.servlet.ServletContextHandler;
import org.eclipse.jetty.ee10.servlet.ServletHolder;
import org.eclipse.jetty.server.ServerConnector;
import org.glassfish.jersey.server.ResourceConfig;
import org.glassfish.jersey.servlet.ServletContainer;

/** Jersey 3 resources (ServletContainer) on embedded Jetty 12 ee10: GET /health, GET /users/{id} (JDBC), GET /fail. */
public final class JaxrsApp {
  @Path("/")
  public static final class Resources {
    @GET
    @Path("health")
    @Produces(MediaType.TEXT_PLAIN)
    public String health() {
      return "ok";
    }

    @GET
    @Path("users/{id}")
    @Produces(MediaType.TEXT_PLAIN)
    public String user(@PathParam("id") int id) throws Exception {
      return Integer.toString(query(id));
    }

    @GET
    @Path("fail")
    public String fail() {
      throw new IllegalStateException("boom from jaxrs");
    }
  }

  /** A literal statement on purpose: the agent must sanitize it. */
  static int query(int id) throws Exception {
    String url = "jdbc:postgresql://" + env("PG_HOST", "127.0.0.1") + ":" + env("PG_PORT", "55442") + "/openlog";
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

  public static void main(String[] args) throws Exception {
    Server server = new Server();
    ServerConnector connector = new ServerConnector(server);
    connector.setHost("127.0.0.1");
    connector.setPort(Integer.parseInt(env("APP_PORT", "18081")));
    server.addConnector(connector);
    ServletContextHandler context = new ServletContextHandler();
    context.addServlet(new ServletHolder(new ServletContainer(new ResourceConfig(Resources.class))), "/*");
    server.setHandler(context);
    server.start();
    Runtime.getRuntime().addShutdownHook(new Thread(() -> {
      try {
        server.stop();
      } catch (Exception ignored) {
        // exiting
      }
    }));
    System.out.println("jaxrs listening on " + connector.getLocalPort());
    new CountDownLatch(1).await();
  }
}
