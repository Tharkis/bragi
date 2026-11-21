<script>
  import { onMount } from 'svelte'
  import { get, del } from '../lib/api.js'
  import { link } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { downloadStates, when, itemPath } from '../lib/status.js'
  import Poster from '../components/Poster.svelte'
  import Badge from '../components/Badge.svelte'

  let { me, tab = 'queue' } = $props()
  const admin = me.role === 'admin'

  let queue = $state([])
  let history = $state([])
  let loaded = $state(false)
  let error = $state('')

  async function load() {
    try {
      ;[queue, history] = await Promise.all([get('/activity/queue'), get('/activity/history')])
      error = ''
    } catch (e) {
      error = e.message
    } finally {
      loaded = true
    }
  }

  onMount(() => {
    load()
    // Progress is polled from the clients every 30 seconds; refetch then too.
    const t = setInterval(load, 15000)
    const off = onEvents(['download', 'episodes', 'movie', 'book'], load, 250)
    return () => {
      clearInterval(t)
      off()
    }
  })

  async function remove(d) {
    if (!window.confirm(`Remove ${d.releaseTitle}? Bragi deletes it from ${d.clientName} and searches for another release.`)) return
    try {
      await del(`/activity/queue/${d.id}`)
      await load()
    } catch (e) {
      error = e.message
    }
  }

  const items = $derived(tab === 'queue' ? queue : history)
</script>

<div class="page-head">
  <div>
    <h1>Activity</h1>
    <p class="muted">Downloads in progress and what happened to earlier ones.</p>
  </div>
</div>

<div class="tabs">
  <a href="/activity" onclick={link} class:active={tab === 'queue'}>Queue {#if queue.length}<span class="badge accent">{queue.length}</span>{/if}</a>
  <a href="/activity/history" onclick={link} class:active={tab === 'history'}>History</a>
</div>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if loaded && items.length === 0}
  <div class="card empty muted">
    {tab === 'queue' ? 'Nothing is downloading.' : 'Nothing has finished yet.'}
  </div>
{/if}

<div class="stack">
  {#each items as d (d.id)}
    {@const [label, tone] = downloadStates[d.status] ?? [d.status, '']}
    <div class="card dl">
      <Poster url={d.posterUrl} title={d.seriesTitle} size="sm" />
      <div class="body">
        <div class="spread">
          <div class="what">
            <a href={itemPath(d.mediaType, d.seriesId)} onclick={link}
              ><strong>{d.seriesTitle}</strong></a
            >
            <span class="muted">{d.mediaType === 'movie' ? 'Movie' : d.label}</span>
          </div>
          <Badge {label} {tone} />
        </div>
        <div class="small muted rel">{d.releaseTitle}</div>
        {#if tab === 'queue'}
          <div class="bar"><div style="width: {Math.round(d.progress * 100)}%"></div></div>
          <div class="spread small muted">
            <span>{Math.round(d.progress * 100)}% of {d.size} · {d.quality} · {d.clientName}</span>
            {#if admin}<button class="link small" onclick={() => remove(d)}>Remove</button>{/if}
          </div>
          {#if d.error}<div class="small warn-text">{d.error}</div>{/if}
        {:else}
          <div class="small muted">
            {d.quality} · {d.size} · {d.indexer} · {when(d.updatedAt + '')}
          </div>
          {#if d.error}<div class="small bad-text">{d.error}</div>{/if}
        {/if}
      </div>
    </div>
  {/each}
</div>

<style>
  .dl {
    display: grid;
    grid-template-columns: 44px 1fr;
    gap: 14px;
    padding: 12px 14px;
  }
  .body {
    display: grid;
    gap: 6px;
    min-width: 0;
  }
  .what a {
    color: inherit;
    text-decoration: none;
  }
  .rel {
    word-break: break-all;
  }
  .tabs .badge {
    margin-left: 4px;
  }
  .bad-text {
    color: var(--bad);
  }
  .warn-text {
    color: var(--warn);
  }
</style>
