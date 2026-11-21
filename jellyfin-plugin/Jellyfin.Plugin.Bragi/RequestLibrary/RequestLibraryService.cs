using System.Globalization;
using System.Net;
using System.Security;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using MediaBrowser.Common.Configuration;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.Providers;
using MediaBrowser.Model.Configuration;
using MediaBrowser.Model.Entities;
using MediaBrowser.Model.IO;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.Bragi.RequestLibrary;

/// <summary>
/// Keeps the Request library: a Jellyfin TV library of trending shows that
/// are not in Bragi yet. Each show is a folder with only a tvshow.nfo and a
/// poster, so it shows up on every Jellyfin client, including TV apps that
/// cannot run the web integration. Marking one as a favorite requests it.
/// </summary>
public partial class RequestLibraryService
{
    public const string LibraryName = "Request";

    private readonly BragiClient _bragi;
    private readonly ILibraryManager _libraryManager;
    private readonly IApplicationPaths _paths;
    private readonly IFileSystem _fileSystem;
    private readonly IHttpClientFactory _httpClientFactory;
    private readonly ILogger<RequestLibraryService> _logger;
    private readonly SemaphoreSlim _lock = new(1, 1);

    public RequestLibraryService(BragiClient bragi, ILibraryManager libraryManager, IApplicationPaths paths, IFileSystem fileSystem,
        IHttpClientFactory httpClientFactory, ILogger<RequestLibraryService> logger)
    {
        _bragi = bragi;
        _libraryManager = libraryManager;
        _paths = paths;
        _fileSystem = fileSystem;
        _httpClientFactory = httpClientFactory;
        _logger = logger;
    }

    /// <summary>Gets the folder that holds the placeholders.</summary>
    public string LibraryPath
    {
        get
        {
            var configured = Plugin.Instance?.Configuration.RequestLibraryPath;
            return string.IsNullOrWhiteSpace(configured) ? Path.Combine(_paths.DataPath, "bragi-request-library") : configured.Trim();
        }
    }

    /// <summary>Gets a value indicating whether an item is one of the placeholders.</summary>
    public bool IsPlaceholder(BaseItem item)
        => item is MediaBrowser.Controller.Entities.TV.Series && !string.IsNullOrEmpty(item.Path)
           && item.Path.StartsWith(LibraryPath.TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar, StringComparison.Ordinal);

    /// <summary>Creates the Jellyfin library if it is missing, turns syncing on and fills it.</summary>
    /// <returns>A sentence for the settings page.</returns>
    public async Task<string> CreateLibraryAsync(CancellationToken ct)
    {
        if (!BragiClient.Configured)
        {
            throw new InvalidOperationException("Save Bragi's URL and API key first.");
        }

        Directory.CreateDirectory(LibraryPath);
        var exists = _libraryManager.GetVirtualFolders().Any(f => f.Locations.Any(l => PathEquals(l, LibraryPath)));
        if (!exists)
        {
            var options = new LibraryOptions
            {
                EnableRealtimeMonitor = false,
                EnablePhotos = false,
                PathInfos = [new MediaPathInfo(LibraryPath)],
                // Everything comes from the plugin's own tvshow.nfo files, so
                // the library never reaches out to TMDB or TVDB.
                TypeOptions =
                [
                    new TypeOptions { Type = "Series", MetadataFetchers = [], ImageFetchers = [] },
                ],
                MetadataSavers = [],
            };
            var name = LibraryName;
            if (_libraryManager.GetVirtualFolders().Any(f => string.Equals(f.Name, name, StringComparison.OrdinalIgnoreCase)))
            {
                name = "Request from Bragi";
            }

            await _libraryManager.AddVirtualFolder(name, CollectionTypeOptions.tvshows, options, false).ConfigureAwait(false);
        }

        var config = Plugin.Instance!.Configuration;
        config.EnableRequestLibrary = true;
        Plugin.Instance.SaveConfiguration();

        var count = await SyncAsync(ct).ConfigureAwait(false);
        _libraryManager.QueueLibraryScan();
        return exists
            ? $"The Request library was already there; it now lists {count} shows."
            : $"Created the Request library with {count} shows. Users mark a show as a favorite to request it.";
    }

