# Bragi for Jellyfin

A Jellyfin plugin that lets everyone on your server find shows and request them from [Bragi](../README.md) without leaving Jellyfin. Requests carry each person's Jellyfin name and go through Bragi's approval, the same as requests made in Bragi.

Needs Jellyfin 12.

## What it adds

**Web and mobile apps** (browsers, Jellyfin Media Player, the Android and iOS apps):

- A **Request** button in the header. It opens a panel with this week's trending shows, a **Music** tab to follow an artist or request an album, a search box, and **My requests** with each request's status and progress.
- **Request from Bragi** under Jellyfin's own search results, for shows you don't have yet.
- Requesting asks which seasons: all, the latest, only new episodes, or a pick.

**TV apps** (Android TV, Fire TV, Roku, Swiftfin and others, which can't run plugin code):

- A **Request** library of trending shows Bragi doesn't have yet. Each one is a tiny placeholder (a `tvshow.nfo` and a poster, never video). Marking one as a **favorite** requests it, and its description changes to say it was requested. Once Bragi adds the show, it leaves the Request library and shows up in your real TV library.

The Bragi API key stays on the Jellyfin server. Browsers and apps only talk to Jellyfin, which passes requests on with the signed-in user's id.

## Install

1. Download `jellyfin-plugin-bragi.zip` from the [latest `plugin-v*` release](https://github.com/Tharkis/bragi/releases) (or the build artifact of the Jellyfin plugin workflow). Maintainers publish a release from Actions, *Jellyfin plugin*, *Run workflow*, with a version number.
2. Unzip it into Jellyfin's plugin folder, so you have `plugins/Bragi/Jellyfin.Plugin.Bragi.dll` (on Docker and Unraid: `/config/plugins/Bragi/`).
3. Restart Jellyfin.

## Set up

In Jellyfin, open **Dashboard, Plugins, Bragi**:

1. Enter Bragi's URL as the Jellyfin server sees it (for example `http://bragi:8787`) and an **admin** API key from Bragi (Settings, Account). Press **Test connection**, then **Save**.
2. For TV apps, press **Create Request library**. It refreshes every six hours, or run *Refresh Bragi Request library* under Scheduled Tasks.

Requests from Jellyfin users wait for approval unless the user is set to auto-approve in Bragi. Each Jellyfin user gets a requester account in Bragi on their first request.

## Jellyseerr-compatible API

Bragi also answers a Jellyseerr-style API at `http://bragi:8787/seerr`, so tools built for Jellyseerr, such as Jellyfin Enhanced, can search and request through Bragi. Point them at that URL with a Bragi admin API key.

## Develop

Needs the .NET 10 SDK.

```
dotnet build jellyfin-plugin/Jellyfin.Plugin.Bragi -c Release
```

The web client script and styles are `Web/bragi.js` and `Web/bragi.css`, embedded in the DLL and served at `/Bragi/ClientScript` and `/Bragi/ClientStyle`. The plugin adds the script tag to `index.html` as Jellyfin serves it, so nothing on disk changes.
