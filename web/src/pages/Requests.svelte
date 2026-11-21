<script>
  import { onMount } from 'svelte'
  import { get, post, del } from '../lib/api.js'
  import { link } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { requestStates, scopeLabel, when, isBook, bookTypes, itemPath } from '../lib/status.js'
  import Poster from '../components/Poster.svelte'
  import Badge from '../components/Badge.svelte'

  let { me } = $props()
  const admin = me.role === 'admin'

  let requests = $state([])
  let loaded = $state(false)
  let error = $state('')
  let filter = $state(admin ? 'pending' : 'all')
  let busy = $state({})

  async function load() {
    try {
      requests = await get('/requests')
      error = ''
    } catch (e) {
      error = e.message
    } finally {
      loaded = true
    }
  }

  onMount(() => {
    load()
    return onEvents(['request', 'download', 'episodes', 'book'], load)
  })

  const pending = $derived(requests.filter((r) => r.status === 'pending'))
  const shown = $derived(filter === 'pending' ? pending : requests)

  async function act(r, action) {
    busy[r.id] = true
    error = ''
    try {
      if (action === 'deny') {
        const reason = window.prompt(`Deny ${r.title}? You can give a reason (optional).`, '')
        if (reason === null) return
        await post(`/requests/${r.id}/deny`, { reason })
      } else if (action === 'cancel') {
        if (!window.confirm(`Cancel your request for ${r.title}?`)) return
        await del(`/requests/${r.id}`)
      } else {
        await post(`/requests/${r.id}/approve`)
      }
      await load()
    } catch (e) {
      error = e.message
      await load()
    } finally {
      busy[r.id] = false
    }
  }

  const pct = (p) => (p && p.episodes ? Math.round((100 * p.imported) / p.episodes) : 0)
</script>

<div class="page-head">
  <div>
    <h1>Requests</h1>
    <p class="muted">{admin ? 'What people have asked for.' : 'What you have asked for, and how far along it is.'}</p>
  </div>
</div>

<div class="tabs">
  {#if admin}
    <button class:active={filter === 'pending'} onclick={() => (filter = 'pending')}>
      Waiting for approval {#if pending.length}<span class="badge warn">{pending.length}</span>{/if}
    </button>
  {/if}
  <button class:active={filter === 'all'} onclick={() => (filter = 'all')}>{admin ? 'All requests' : 'My requests'}</button>
</div>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if loaded && shown.length === 0}
  <div class="card empty muted">
    {#if filter === 'pending'}
      <p>Nothing is waiting for approval.</p>
    {:else}
      <p>No requests yet.</p>
      <a href="/discover" onclick={link}>Find something to request</a>
    {/if}
  </div>
{/if}

<div class="stack">
  {#each shown as r (r.id)}
    {@const [label, tone] = requestStates[r.status] ?? [r.status, '']}
    <div class="card req">
      <Poster url={r.posterUrl} title={r.title} />
      <div class="body">
        <div class="spread">
          <div>
            <div class="title">
              {#if r.progress}<a href={itemPath(r.type, r.progress.seriesId)} onclick={link}>{r.title}</a>{:else}{r.title}{/if}
              {#if r.year}<span class="muted">({r.year})</span>{/if}
            </div>
            <div class="muted small">
              {isBook(r.type) ? `${bookTypes[r.type]}${r.author ? ' by ' + r.author : ''}` : scopeLabel(r.seasons)} · {admin
                ? `${r.username}, `
                : ''}{when(r.createdAt)}
            </div>
          </div>
          <Badge {label} {tone} />
        </div>

        {#if r.reason}<div class="small reason" class:bad={r.status !== 'approved'}>{r.reason}</div>{/if}

        {#if r.progress && r.status !== 'pending'}
          <div class="progress">
            <div class="bar" class:good={r.status === 'available'}><div style="width: {pct(r.progress)}%"></div></div>
            <div class="muted small">
              {#if isBook(r.type)}
                {r.progress.imported ? 'In your library' : r.progress.downloading ? 'Downloading' : 'Searching for it'}
              {:else}
                {r.progress.imported} of {r.progress.episodes} episodes
                {#if r.progress.downloading}· {r.progress.downloading} downloading{/if}
              {/if}
            </div>
          </div>
        {/if}

        {#if r.status === 'pending'}
          <div class="row">
            {#if admin}
              <button class="primary" disabled={busy[r.id]} onclick={() => act(r, 'approve')}>
                {busy[r.id] ? 'Adding…' : 'Approve'}
              </button>
              <button disabled={busy[r.id]} onclick={() => act(r, 'deny')}>Deny</button>
            {:else}
              <button class="link small" onclick={() => act(r, 'cancel')}>Cancel request</button>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/each}
</div>

<style>
  .req {
    display: grid;
    grid-template-columns: 64px 1fr;
    gap: 14px;
    padding: 12px 14px;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  .title {
    font-weight: 600;
  }
  .title a {
    color: inherit;
  }
  .progress {
    display: grid;
    gap: 4px;
    max-width: 420px;
  }
  .reason {
    color: var(--muted);
  }
  .reason.bad {
    color: var(--bad);
  }
  .tabs .badge {
    margin-left: 4px;
  }
</style>
