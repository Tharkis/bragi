<script>
  import { onMount } from 'svelte'
  import { get, post, put, del } from '../lib/api.js'
  import { link, navigate } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { episodeState } from '../lib/status.js'
  import Poster from '../components/Poster.svelte'
  import Badge from '../components/Badge.svelte'
  import Modal from '../components/Modal.svelte'

  let { id, me } = $props()
  const admin = me.role === 'admin'

  let show = $state(null)
  let error = $state('')
  let notice = $state('')
  let open = $state({}) // season number -> expanded
  let busy = $state(false)

  async function load() {
    try {
      const first = show === null
      show = await get(`/series/${id}`)
      if (first) {
        // Open the newest season Bragi is working on, else the newest that has aired.
        const today = new Date().toISOString().slice(0, 10)
        const s =
          show.seasons.find((s) => s.number > 0 && s.episodes.some((e) => e.monitored || e.status === 'imported')) ??
          show.seasons.find((s) => s.number > 0 && s.episodes.some((e) => e.airDate && e.airDate <= today))
        if (s) open[s.number] = true
      }
      error = ''
    } catch (e) {
      error = e.message
    }
  }

  onMount(() => {
    load()
    return onEvents(['episodes', 'download', 'series'], (d) => {
      if (!d || !d.seriesId || d.seriesId === Number(id) || d.id === Number(id)) load()
    })
  })

  async function searchNow(season = 0, episode = 0) {
    error = ''
    try {
      const r = await post(`/series/${id}/search`, { season, episode })
      notice = r.queued ? `Searching for ${r.queued} ${r.queued === 1 ? 'episode' : 'episodes'}.` : 'Nothing to search for.'
      setTimeout(() => (notice = ''), 4000)
      await load()
    } catch (e) {
      error = e.message
    }
  }

  async function setMonitored(episodes, monitored) {
    try {
      show = await put(`/series/${id}/monitor`, { episodeIds: episodes.map((e) => e.id), monitored })
    } catch (e) {
      error = e.message
    }
  }

  async function refresh() {
    busy = true
    try {
      show = await post(`/series/${id}/refresh`)
      notice = 'Episode list updated.'
      setTimeout(() => (notice = ''), 4000)
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  async function remove() {
    if (!window.confirm(`Stop tracking ${show.title}? Files on disk are kept.`)) return
    await del(`/series/${id}`)
    navigate('/library')
  }

  // ---- manual pick ----
  let picking = $state(null) // { season, episode, label }
  let releases = $state(null)
  let pickError = $state('')
  let grabbing = $state('')
  let showRejected = $state(false)

  async function manual(season, episode, label) {
    picking = { season, episode, label }
    releases = null
    pickError = ''
    showRejected = false
    try {
      releases = await get(`/series/${id}/releases?season=${season}&episode=${episode}`)
    } catch (e) {
      pickError = e.message
    }
  }

  async function grab(r) {
    grabbing = r.guid || r.title
    pickError = ''
    try {
      await post(`/series/${id}/grab`, { season: picking.season, episode: picking.episode, release: r })
      picking = null
      notice = `Sent ${r.title} to the download client.`
      await load()
    } catch (e) {
      pickError = e.message
    } finally {
      grabbing = ''
    }
  }

  const accepted = $derived(releases?.filter((r) => !r.rejected) ?? [])
  const rejected = $derived(releases?.filter((r) => r.rejected) ?? [])

  function seasonSummary(s) {
    const today = new Date().toISOString().slice(0, 10)
    const aired = s.episodes.filter((e) => e.airDate && e.airDate <= today)
    const have = s.episodes.filter((e) => e.status === 'imported').length
    return `${have} of ${aired.length} aired episodes`
  }
  const code = (e) => `${String(e.number).padStart(2, '0')}`
</script>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if show}
  <a class="small" href="/library" onclick={link}>← Library</a>
  <div class="hero">
    <Poster url={show.posterUrl} title={show.title} size="lg" />
    <div class="info">
      <h1>{show.title} {#if show.year}<span class="muted">({show.year})</span>{/if}</h1>
      <p class="muted">{show.overview}</p>
      <div class="row small muted facts">
        <span>{show.imported} of {show.episodes} episodes</span>
        {#if show.missing}<Badge label={`${show.missing} missing`} tone="warn" />{/if}
        {#if show.activities}<Badge label={`${show.activities} downloading`} tone="accent" />{/if}
      </div>
      {#if admin}
        <div class="small muted path"><code>{show.path}</code></div>
        <div class="row">
          <button class="primary" onclick={() => searchNow()}>Search for missing</button>
          <button onclick={refresh} disabled={busy}>{busy ? 'Updating…' : 'Update episode list'}</button>
          <button class="danger" onclick={remove}>Remove</button>
        </div>
      {/if}
      {#if notice}<div class="notice good" style="margin-top: 12px">{notice}</div>{/if}
    </div>
  </div>

  <div class="stack">
    {#each show.seasons as s (s.id)}
      <div class="card season">
        <button class="season-head" onclick={() => (open[s.number] = !open[s.number])} aria-expanded={!!open[s.number]}>
          <span class="chev" class:open={open[s.number]}>›</span>
          <strong>{s.number === 0 ? 'Specials' : `Season ${s.number}`}</strong>
          <span class="muted small">{seasonSummary(s)}</span>
        </button>
        {#if open[s.number]}
          {#if admin}
            <div class="row season-actions">
              <button class="small" onclick={() => searchNow(s.number)}>Search season</button>
              <button class="small" onclick={() => manual(s.number, 0, `Season ${s.number}`)}>Pick a season pack</button>
              <button class="link small" onclick={() => setMonitored(s.episodes, true)}>Monitor all</button>
              <button class="link small" onclick={() => setMonitored(s.episodes, false)}>Monitor none</button>
            </div>
          {/if}
          <table>
            <tbody>
              {#each s.episodes as e (e.id)}
                {@const [label, tone] = episodeState(e)}
                <tr class:dim={!e.monitored && e.status !== 'imported'}>
                  <td class="num muted">{code(e)}</td>
                  <td>
                    <div>{e.title || `Episode ${e.number}`}</div>
                    {#if e.status === 'imported' && e.quality}
                      <div class="small muted">{e.quality}</div>
                    {:else if e.searchNote}
                      <div class="small muted">{e.searchNote}</div>
                    {/if}
                  </td>
                  <td class="muted small date">{e.airDate || 'TBA'}</td>
                  <td><Badge {label} {tone} /></td>
                  {#if admin}
                    <td class="actions">
                      <label class="mon" title={e.monitored ? 'Monitored' : 'Not monitored'}>
                        <input
                          type="checkbox"
                          checked={e.monitored}
                          onchange={(ev) => setMonitored([e], ev.currentTarget.checked)}
                          aria-label="Monitored"
                        />
                      </label>
                      <button class="small" onclick={() => searchNow(s.number, e.number)} disabled={e.status === 'downloading'}>
                        Search
                      </button>
                      <button class="small" onclick={() => manual(s.number, e.number, `S${String(s.number).padStart(2, '0')}E${code(e)}`)}>
                        Pick
                      </button>
                    </td>
                  {/if}
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>
    {/each}
  </div>
{/if}

{#if picking}
  <Modal title={`Pick a release: ${show.title} ${picking.label}`} wide onClose={() => (picking = null)}>
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
  .path code {
    word-break: break-all;
  }
  .hero h1 {
    margin-bottom: 8px;
  }
  .facts {
    margin-bottom: 10px;
  }
  .path {
    margin-bottom: 14px;
  }
  .season {
    padding: 0;
    overflow: hidden;
  }
  .season-head {
    width: 100%;
    border: none;
    border-radius: 0;
    padding: 14px 16px;
    background: none;
    justify-content: flex-start;
    gap: 10px;
  }
  .chev {
    display: inline-block;
    transition: transform 0.15s;
    font-size: 1.2rem;
    line-height: 1;
    width: 12px;
  }
  .chev.open {
    transform: rotate(90deg);
  }
  .season-actions {
    padding: 0 16px 8px;
  }
  .season table {
    border-top: 1px solid var(--border);
  }
  .season td:first-child {
    padding-left: 16px;
  }
  .season td:last-child {
    padding-right: 16px;
  }
  .num {
    width: 36px;
    font-variant-numeric: tabular-nums;
  }
  .date {
    white-space: nowrap;
  }
  tr.dim td {
    opacity: 0.6;
  }
  tr:last-child td {
    border-bottom: none;
  }
  button.small {
    padding: 4px 10px;
    font-size: 0.85rem;
  }
  .mon {
    display: inline-flex;
    margin: 0 6px 0 0;
    vertical-align: middle;
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
  @media (max-width: 720px) {
    .hero {
      grid-template-columns: 100px minmax(0, 1fr);
      gap: 14px;
    }
    .hero :global(.lg) {
      width: 100px;
    }
    .date,
    .num {
      display: none;
    }
    .hero .row button {
      padding: 6px 10px;
    }
  }
</style>
