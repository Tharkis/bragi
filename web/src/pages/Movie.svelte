<script>
  import { onMount } from 'svelte'
  import { get, post, put, del } from '../lib/api.js'
  import { link, navigate } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { movieState, releaseNote } from '../lib/status.js'
  import Poster from '../components/Poster.svelte'
  import Badge from '../components/Badge.svelte'
  import Modal from '../components/Modal.svelte'

  let { id, me } = $props()
  const admin = me.role === 'admin'

  let movie = $state(null)
  let error = $state('')
  let notice = $state('')
  let busy = $state(false)

  async function load() {
    try {
      movie = await get(`/movies/${id}`)
      error = ''
    } catch (e) {
      error = e.message
    }
  }

  onMount(() => {
    load()
    return onEvents(['movie', 'download'], (d) => {
      if (!d || d.id === Number(id) || d.movieId === Number(id)) load()
    })
  })

  function flash(msg) {
    notice = msg
    setTimeout(() => (notice = ''), 5000)
  }

  async function searchNow() {
    error = ''
    try {
      const r = await post(`/movies/${id}/search`)
      flash(r.queued ? `Searching for ${movie.title}.` : r.message)
      await load()
    } catch (e) {
      error = e.message
    }
  }

  async function setMonitored(on) {
    try {
      movie = await put(`/movies/${id}/monitor`, { monitored: on })
    } catch (e) {
      error = e.message
    }
  }

  async function refresh() {
    busy = true
    try {
      movie = await post(`/movies/${id}/refresh`)
      flash('Movie details updated.')
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  async function remove() {
    if (!window.confirm(`Stop tracking ${movie.title}? The file on disk is kept.`)) return
    await del(`/movies/${id}`)
    navigate('/library/movies')
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
      releases = await get(`/movies/${id}/releases`)
    } catch (e) {
      pickError = e.message
    }
  }

  async function grab(r) {
    grabbing = r.guid || r.title
    pickError = ''
    try {
      await post(`/movies/${id}/grab`, { release: r })
      picking = false
      flash(`Sent ${r.title} to the download client.`)
      await load()
    } catch (e) {
      pickError = e.message
    } finally {
      grabbing = ''
    }
  }

  const accepted = $derived(releases?.filter((r) => !r.rejected) ?? [])
  const rejected = $derived(releases?.filter((r) => r.rejected) ?? [])
  const state = $derived(movie ? movieState(movie) : ['', ''])
  const hours = (min) => (min >= 60 ? `${Math.floor(min / 60)} h ${min % 60} min` : `${min} min`)
  const fmt = (d) => new Date(d + 'T00:00:00Z').toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })
</script>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if movie}
  <a class="small" href="/library/movies" onclick={link}>← Movies</a>
  <div class="hero">
    <Poster url={movie.posterUrl} title={movie.title} size="lg" />
    <div class="info">
      <h1>{movie.title} {#if movie.year}<span class="muted">({movie.year})</span>{/if}</h1>
      <div class="row small muted facts">
        <Badge label={state[0]} tone={state[1]} />
        {#if movie.runtime}<span>{hours(movie.runtime)}</span>{/if}
        {#if movie.inCinemas}<span>In cinemas {fmt(movie.inCinemas)}</span>{/if}
      </div>
      <p class="muted">{movie.overview}</p>

      <div class="card file">
        {#if movie.filePath}
          <div><strong>{movie.quality || 'Unknown quality'}</strong> <span class="muted small">· {movie.size}</span></div>
          {#if admin}<div class="small muted"><code>{movie.filePath}</code></div>{/if}
        {:else if movie.downloading}
          <div>Downloading now. <a href="/activity" onclick={link}>See progress</a></div>
        {:else}
          <div>{releaseNote(movie) || (movie.monitored ? 'Not in your library yet.' : 'Not monitored, so Bragi is not looking for it.')}</div>
          {#if movie.searchNote}<div class="small muted">{movie.searchNote}</div>{/if}
        {/if}
        {#if admin && !movie.filePath}<div class="small muted"><code>{movie.path}</code></div>{/if}
      </div>

      {#if admin}
        <div class="row">
          {#if !movie.filePath && !releaseNote(movie)}
            <button class="primary" onclick={searchNow} disabled={movie.downloading > 0}>Search now</button>
          {/if}
          <button onclick={manual}>{movie.filePath ? 'Replace with another release' : 'Pick a release'}</button>
          {#if !movie.filePath}
            <label class="check mon">
              <input type="checkbox" checked={movie.monitored} onchange={(ev) => setMonitored(ev.currentTarget.checked)} />
              <span>Monitored</span>
            </label>
          {/if}
          <button class="link" onclick={refresh} disabled={busy}>{busy ? 'Updating…' : 'Update details'}</button>
          <button class="danger" onclick={remove}>Remove</button>
        </div>
      {/if}
      {#if notice}<div class="notice good" style="margin-top: 12px">{notice}</div>{/if}
    </div>
  </div>
{/if}

{#if picking}
  <Modal title={`Pick a release: ${movie.title}`} wide onClose={() => (picking = false)}>
    {#if pickError}<div class="notice bad">{pickError}</div>{/if}
    {#if releases === null && !pickError}
      <p class="muted">Asking your indexers…</p>
    {:else if releases}
      <p class="muted small">
        {accepted.length} of {releases.length} releases fit your quality preset. The first one is what Bragi would pick.
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
                <td><span class="badge">{r.quality || '?'}</span></td>
                <td class="nowrap">{r.size}</td>
                <td class="small nowrap">
                  {r.indexer}<br /><span class="muted"
                    >{r.protocol === 'torrent' ? `${r.seeders} seeders` : 'Usenet'}{r.age ? ` · ${r.age}` : ''}</span
                  >
                </td>
                <td class="actions">
                  <button class:primary={i === 0 && !r.rejected} onclick={() => grab(r)} disabled={!!grabbing}>
                    {grabbing === (r.guid || r.title) ? 'Sending…' : 'Grab'}
                  </button>
                </td>
              </tr>
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
    margin-bottom: 8px;
  }
  .facts {
    margin-bottom: 10px;
  }
  .file {
    padding: 12px 14px;
    margin: 0 0 14px;
    display: grid;
    gap: 4px;
  }
  .file code {
    word-break: break-all;
  }
  .mon {
    margin: 0;
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
  tr.dim td {
    opacity: 0.6;
  }
  .bad-text {
    color: var(--bad);
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
