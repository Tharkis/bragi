using MediaBrowser.Model.Tasks;

namespace Jellyfin.Plugin.Bragi.RequestLibrary;

/// <summary>Refreshes the Request library every six hours, and on demand from Scheduled Tasks.</summary>
public class RequestLibraryTask : IScheduledTask
{
    private readonly RequestLibraryService _library;

    public RequestLibraryTask(RequestLibraryService library)
    {
        _library = library;
    }

    public string Name => "Refresh Bragi Request library";

    public string Key => "BragiRequestLibrary";

    public string Description => "Updates the Request library with Bragi's trending shows.";

    public string Category => "Bragi";

    public async Task ExecuteAsync(IProgress<double> progress, CancellationToken cancellationToken)
    {
        await _library.SyncAsync(cancellationToken).ConfigureAwait(false);
        progress.Report(100);
    }

    public IEnumerable<TaskTriggerInfo> GetDefaultTriggers()
    {
        yield return new TaskTriggerInfo { Type = TaskTriggerInfoType.IntervalTrigger, IntervalTicks = TimeSpan.FromHours(6).Ticks };
        yield return new TaskTriggerInfo { Type = TaskTriggerInfoType.StartupTrigger };
    }
}
