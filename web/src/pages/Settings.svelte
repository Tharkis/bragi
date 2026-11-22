<script>
  import { onMount } from 'svelte'
  import { get, post } from '../lib/api.js'
  import { route, navigate, link } from '../lib/router.svelte.js'
  import IntegrationSection from '../components/IntegrationSection.svelte'
  import LibrarySection from '../components/LibrarySection.svelte'
  import ConnectionsSection from '../components/ConnectionsSection.svelte'
  import MusicSettings from '../components/MusicSettings.svelte'

  let { me } = $props()

  const tabs = [
    { path: '/settings/indexers', label: 'Indexers' },
    { path: '/settings/downloadclients', label: 'Download clients' },
    { path: '/settings/libraries', label: 'Libraries' },
    { path: '/settings/quality', label: 'Quality' },
    { path: '/settings/music', label: 'Music' },
    { path: '/settings/connections', label: 'Connections' },
    { path: '/settings/account', label: 'Account' },
  ]

  $effect(() => {
    if (route.path === '/settings' || route.path === '/settings/') navigate(tabs[0].path, true)
  })

  let presets = $state([])
  // svelte-ignore state_referenced_locally
  let apiKey = $state(me.apiKey)
  let showKey = $state(false)

  onMount(async () => {
    presets = await get('/qualitypresets')
  })

  async function rotateKey() {
    if (!window.confirm('Make a new API key? Anything using the old key stops working.')) return
    apiKey = (await post('/auth/apikey')).apiKey
    showKey = true
  }
</script>

<h1>Settings</h1>
<div class="tabs">
  {#each tabs as t (t.path)}
    <a href={t.path} onclick={link} class:active={route.path === t.path}>{t.label}</a>
  {/each}
</div>

{#if route.path === '/settings/indexers'}
  <IntegrationSection
    role="indexer"
    endpoint="/indexers"
    title="Indexers"
    intro="Where Bragi searches. Prowlarr covers all its indexers with one connection."
  />
{:else if route.path === '/settings/downloadclients'}
  <IntegrationSection
    role="downloader"
    endpoint="/downloadclients"
    title="Download clients"
    intro="Where Bragi sends releases. Use a category so Bragi only touches its own downloads."
  />
{:else if route.path === '/settings/libraries'}
  <LibrarySection />
{:else if route.path === '/settings/quality'}
  <h2>Quality presets</h2>
  <p class="muted small">Each library uses one preset. Bragi takes the first quality in the list it can find, under the size cap.</p>
  {#each [['video', 'TV and movies'], ['music', 'Music']] as [kind, heading] (kind)}
  <h3>{heading}</h3>
  <div class="stack">
    {#each presets.filter((p) => (p.mediaType || 'video') === kind) as p (p.id)}
      <div class="card">
        <strong>{p.name}</strong>
        <p class="muted small">{p.description}</p>
        <div class="row">
          {#each p.qualities as q (q)}<span class="badge">{q}</span>{/each}
        </div>
        {#if kind === 'video'}
          <p class="muted small" style="margin: 8px 0 0">
            Size cap: {p.maxMbPerMin ? `${p.maxMbPerMin} MB per minute` : 'none'}
          </p>
        {/if}
      </div>
    {/each}
  </div>
  {/each}
{:else if route.path === '/settings/music'}
  <MusicSettings />
{:else if route.path === '/settings/connections'}
  <ConnectionsSection />
{:else if route.path === '/settings/account'}
  <h2>Account</h2>
  <div class="card stack">
    <div><span class="muted">Signed in as</span> {me.username} ({me.role})</div>
    <div>
      <div class="muted small">API key, for scripts and the Jellyfin plugin. Send it as the <code>X-Api-Key</code> header.</div>
      <div class="row" style="margin-top: 6px">
        <code>{showKey ? apiKey : '•'.repeat(32)}</code>
        <button onclick={() => (showKey = !showKey)}>{showKey ? 'Hide' : 'Show'}</button>
        <button onclick={rotateKey}>New key</button>
      </div>
    </div>
  </div>
{/if}

<style>
  h3 {
    font-size: 0.95rem;
    margin: 18px 0 8px;
  }
  .tabs {
    display: flex;
    gap: 4px;
    border-bottom: 1px solid var(--border);
    margin: 16px 0 20px;
    overflow-x: auto;
  }
  .tabs a {
    padding: 8px 12px;
    color: var(--muted);
    text-decoration: none;
    border-bottom: 2px solid transparent;
    white-space: nowrap;
  }
  .tabs a.active {
    color: var(--accent);
    border-bottom-color: var(--accent);
    font-weight: 600;
  }
</style>
