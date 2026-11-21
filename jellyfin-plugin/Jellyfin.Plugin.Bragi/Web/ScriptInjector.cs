using System.Text;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.Bragi.Web;

/// <summary>
/// Adds the Bragi script to jellyfin-web's index.html as it is served. Nothing
/// on disk changes, so it survives Jellyfin updates and read-only images.
/// Any error serves the page unchanged.
/// </summary>
public class ScriptInjector : IStartupFilter
{
    private const string Tag = "<script src=\"../Bragi/ClientScript\" defer></script>";

    private readonly ILogger<ScriptInjector> _logger;

    public ScriptInjector(ILogger<ScriptInjector> logger)
    {
        _logger = logger;
    }

    public Action<IApplicationBuilder> Configure(Action<IApplicationBuilder> next)
    {
        return app =>
        {
            app.Use(InjectAsync);
            next(app);
        };
    }

    private static bool IsIndex(string? path)
        => path is not null && (path.EndsWith("/web/index.html", StringComparison.OrdinalIgnoreCase)
            || path.EndsWith("/web/", StringComparison.OrdinalIgnoreCase)
            || path.EndsWith("/web", StringComparison.OrdinalIgnoreCase));

    private async Task InjectAsync(HttpContext context, Func<Task> next)
    {
        if (!HttpMethods.IsGet(context.Request.Method) || !IsIndex(context.Request.Path.Value)
            || Plugin.Instance?.Configuration.EnableWebIntegration != true)
        {
            await next().ConfigureAwait(false);
            return;
        }

        context.Request.Headers.Remove("Accept-Encoding");
        context.Request.Headers.Remove("Range");
        context.Request.Headers.Remove("If-Range");
        context.Request.Headers.Remove("If-None-Match");
        context.Request.Headers.Remove("If-Modified-Since");
        var original = context.Response.Body;
        using var buffer = new MemoryStream();
        context.Response.Body = buffer;
        try
        {
            await next().ConfigureAwait(false);
        }
        finally
        {
            context.Response.Body = original;
        }

        buffer.Position = 0;
        if (context.Response.StatusCode != 200 || !(context.Response.ContentType ?? string.Empty).Contains("text/html", StringComparison.OrdinalIgnoreCase))
        {
            await buffer.CopyToAsync(original).ConfigureAwait(false);
            return;
        }

        var html = Encoding.UTF8.GetString(buffer.ToArray());
        try
        {
            var end = html.LastIndexOf("</body>", StringComparison.OrdinalIgnoreCase);
            if (end >= 0 && !html.Contains("/Bragi/ClientScript", StringComparison.Ordinal))
            {
                html = html[..end] + Tag + html[end..];
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning("Bragi could not add its script to the web client: {Message}", ex.Message);
        }

        var bytes = Encoding.UTF8.GetBytes(html);
        context.Response.ContentLength = bytes.Length;
        context.Response.Headers.Remove("ETag");
        context.Response.Headers.Remove("Last-Modified");
        await original.WriteAsync(bytes).ConfigureAwait(false);
    }
}