    /// <summary>Refreshes the placeholders from Bragi's trending list.</summary>
    /// <returns>How many shows the library lists.</returns>
    public async Task<int> SyncAsync(CancellationToken ct)
    {
        var config = Plugin.Instance?.Configuration;
        if (config is null || !config.EnableRequestLibrary || !BragiClient.Configured)
        {
            return 0;
        }

        await _lock.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            return await SyncLockedAsync(config.RequestLibraryPages, ct).ConfigureAwait(false);
        }
        finally
        {
            _lock.Release();
        }
    }

    private async Task<int> SyncLockedAsync(int pages, CancellationToken ct)
    {
        var wanted = new Dictionary<int, Show>();
        for (var page = 1; page <= Math.Clamp(pages, 1, 10); page++)
        {
            var res = await _bragi.SendAsync(HttpMethod.Get, "/api/v1/discover/trending?type=tv&page=" + page, null, null, null, ct).ConfigureAwait(false);
            if (!res.Ok)
            {
                _logger.LogWarning("Bragi trending list failed: HTTP {Status} {Body}", (int)res.Status, res.Body);
                if (page == 1)
                {
                    return -1;
                }

                break;
            }

            var parsed = JsonSerializer.Deserialize<TrendingPage>(res.Body, JsonOptions);
            foreach (var s in parsed?.Results ?? [])
            {
                // Shows Bragi already has are in the real TV library.
                if (s.SeriesId == 0 && s.TmdbId > 0)
                {
                    wanted.TryAdd(s.TmdbId, s);
                }
            }

            if (parsed is null || page >= parsed.TotalPages)
            {
                break;
            }
        }

        Directory.CreateDirectory(LibraryPath);
        var existing = new Dictionary<int, string>();
        foreach (var dir in Directory.GetDirectories(LibraryPath))
        {
            var m = TmdbIdInName().Match(Path.GetFileName(dir));
            if (m.Success && int.TryParse(m.Groups[1].Value, CultureInfo.InvariantCulture, out var id))
            {
                existing[id] = dir;
            }
        }

        var changed = false;
        var rewritten = new List<(string Dir, string Plot)>();
        foreach (var (id, dir) in existing)
        {
            if (!wanted.ContainsKey(id))
            {
                Directory.Delete(dir, true);
                changed = true;
            }
        }

        foreach (var show in wanted.Values)
        {
            var dir = existing.TryGetValue(show.TmdbId, out var d) ? d : Path.Combine(LibraryPath, FolderName(show));
            Directory.CreateDirectory(dir);
            var nfo = WriteNfo(dir, show, null);
            var poster = await DownloadPosterAsync(dir, show.PosterUrl, ct).ConfigureAwait(false);
            if (existing.ContainsKey(show.TmdbId))
            {
                rewritten.Add((dir, Plot(show, null)));
            }

            changed |= nfo || poster;
        }

        if (changed)
        {
            await RescanAsync(ct).ConfigureAwait(false);
        }

        // A library scan does not re-read an existing show's tvshow.nfo, so
        // refresh the shows whose status line is out of date.
        foreach (var (dir, plot) in rewritten)
        {
            if (_libraryManager.FindByPath(dir, true) is BaseItem item && (item.IsLocked || !string.Equals(item.Overview?.ReplaceLineEndings("\n"), plot, StringComparison.Ordinal)))
            {
                await RefreshItemAsync(item, ct).ConfigureAwait(false);
            }
        }

        _logger.LogInformation("Bragi Request library lists {Count} shows", wanted.Count);
        return wanted.Count;
    }

    /// <summary>Notes on a placeholder that it was requested, so its description says so.</summary>
    public async Task MarkRequestedAsync(BaseItem item, string status, CancellationToken ct)
    {
        if (string.IsNullOrEmpty(item.Path) || !Directory.Exists(item.Path))
        {
            return;
        }

        var show = new Show { Title = item.Name, Year = item.ProductionYear ?? 0, Overview = StripStatus(item.Overview ?? string.Empty) };
        if (int.TryParse(item.GetProviderId(MetadataProvider.Tmdb), CultureInfo.InvariantCulture, out var id))
        {
            show.TmdbId = id;
        }

        show.Requested = status;
        WriteNfo(item.Path, show, status);
        await RefreshItemAsync(item, ct).ConfigureAwait(false);
    }

    private async Task RefreshItemAsync(BaseItem item, CancellationToken ct)
    {
        // A locked item ignores its tvshow.nfo; never lock the placeholders.
        item.IsLocked = false;
        await item.RefreshMetadata(FullRefresh(), ct).ConfigureAwait(false);
    }

    private MetadataRefreshOptions FullRefresh() => new(new DirectoryService(_fileSystem))
    {
        MetadataRefreshMode = MetadataRefreshMode.FullRefresh,
        ImageRefreshMode = MetadataRefreshMode.FullRefresh,
        ReplaceAllMetadata = true,
        ReplaceAllImages = true,
    };

    private async Task RescanAsync(CancellationToken ct)
    {
        if (_libraryManager.FindByPath(LibraryPath, true) is Folder folder)
        {
            var options = new MetadataRefreshOptions(new DirectoryService(_fileSystem));
            await folder.ValidateChildren(new Progress<double>(), options, true, false, ct).ConfigureAwait(false);
        }
    }

    private static string Plot(Show show, string? status)
    {
        var line = (status ?? show.Requested) switch
        {
            "pending" => "Requested. Waiting for an admin to approve it.",
            "approved" => "Requested. Bragi is getting it.",
            "available" => "Requested. It is ready in your TV library.",
            _ => "Not in the library yet. Mark it as a favorite to request it.",
        };
        return line + "\n\n" + show.Overview;
    }

    private static bool WriteNfo(string dir, Show show, string? status)
    {
        var plot = Plot(show, status);
        var sb = new StringBuilder();
        sb.Append("<?xml version=\"1.0\" encoding=\"utf-8\" standalone=\"yes\"?>\n<tvshow>\n");
        sb.Append("  <title>").Append(SecurityElement.Escape(show.Title)).Append("</title>\n");
        sb.Append("  <plot>").Append(SecurityElement.Escape(plot)).Append("</plot>\n");
        if (show.Year > 0)
        {
            sb.Append("  <year>").Append(show.Year.ToString(CultureInfo.InvariantCulture)).Append("</year>\n");
        }

        sb.Append("  <uniqueid type=\"tmdb\" default=\"true\">").Append(show.TmdbId.ToString(CultureInfo.InvariantCulture)).Append("</uniqueid>\n");
        sb.Append("  <tag>Bragi request</tag>\n</tvshow>\n");
        var path = Path.Combine(dir, "tvshow.nfo");
        var text = sb.ToString();
        if (File.Exists(path) && File.ReadAllText(path) == text)
        {
            return false;
        }

        File.WriteAllText(path, text);
        return true;
    }

    private async Task<bool> DownloadPosterAsync(string dir, string? url, CancellationToken ct)
    {
        if (string.IsNullOrEmpty(url) || Directory.GetFiles(dir, "poster.*").Length > 0)
        {
            return false;
        }

        try
        {
            var client = _httpClientFactory.CreateClient(BragiClient.HttpClientName);
            using var res = await client.GetAsync(url, ct).ConfigureAwait(false);
            if (res.StatusCode != HttpStatusCode.OK)
            {
                return false;
            }

            var ext = res.Content.Headers.ContentType?.MediaType switch
            {
                "image/png" => ".png",
                "image/webp" => ".webp",
                "image/jpeg" or "image/jpg" => ".jpg",
                _ => null,
            };
            if (ext is null)
            {
                return false;
            }

            var bytes = await res.Content.ReadAsByteArrayAsync(ct).ConfigureAwait(false);
            await File.WriteAllBytesAsync(Path.Combine(dir, "poster" + ext), bytes, ct).ConfigureAwait(false);
            return true;
        }
        catch (Exception ex) when (ex is HttpRequestException or TaskCanceledException or IOException)
        {
            _logger.LogDebug("Poster download failed for {Url}: {Message}", url, ex.Message);
            return false;
        }
    }

    private static string FolderName(Show s)
    {
        var title = InvalidChars().Replace(s.Title, string.Empty).Trim();
        var year = s.Year > 0 ? $" ({s.Year})" : string.Empty;
        return $"{title}{year} [tmdbid-{s.TmdbId}]";
    }

    private static string StripStatus(string overview)
    {
        var i = overview.IndexOf("\n\n", StringComparison.Ordinal);
        return i >= 0 && (overview.StartsWith("Not in the library", StringComparison.Ordinal) || overview.StartsWith("Requested.", StringComparison.Ordinal))
            ? overview[(i + 2)..]
            : overview;
    }

    private static bool PathEquals(string a, string b)
        => string.Equals(a.TrimEnd('/', '\\'), b.TrimEnd('/', '\\'), StringComparison.Ordinal);

    [GeneratedRegex(@"\[tmdbid-(\d+)\]")]
    private static partial Regex TmdbIdInName();

    [GeneratedRegex(@"[<>:""/\\|?*\x00-\x1f]")]
    private static partial Regex InvalidChars();

    private static readonly JsonSerializerOptions JsonOptions = new() { PropertyNameCaseInsensitive = true };

    private sealed class TrendingPage
    {
        public int Page { get; set; }

        public int TotalPages { get; set; }

        public List<Show> Results { get; set; } = [];
    }

    private sealed class Show
    {
        public int TmdbId { get; set; }

        public string Title { get; set; } = string.Empty;

        public int Year { get; set; }

        public string Overview { get; set; } = string.Empty;

        public string? PosterUrl { get; set; }

        public long SeriesId { get; set; }

        public string? Requested { get; set; }
    }
}
