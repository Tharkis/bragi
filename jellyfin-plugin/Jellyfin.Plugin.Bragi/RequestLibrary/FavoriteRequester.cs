using System.Globalization;
using System.Text.Json;
using MediaBrowser.Controller.Library;
using MediaBrowser.Model.Entities;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.Bragi.RequestLibrary;

/// <summary>
/// Turns a favorite on a Request library placeholder into a Bragi request
/// for that user. This is how TV apps request: no plugin code runs there,
/// but every app can mark a favorite.
/// </summary>
public class FavoriteRequester : IHostedService
{
    private readonly IUserDataManager _userData;
    private readonly IUserManager _userManager;
    private readonly BragiClient _bragi;
    private readonly RequestLibraryService _library;
    private readonly ILogger<FavoriteRequester> _logger;

    public FavoriteRequester(IUserDataManager userData, IUserManager userManager, BragiClient bragi, RequestLibraryService library, ILogger<FavoriteRequester> logger)
    {
        _userData = userData;
        _userManager = userManager;
        _bragi = bragi;
        _library = library;
        _logger = logger;
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        _userData.UserDataSaved += OnUserDataSaved;
        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken cancellationToken)
    {
        _userData.UserDataSaved -= OnUserDataSaved;
        return Task.CompletedTask;
    }

    private void OnUserDataSaved(object? sender, UserDataSaveEventArgs e)
    {
        if (e.SaveReason != UserDataSaveReason.UpdateUserRating || e.UserData?.IsFavorite != true || e.Item is null || !_library.IsPlaceholder(e.Item))
        {
            return;
        }

        if (!int.TryParse(e.Item.GetProviderId(MetadataProvider.Tmdb), CultureInfo.InvariantCulture, out var tmdbId))
        {
            return;
        }

        var item = e.Item;
        var userId = e.UserId;
        _ = Task.Run(async () =>
        {
            try
            {
                var name = _userManager.GetUserById(userId)?.Username;
                var body = JsonSerializer.Serialize(new { type = "tv", tmdbId, seasons = "all" });
                var res = await _bragi.SendAsync(HttpMethod.Post, "/api/v1/requests", body, userId, name, CancellationToken.None).ConfigureAwait(false);
                string? status = null;
                if (res.Ok || (int)res.Status == 409)
                {
                    using var doc = JsonDocument.Parse(res.Body);
                    var root = doc.RootElement.TryGetProperty("request", out var r) ? r : doc.RootElement;
                    status = root.TryGetProperty("status", out var s) ? s.GetString() : "pending";
                    _logger.LogInformation("{User} requested {Title} from Bragi by marking it a favorite ({Status})", name, item.Name, status);
                    await _library.MarkRequestedAsync(item, status ?? "pending", CancellationToken.None).ConfigureAwait(false);
                }
                else
                {
                    _logger.LogWarning("Bragi refused the request for {Title}: HTTP {Status} {Body}", item.Name, (int)res.Status, res.Body);
                }
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Requesting {Title} from Bragi failed", item.Name);
            }
        });
    }
}
