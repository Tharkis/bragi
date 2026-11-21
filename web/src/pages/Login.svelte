<script>
  import { post } from '../lib/api.js'

  let { onDone } = $props()
  let username = $state('')
  let password = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(event) {
    event.preventDefault()
    error = ''
    busy = true
    try {
      onDone(await post('/auth/login', { username, password }))
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<div class="center-page">
  <form class="narrow card" onsubmit={submit}>
    <div class="brand" style="margin-bottom: 16px"><img src="/icon.svg" alt="" /> Bragi</div>
    {#if error}<div class="notice bad">{error}</div>{/if}
    <label><span>Username</span><input bind:value={username} autocomplete="username" required /></label>
    <label
      ><span>Password</span><input type="password" bind:value={password} autocomplete="current-password" required
      /></label
    >
    <button class="primary" disabled={busy}>Sign in</button>
  </form>
</div>
