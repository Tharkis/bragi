<script>
  import { onMount } from 'svelte'
  import { get, post, put } from '../lib/api.js'
  import { link } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { albumState } from '../lib/status.js'
  import Poster from '../components/Poster.svelte'
  import Badge from '../components/Badge.svelte'
  import Modal from '../components/Modal.svelte'

  let { id, me } = $props()
  const admin = me.role === 'admin'

  let album = $state(null)
  let error = $state('')
  let notice = $state('')

  async function load() {
    try {
      album = await get(`/albums/${id}`)
      error = ''
    } catch (e) {
      error = e.message
    }
  }

  onMount(() => {
    load()
    return onEvents(['album', 'download'], (d) => {
      if (!d || d.id === Number(id) || d.albumId === Number(id)) load()
    })
  })

  function flash(msg) {
    notice = msg
    setTimeout(() => (notice = ''), 4000)
  }

  async function searchNow() {
    error = ''
    try {
      await post(`/albums/${id}/search`)
      flash('Searching now.')
      await load()
    } catch (e) {
      error = e.message
    }
  }

  async function setMonitored(on) {
    try {
      album = await put(`/albums/${id}/monitor`, { monitored: on })
    } catch (e) {
      error = e.message
    }
  }

  // ---- manual pick ----
  let picking = $state(false)
  let releases = $state(null)
  let pickError = $state('')
  let grabbing = $state('')
  let showRejected = $state(false)

  async function manual() {
    picking = true
    releases = null
    pickError = ''
    showRejected = false
    try {
      releases = await get(`/albums/${id}/releases`)
    } catch (e) {
      pickError = e.message
    }
  }

  async function grab(r) {
    grabbing = r.guid || r.title
    pickError = ''
    try {
      await post(`/albums/${id}/grab`, { release: r })
      picking = false
      flash(`Sent ${r.title} to ${r.protocol === 'soulseek' ? 'Soulseek' : 'the download client'}.`)
      await load()
    } catch (e) {
      pickError = e.message
    } finally {
      grabbing = ''
    }
  }

  const accepted = $derived(releases?.filter((r) => !r.rejected) ?? [])
  const rejected = $derived(releases?.filter((r) => r.rejected) ?? [])

  function source(r) {
    if (r.protocol === 'soulseek') return r.seeders >= 1000 ? 'Free slot now' : 'Queued at the peer'
    if (r.protocol === 'torrent') return `${r.seeders} seeders`
    return 'Usenet'
  }

  const discs = $derived(album ? [...new Set(album.trackList?.map((t) => t.disc) ?? [])] : [])
  const length = (s) => (s ? `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}` : '')
  const total = $derived(album?.trackList?.reduce((n, t) => n + (t.seconds || 0), 0) ?? 0)
  const quality = (q) => (q ? q.replace('-', ' ') : '')
  const have = $derived(album && (album.status === 'imported' || album.status === 'upgrade_wanted'))
