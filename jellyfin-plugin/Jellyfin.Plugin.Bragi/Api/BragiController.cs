using System.Net.Mime;
using System.Reflection;
using System.Text.Json;
using Jellyfin.Plugin.Bragi.RequestLibrary;
using MediaBrowser.Controller.Library;
using Microsoft.AspNetCore.Authorization;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Mvc;

namespace Jellyfin.Plugin.Bragi.Api;

/// <summary>
/// The web client talks to Bragi through these endpoints, so the Bragi API
/// key stays on the server and every call carries the signed-in Jellyfin user.
/// </summary>
[ApiController]
[Route("Bragi")]
[Authorize]
public class BragiController : ControllerBase
{
    private readonly BragiClient _bragi;
    private readonly IUserManager _userManager;
    private readonly RequestLibraryService _requestLibrary;

    public BragiController(BragiClient bragi, IUserManager userManager, RequestLibraryService requestLibrary)
    {
        _bragi = bragi;
        _userManager = userManager;
        _requestLibrary = requestLibrary;
    }

    /// <summary>Searches TMDB through Bragi, marked with what Bragi already has.</summary>
    [HttpGet("Search")]
    public Task<IActionResult> Search([FromQuery] string query, CancellationToken ct)
        => Proxy(HttpMethod.Get, "/api/v1/discover/search?type=tv&q=" + Uri.EscapeDataString(query ?? string.Empty), null, ct);

    /// <summary>This week's trending shows.</summary>
    [HttpGet("Trending")]
    public Task<IActionResult> Trending([FromQuery] int page, CancellationToken ct)
        => Proxy(HttpMethod.Get, "/api/v1/discover/trending?type=tv&page=" + Math.Max(page, 1), null, ct);

    /// <summary>One show's seasons, for the season picker.</summary>
    [HttpGet("Shows/{tmdbId:int}")]
    public Task<IActionResult> Show([FromRoute] int tmdbId, CancellationToken ct)
        => Proxy(HttpMethod.Get, "/api/v1/discover/tv/" + tmdbId, null, ct);

    /// <summary>The signed-in user's requests.</summary>
    [HttpGet("Requests")]
    public Task<IActionResult> MyRequests(CancellationToken ct)
        => Proxy(HttpMethod.Get, "/api/v1/requests?mine=true", null, ct);

    /// <summary>Requests a show: {"type":"tv","tmdbId":1,"seasons":"all"}.</summary>
    [HttpPost("Requests")]
    public Task<IActionResult> CreateRequest([FromBody] JsonElement body, CancellationToken ct)
        => Proxy(HttpMethod.Post, "/api/v1/requests", body.GetRawText(), ct);

    /// <summary>Cancels one of the user's pending requests.</summary>
    [HttpDelete("Requests/{id:long}")]
    public Task<IActionResult> CancelRequest([FromRoute] long id, CancellationToken ct)
        => Proxy(HttpMethod.Delete, "/api/v1/requests/" + id, null, ct);

    /// <summary>Tells the web client whether to show Bragi.</summary>
    [HttpGet("ClientConfig")]
    public IActionResult ClientConfig()
    {
        var c = Plugin.Instance?.Configuration;
        return Ok(new { enabled = c?.EnableWebIntegration == true && BragiClient.Configured });
    }

    /// <summary>Checks a Bragi URL and key before they are saved.</summary>
    [HttpPost("Test")]
    [Authorize(Policy = "RequiresElevation")]
    public async Task<IActionResult> Test([FromBody] TestBody body, CancellationToken ct)
    {
        if (string.IsNullOrWhiteSpace(body.Url) || string.IsNullOrWhiteSpace(body.ApiKey))
        {
            return Ok(new { ok = false, message = "Enter Bragi's URL and an API key." });
        }

        var res = await _bragi.SendAsync(body.Url, body.ApiKey, HttpMethod.Get, "/api/v1/auth/me", null, null, null, ct).ConfigureAwait(false);
        if (!res.Ok)
        {
            var msg = (int)res.Status == 401 ? "Bragi did not accept that API key." : ErrorText(res.Body) ?? $"Bragi answered HTTP {(int)res.Status}.";
            return Ok(new { ok = false, message = msg });
        }

        using var doc = JsonDocument.Parse(res.Body);
        var root = doc.RootElement;
        if (root.TryGetProperty("role", out var role) && role.GetString() != "admin")
        {
            return Ok(new { ok = false, message = "That key belongs to a requester. Use an admin's key, so the plugin can request for each Jellyfin user." });
        }

        var name = root.TryGetProperty("username", out var u) ? u.GetString() : "admin";
        return Ok(new { ok = true, message = $"Connected to Bragi as {name}." });
    }

    /// <summary>Creates the Request library and fills it.</summary>
    [HttpPost("RequestLibrary")]
    [Authorize(Policy = "RequiresElevation")]
    public async Task<IActionResult> CreateRequestLibrary(CancellationToken ct)
    {
        try
        {
            var message = await _requestLibrary.CreateLibraryAsync(ct).ConfigureAwait(false);
            return Ok(new { ok = true, message });
        }
        catch (InvalidOperationException ex)
        {
            return Ok(new { ok = false, message = ex.Message });
        }
    }

    /// <summary>The script the web client loads.</summary>
    [HttpGet("ClientScript")]
    [AllowAnonymous]
    public IActionResult ClientScript() => Resource("Web.bragi.js", "application/javascript");

    /// <summary>The stylesheet the web client loads.</summary>
    [HttpGet("ClientStyle")]
    [AllowAnonymous]
    public IActionResult ClientStyle() => Resource("Web.bragi.css", "text/css");

    private IActionResult Resource(string name, string contentType)
    {
        var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream(typeof(Plugin).Namespace + "." + name);
        if (stream is null)
        {
            return NotFound();
        }

        Response.Headers.CacheControl = "no-cache";
        return File(stream, contentType);
    }

    private async Task<IActionResult> Proxy(HttpMethod method, string path, string? body, CancellationToken ct)
    {
        var userId = CurrentUserId();
        if (userId is null)
        {
            return Unauthorized();
        }

        var name = _userManager.GetUserById(userId.Value)?.Username;
        var res = await _bragi.SendAsync(method, path, body, userId, name, ct).ConfigureAwait(false);
        if (res.Status == System.Net.HttpStatusCode.NoContent)
        {
            return NoContent();
        }

        return new ContentResult { StatusCode = (int)res.Status, Content = res.Body, ContentType = MediaTypeNames.Application.Json };
    }

    private Guid? CurrentUserId()
    {
        var claim = User.Claims.FirstOrDefault(c => c.Type == "Jellyfin-UserId")?.Value;
        return Guid.TryParse(claim, out var id) ? id : null;
    }

    private static string? ErrorText(string body)
    {
        try
        {
            using var doc = JsonDocument.Parse(body);
            return doc.RootElement.TryGetProperty("error", out var e) ? e.GetString() : null;
        }
        catch (JsonException)
        {
            return null;
        }
    }

    /// <summary>The connection test's input.</summary>
    public class TestBody
    {
        public string Url { get; set; } = string.Empty;

        public string ApiKey { get; set; } = string.Empty;
    }
}
