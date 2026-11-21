<script>
  import { onMount } from 'svelte'
  import { get, post, del } from '../lib/api.js'
  import IntegrationForm from './IntegrationForm.svelte'

  // role: 'indexer' | 'downloader'
  let { role, endpoint, title, intro } = $props()

  let kinds = $state([])
  let items = $state([])
  let editing = $state(null) // null, 'new', or an item
  let results = $state({}) // id -> test result
  let error = $state('')

  async function load() {
    try {
      const all = await get('/integrations/kinds')
      kinds = all.filter((k) => k.role === role)
      items = await get(endpoint)
    } catch (e) {
      error = e.message
    }
  }
  onMount(load)

  const label = (id) => kinds.find((k) => k.id === id)?.label ?? id

  async function test(item) {
    results[item.id] = { pending: true }
    try {
      results[item.id] = await post(`${endpoint}/${item.id}/test`)
    } catch (e) {
      results[item.id] = { ok: false, error: e.message }
    }
  }

  async function remove(item) {
    if (!window.confirm(`Remove ${item.name}?`)) return
    try {
      await del(`${endpoint}/${item.id}`)
      await load()
    } catch (e) {
      error = e.message
    }
  }

  async function saved() {
    editing = null
    await load()
  }
</script>

<div class="spread" style="margin-bottom: 12px">
  <div>
    <h2 style="margin: 0">{title}</h2>
    <p class="muted small" style="margin: 0">{intro}</p>
  </div>
  {#if editing === null}<button class="primary" onclick={() => (editing = 'new')}>Add</button>{/if}
</div>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if editing !== null && kinds.length}
  <div class="card" style="margin-bottom: 16px">
    {#key editing}
      <IntegrationForm
        {endpoint}
        {kinds}
        initial={editing === 'new' ? null : editing}
        onSaved={saved}
        onCancel={() => (editing = null)}
      />
    {/key}
  </div>
{/if}

{#if items.length}
  <div class="card" style="padding: 4px 12px">
    <table>
      <thead><tr><th>Name</th><th>Type</th><th>Status</th><th></th></tr></thead>
      <tbody>
        {#each items as item (item.id)}
          <tr>
            <td>
              <div>{item.name}</div>
              <div class="muted small">{item.baseUrl}</div>
            </td>
            <td>{label(item.kind)}</td>
            <td>
              <span class="badge" class:on={item.enabled}>{item.enabled ? 'Enabled' : 'Disabled'}</span>
              {#if results[item.id]?.pending}
                <div class="muted small">Testing…</div>
              {:else if results[item.id]?.ok}
                <div class="small" style="color: var(--good)">Connected</div>
              {:else if results[item.id]}
                <div class="small" style="color: var(--bad)">{results[item.id].error}</div>
              {/if}
            </td>
            <td class="actions">
              <button onclick={() => test(item)}>Test</button>
              <button onclick={() => (editing = item)}>Edit</button>
              <button class="danger" onclick={() => remove(item)}>Remove</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{:else if editing === null}
  <div class="card muted">None yet.</div>
{/if}
