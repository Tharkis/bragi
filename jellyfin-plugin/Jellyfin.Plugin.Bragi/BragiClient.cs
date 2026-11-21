using System.Net;
using System.Net.Http.Headers;
using System.Text;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.Bragi;

/// <summary>A Bragi API answer: the HTTP status and the JSON body.</summary>
public sealed record BragiResponse(HttpStatusCode Status, string Body)
{
    public bool Ok => (int)Status is >= 200 and < 300;
}

/// <summary>
/// Calls Bragi's API with the admin key from the plugin settings. Calls made
/// for a Jellyfin user carry that user's id and name, and Bragi maps them to
/// a requester account of its own, so approvals work the same as in Bragi.
/// </summary>
public class BragiClient
{
    public const string HttpClientName = "Bragi";

    private readonly IHttpClientFactory _httpClientFactory;
    private readonly ILogger<BragiClient> _logger;

    public BragiClient(IHttpClientFactory httpClientFactory, ILogger<BragiClient> logger)
    {
        _httpClientFactory = httpClientFactory;
        _logger = logger;
    }

    /// <summary>Gets a value indicating whether the plugin has a Bragi URL and key.</summary>
    public static bool Configured
    {
        get
        {
            var c = Plugin.Instance?.Configuration;
            return c is not null && !string.IsNullOrWhiteSpace(c.BragiUrl) && !string.IsNullOrWhiteSpace(c.ApiKey);
        }
    }

    /// <summary>Sends a request to Bragi with the saved settings.</summary>
    /// <param name="method">HTTP method.</param>
    /// <param name="path">Path under the Bragi URL, such as /api/v1/requests.</param>
    /// <param name="jsonBody">JSON to send, or null.</param>
    /// <param name="userId">The Jellyfin user to act for, or null to act as the admin.</param>
    /// <param name="userName">That user's Jellyfin name.</param>
    /// <param name="cancellationToken">Cancellation token.</param>
    public Task<BragiResponse> SendAsync(HttpMethod method, string path, string? jsonBody, Guid? userId, string? userName, CancellationToken cancellationToken)
    {
        var c = Plugin.Instance?.Configuration;
        if (c is null || !Configured)
        {
            return Task.FromResult(new BragiResponse(HttpStatusCode.ServiceUnavailable, Error("An admin has not connected Jellyfin to Bragi yet.")));
        }

        return SendAsync(c.BragiUrl, c.ApiKey, method, path, jsonBody, userId, userName, cancellationToken);
    }

    /// <summary>Sends a request to a given Bragi URL and key, for the connection test.</summary>
    public async Task<BragiResponse> SendAsync(string baseUrl, string apiKey, HttpMethod method, string path, string? jsonBody, Guid? userId, string? userName, CancellationToken cancellationToken)
    {
        var url = baseUrl.Trim().TrimEnd('/') + path;
        using var request = new HttpRequestMessage(method, url);
        request.Headers.Add("X-Api-Key", apiKey.Trim());
        request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/json"));
        if (userId is Guid id && id != Guid.Empty)
        {
            request.Headers.Add("X-Bragi-User", id.ToString("N"));
            if (!string.IsNullOrEmpty(userName))
            {
                // Header values must be ASCII; Bragi only uses the name as a label.
                request.Headers.TryAddWithoutValidation("X-Bragi-User-Name", Uri.EscapeDataString(userName));
            }
        }

        if (jsonBody is not null)
        {
            request.Content = new StringContent(jsonBody, Encoding.UTF8, "application/json");
        }

        try
        {
            var client = _httpClientFactory.CreateClient(HttpClientName);
            client.Timeout = TimeSpan.FromSeconds(30);
            using var response = await client.SendAsync(request, cancellationToken).ConfigureAwait(false);
            var body = await response.Content.ReadAsStringAsync(cancellationToken).ConfigureAwait(false);
            if (!(response.Content.Headers.ContentType?.MediaType ?? string.Empty).Contains("json", StringComparison.OrdinalIgnoreCase) && response.StatusCode != HttpStatusCode.NoContent)
            {
                return new BragiResponse(HttpStatusCode.BadGateway, Error("The Bragi URL answered, but not like Bragi. Check the URL in the plugin settings."));
            }

            return new BragiResponse(response.StatusCode, body);
        }
        catch (Exception ex) when (ex is HttpRequestException or TaskCanceledException or UriFormatException or InvalidOperationException)
        {
            _logger.LogWarning("Could not reach Bragi at {Url}: {Message}", url, ex.Message);
            return new BragiResponse(HttpStatusCode.BadGateway, Error("Could not reach Bragi. Is it running, and is the URL right?"));
        }
    }

    private static string Error(string message) => System.Text.Json.JsonSerializer.Serialize(new { error = message });
}
