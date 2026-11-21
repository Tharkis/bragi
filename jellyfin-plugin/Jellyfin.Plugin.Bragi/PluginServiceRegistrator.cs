using Jellyfin.Plugin.Bragi.RequestLibrary;
using Jellyfin.Plugin.Bragi.Web;
using MediaBrowser.Controller;
using MediaBrowser.Controller.Plugins;
using Microsoft.AspNetCore.Hosting;
using Microsoft.Extensions.DependencyInjection;

namespace Jellyfin.Plugin.Bragi;

/// <summary>Registers the plugin's services with Jellyfin.</summary>
public class PluginServiceRegistrator : IPluginServiceRegistrator
{
    public void RegisterServices(IServiceCollection serviceCollection, IServerApplicationHost applicationHost)
    {
        serviceCollection.AddHttpClient(BragiClient.HttpClientName);
        serviceCollection.AddSingleton<BragiClient>();
        serviceCollection.AddSingleton<RequestLibraryService>();
        serviceCollection.AddHostedService<FavoriteRequester>();
        serviceCollection.AddSingleton<IStartupFilter, ScriptInjector>();
    }
}
