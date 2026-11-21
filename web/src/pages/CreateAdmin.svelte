<script>
  import { post } from '../lib/api.js'

  let { onDone } = $props()
  let username = $state('admin')
  let password = $state('')
  let confirm = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(event) {
    event.preventDefault()
    error = ''
    if (password.length < 8) return (error = 'Use at least 8 characters.')
    if (password !== confirm) return (error = 'The passwords do not match.')
    busy = true
    try {
      onDone(await post('/setup', { username, password }))
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
    <h1>Welcome</h1>
    <p class="muted">Create the admin account. You will use it to set up Bragi and approve requests.</p>
    {#if error}<div class="notice bad">{error}</div>{/if}
    <label><span>Username</span><input bind:value={username} autocomplete="username" required /></label>
    <label
      ><span>Password</span><input type="password" bind:value={password} autocomplete="new-password" required />
      <small>At least 8 characters.</small></label
    >
    <label
      ><span>Confirm password</span><input type="password" bind:value={confirm} autocomplete="new-password" required
      /></label
    >
    <button class="primary" disabled={busy}>Create account</button>
  </form>
</div>