</script>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if album}
  {@const [label, tone] = albumState(album)}
  <a class="small" href={`/artists/${album.artistDbId}`} onclick={link}>← {album.artist}</a>
  <div class="hero">
    <Poster url={album.coverUrl} title={album.title} size="lg" square />
    <div class="info">
      <h1>{album.title}</h1>
      <div class="byline">
        <a href={`/artists/${album.artistDbId}`} onclick={link}>{album.artist}</a>
        <span class="muted">{album.year || ''}</span>
        {#if album.albumType !== 'Album'}<span class="badge">{album.albumType}</span>{/if}
        <Badge {label} {tone} />
        {#if album.quality}<span class="badge">{quality(album.quality)}</span>{/if}
      </div>
      <div class="small muted">
        {album.tracks} {album.tracks === 1 ? 'track' : 'tracks'}{total ? ` · ${Math.round(total / 60)} min` : ''}{album.importedTracks && !have ? ` · ${album.importedTracks} on disk` : ''}
      </div>
      {#if album.searchNote && !have}<p class="small muted">{album.searchNote}</p>{/if}
      {#if album.status === 'upgrade_wanted'}<p class="small muted">Bragi keeps looking for a better copy than {quality(album.quality)}.</p>{/if}
      {#if admin}
        {#if album.path}<div class="small muted path"><code>{album.path}</code></div>{/if}
        <div class="row">
          {#if !have || album.status === 'upgrade_wanted'}
            <button class="primary" onclick={searchNow} disabled={album.status === 'downloading'}>Search now</button>
          {/if}
          <button onclick={manual}>{have ? 'Find another copy' : 'Pick a release'}</button>
          <label class="check mon">
            <input type="checkbox" checked={album.monitored} onchange={(e) => setMonitored(e.currentTarget.checked)} />
            <span class="small">Search automatically</span>
          </label>
        </div>
      {/if}
      {#if notice}<div class="notice good" style="margin-top: 12px">{notice}</div>{/if}
    </div>
  </div>

  {#if album.trackList?.length}
    <div class="card tracks">
      <table>
        <tbody>
          {#each discs as d (d)}
            {#if discs.length > 1}
              <tr class="disc"><td colspan="4">Disc {d}</td></tr>
            {/if}
            {#each album.trackList.filter((t) => t.disc === d) as t (t.id)}
              <tr class:missing={!t.filePath}>
                <td class="num muted">{t.number}</td>
                <td>
                  <div>{t.title}</div>
                  {#if t.artist && t.artist !== album.artist}<div class="small muted">{t.artist}</div>{/if}
                </td>
                <td class="muted small nowrap">{length(t.seconds)}</td>
                <td class="nowrap right">
                  {#if t.filePath}<span class="badge good" title={t.filePath}>{quality(t.quality) || 'On disk'}</span>
                  {:else}<span class="small muted">Missing</span>{/if}
                </td>
              </tr>
            {/each}
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <div class="card empty muted">MusicBrainz has no track list for this album yet.</div>
  {/if}
{/if}

{#if picking}
  <Modal title={`Pick a release: ${album.title}`} wide onClose={() => (picking = false)}>
    {#if pickError}<div class="notice bad">{pickError}</div>{/if}
    {#if releases === null && !pickError}
      <p class="muted">Asking Soulseek and your indexers… this takes about 20 seconds.</p>
    {:else if releases}
      <p class="muted small">
        {accepted.length} of {releases.length} results look like this album. The first one is what Bragi would pick.
      </p>
      <div class="table-wrap">
        <table class="releases">
          <thead><tr><th>Release</th><th>Quality</th><th>Size</th><th>Source</th><th></th></tr></thead>
          <tbody>
            {#each showRejected ? releases : accepted as r, i (r.guid + r.title + i)}
              <tr class:dim={r.rejected}>
                <td>
                  <div class="rel">{r.title}</div>
                  {#if r.rejected}<div class="small bad-text">{r.rejected}</div>{/if}
                </td>
                <td class="nowrap"><span class="badge">{r.qualityLabel || '?'}</span></td>
                <td class="nowrap">{r.size}{#if r.files}<div class="small muted">{r.files} files</div>{/if}</td>
                <td class="small nowrap">
                  {r.indexer}<br /><span class="muted">{source(r)}{r.age ? ` · ${r.age}` : ''}</span>
                </td>
                <td class="actions">
                  <button class:primary={i === 0 && !r.rejected} onclick={() => grab(r)} disabled={!!grabbing}>
                    {grabbing === (r.guid || r.title) ? 'Sending…' : 'Grab'}
                  </button>
                </td>
              </tr>
            {:else}
              <tr><td colspan="5" class="muted">Nothing suitable found.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if rejected.length}
        <button class="link small" style="margin-top: 10px" onclick={() => (showRejected = !showRejected)}>
          {showRejected ? 'Hide' : 'Show'} {rejected.length} rejected {rejected.length === 1 ? 'release' : 'releases'}
        </button>
      {/if}
    {/if}
  </Modal>
{/if}

<style>
  .hero {
    display: grid;
    grid-template-columns: 180px 1fr;
    gap: 24px;
    margin: 12px 0 24px;
  }
  .info {
    min-width: 0;
  }
  .hero h1 {
    margin-bottom: 4px;
  }
  .byline {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 6px;
  }
  .path {
    margin: 12px 0 14px;
  }
  .path code {
    word-break: break-all;
  }
  .mon {
    margin: 0 6px;
  }
  .mon input {
    width: auto;
  }
  .tracks {
    padding: 4px 16px;
  }
  .tracks tr:last-child td {
    border-bottom: none;
  }
  .tracks .num {
    width: 28px;
    text-align: right;
  }
  .tracks tr.missing td:not(.right) {
    opacity: 0.6;
  }
  .disc td {
    font-weight: 600;
    padding-top: 14px;
  }
  .right {
    text-align: right;
  }
  .table-wrap {
    overflow-x: auto;
  }
  .releases .rel {
    word-break: break-all;
    font-size: 0.9rem;
  }
  .nowrap {
    white-space: nowrap;
  }
  .bad-text {
    color: var(--bad);
  }
  tr.dim td {
    opacity: 0.6;
  }
  @media (max-width: 720px) {
    .hero {
      grid-template-columns: 100px minmax(0, 1fr);
      gap: 14px;
    }
    .hero :global(.lg) {
      width: 100px;
    }
  }
</style>
