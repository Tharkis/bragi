<script>
  // Add or edit an indexer or download client. The fields come from the
  // server's kind registry, so a new adapter needs no UI change.
  import { post, put } from '../lib/api.js'

  let { endpoint, kinds, initial = null, onSaved, onCancel = null, saveLabel = 'Save' } = $props()

  const blank = {
    id: 0,
    name: '',
    kind: '',
    baseUrl: '',
    apiKey: '',
    username: '',
    password: '',
    category: '',
    categories: {},
    remotePath: '',
    localPath: '',
    enabled: true,
    priority: 25,
  }
  // The form is seeded once; callers re-key it to load another item.
  // svelte-ignore state_referenced_locally
  let item = $state({ ...blank, kind: kinds[0]?.id ?? '', ...(initial ?? {}) })
  item.categories ??= {}
  let test = $state(null) // { ok, error, warnings }
  let error = $state('')
  let busy = $state(false)

  const kind = $derived(kinds.find((k) => k.id === item.kind))
  const basic = $derived((kind?.fields ?? []).filter((f) => !f.advanced))
  const advanced = $derived((kind?.fields ?? []).filter((f) => f.advanced))

  // Field names like "categories.tv" live in a nested object.
  function getField(name) {
    const [head, key] = name.split('.')
    return (key ? item[head]?.[key] : item[head]) ?? ''
  }
  function setField(name, value) {
    const [head, key] = name.split('.')
    if (key) item[head] = { ...item[head], [key]: value }
    else item[head] = value
  }
  // Open "More options" when any of them is already set.
  // svelte-ignore state_referenced_locally
  let showMore = $state(advanced.some((f) => getField(f.name) !== ''))

  // A changed setting makes an earlier test result stale.
  $effect(() => {
    JSON.stringify(item)
    test = null
  })

  async function runTest() {
    busy = true
    error = ''
    try {
      const result = await post(endpoint + '/test', item)
      test = result
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  async function save(event) {
    event.preventDefault()
    busy = true
    error = ''
    try {
      const saved = item.id ? await put(`${endpoint}/${item.id}`, item) : await post(endpoint, item)
      onSaved(saved)
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<form onsubmit={save}>
  <label>
    <span>Type</span>
    <select bind:value={item.kind} disabled={!!item.id}>
      {#each kinds as k (k.id)}
        <option value={k.id}>{k.label}</option>
      {/each}
    </select>
  </label>
  <label>
    <span>Name</span>
    <input bind:value={item.name} placeholder={kind?.label ?? ''} />
  </label>
  {#snippet field(f)}
    <label>
      <span>{f.label}{f.required ? '' : ' (optional)'}</span>
      <input
        type={f.type === 'password' ? 'password' : 'text'}
        bind:value={() => getField(f.name), (v) => setField(f.name, v)}
        placeholder={f.placeholder ?? ''}
        required={f.required}
        autocomplete="off"
      />
      {#if f.help}<small>{f.help}</small>{/if}
    </label>
  {/snippet}
  {#each basic as f (f.name)}{@render field(f)}{/each}
  {#if advanced.length}
    <button type="button" class="link more" onclick={() => (showMore = !showMore)} aria-expanded={showMore}>
      {showMore ? '▾' : '▸'} More options: categories per media type, path mapping
    </button>
    {#if showMore}
      <div class="more-box">
        {#each advanced as f (f.name)}{@render field(f)}{/each}
      </div>
    {/if}
  {/if}
  <label class="check"><input type="checkbox" bind:checked={item.enabled} /> <span>Enabled</span></label>

  {#if test?.ok}
    <div class="notice good">Connected to {kind?.label}.</div>
    {#each test.warnings ?? [] as w}<div class="notice warn">{w}</div>{/each}
  {:else if test}
    <div class="notice bad">{test.error}</div>
  {/if}
  {#if error}<div class="notice bad">{error}</div>{/if}

  <div class="row">
    <button type="button" onclick={runTest} disabled={busy}>Test</button>
    <button class="primary" disabled={busy}>{saveLabel}</button>
    {#if onCancel}<button type="button" class="link" onclick={onCancel}>Cancel</button>{/if}
  </div>
</form>

<style>
  .more {
    align-self: flex-start;
    padding: 0;
    margin: 2px 0 4px;
  }
  .more-box {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 12px 16px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-2);
  }
</style>
