<script>
  // TMDB (show information) and Jellyfin (library refresh) settings.
  import { onMount } from 'svelte'
  import { get, put, post } from '../lib/api.js'

  let { compact = false, onSaved = null } = $props()

  let tmdbKey = $state('')
  let jfUrl = $state('')
  let jfKey = $state('')
  let tmdbTest = $state(null)
  let jfTest = $state(null)
  let saved = $state('')
  let error = $state('')
  let busy = $state(false)

  onMount(async () => {
    try {
      const s = await get('/settings')
      tmdbKey = s.tmdb_api_key
      jfUrl = s.jellyfin_url
      jfKey = s.jellyfin_api_key
    } catch (e) {
      error = e.message
    }
  })

  async function testTMDB() {
    tmdbTest = { pending: true }
    tmdbTest = await post('/settings/test/tmdb', { apiKey: tmdbKey }).catch((e) => ({ ok: false, error: e.message }))
  }

  async function testJellyfin() {
    jfTest = { pending: true }
    jfTest = await post('/settings/test/jellyfin', { url: jfUrl, apiKey: jfKey }).catch((e) => ({ ok: false, error: e.message }))
  }

  async function save(event) {
    event.preventDefault()
    busy = true
    error = ''
    saved = ''
    try {
      const body = { tmdb_api_key: tmdbKey }
      if (!compact) Object.assign(body, { jellyfin_url: jfUrl, jellyfin_api_key: jfKey })
      await put('/settings', body)
      saved = 'Saved.'
      onSaved?.()
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<form onsubmit={save}>
  <div class:card={!compact} class="section">
    {#if !compact}<h2>Show information (TMDB)</h2>{/if}
    <p class="muted small">
      Bragi looks shows up on The Movie Database. Get a free API key at
      <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">themoviedb.org/settings/api</a>
      and paste the “API Key” or “Read Access Token” here.
    </p>
    <label>
      <span>TMDB API key</span>
      <input type="password" bind:value={tmdbKey} autocomplete="off" required={compact} />
    </label>
    {#if tmdbTest?.ok}<div class="notice good">TMDB accepted the key.</div>
    {:else if tmdbTest && !tmdbTest.pending}<div class="notice bad">{tmdbTest.error}</div>{/if}
    <button type="button" onclick={testTMDB} disabled={!tmdbKey || tmdbTest?.pending}>Test</button>
  </div>

  {#if !compact}
    <div class="card section">
      <h2>Jellyfin (optional)</h2>
      <p class="muted small">
        Bragi asks Jellyfin to scan its library after each import, so new episodes show up straight away. Create a key in
        Jellyfin under Dashboard, API Keys.
      </p>
      <label><span>Jellyfin URL</span><input bind:value={jfUrl} placeholder="http://192.168.1.10:8096" /></label>
      <label><span>API key</span><input type="password" bind:value={jfKey} autocomplete="off" /></label>
      {#if jfTest?.ok}<div class="notice good">Connected to {jfTest.server || 'Jellyfin'}.</div>
      {:else if jfTest && !jfTest.pending}<div class="notice bad">{jfTest.error}</div>{/if}
      <button type="button" onclick={testJellyfin} disabled={!jfUrl || jfTest?.pending}>Test</button>
    </div>
  {/if}

  {#if error}<div class="notice bad">{error}</div>{/if}
  {#if saved}<div class="notice good">{saved}</div>{/if}
  <div class="row">
    <button class="primary" disabled={busy}>{compact ? 'Save and continue' : 'Save'}</button>
  </div>
</form>

<style>
  .section {
    margin-bottom: 16px;
  }
  .section > button {
    margin-top: 2px;
  }
</style>
