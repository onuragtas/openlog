using System;
using System.Web;
using OpenLog.Agent;
using OpenTelemetry.Trace;

namespace OpenLog.AspNetFramework.Sample
{
    /// <summary>
    /// Global.asax code-behind of an ASP.NET 4.x application: the agent starts in Application_Start and adds the
    /// ASP.NET instrumentation, whose TelemetryHttpModule the OpenTelemetry.Instrumentation.AspNet package registers in
    /// web.config (see Web.config.sample). Settings come from OPENLOG_* environment variables or appSettings.
    /// </summary>
    public class Global : HttpApplication
    {
        private static IDisposable agent;

        protected void Application_Start(object sender, EventArgs e)
        {
            agent = OpenLogAgent.Start(o =>
            {
                o.ServiceName = "legacy-web";
                o.ConfigureTracing = b => b.AddAspNetInstrumentation(a => a.RecordException = true);
            });
        }

        protected void Application_End(object sender, EventArgs e)
        {
            agent?.Dispose();
        }
    }

    /// <summary>An IHttpHandler to show that request spans come from the module, not from application code.</summary>
    public sealed class UsersHandler : IHttpHandler
    {
        public bool IsReusable => true;

        public void ProcessRequest(HttpContext context)
        {
            context.Response.ContentType = "text/plain";
            context.Response.Write("user " + context.Request.QueryString["id"]);
        }
    }
}
