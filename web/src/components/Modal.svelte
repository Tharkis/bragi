<script>
  let { title, onClose, wide = false, children } = $props()

  function key(e) {
    if (e.key === 'Escape') onClose()
  }
</script>

<svelte:window onkeydown={key} />
<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop" onclick={onClose}>
  <div class="dialog card" class:wide role="dialog" aria-modal="true" aria-label={title} onclick={(e) => e.stopPropagation()}>
    <div class="spread head">
      <h2>{title}</h2>
      <button class="link" onclick={onClose} aria-label="Close">✕</button>
    </div>
    {@render children()}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgb(0 0 0 / 0.45);
    display: grid;
    place-items: center;
    padding: 16px;
    z-index: 10;
  }
  .dialog {
    width: 100%;
    max-width: 480px;
    max-height: calc(100vh - 32px);
    overflow: auto;
  }
  .dialog.wide {
    max-width: 900px;
  }
  .head {
    margin-bottom: 8px;
  }
  .head h2 {
    margin: 0;
  }
</style>
