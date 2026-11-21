using MediaBrowser.Model.Plugins;

namespace Jellyfin.Plugin.Bragi.Configuration;

/// <summary>Settings an admin enters on the plugin page.</summary>
public class PluginConfiguration : BasePluginConfiguration
{
    /// <summary>Gets or sets Bragi's address as Jellyfin's server sees it, such as http://bragi:8787.</summary>
    public string BragiUrl { get; set; } = string.Empty;

    /// <summary>Gets or sets an admin API key from Bragi (Settings, Account). It never leaves the server.</summary>
    public string ApiKey { get; set; } = string.Empty;

    /// <summary>Gets or sets a value indicating whether the web client gets Bragi search results and the Requests panel.</summary>
    public bool EnableWebIntegration { get; set; } = true;

    /// <summary>Gets or sets a value indicating whether the Request library is kept up to date.</summary>
    public bool EnableRequestLibrary { get; set; }

    /// <summary>Gets or sets the folder that holds the Request library's placeholders. Empty uses Jellyfin's data folder.</summary>
    public string RequestLibraryPath { get; set; } = string.Empty;

    /// <summary>Gets or sets how many pages of trending shows (20 each) the Request library holds.</summary>
    public int RequestLibraryPages { get; set; } = 2;
}
