<script>
  let { url, title, size = 'md' } = $props()
</script>

{#if url}
  <img class="poster {size}" src={url} alt="" loading="lazy" />
{:else}
  <div class="poster {size} blank" aria-hidden="true">{title?.[0] ?? '?'}</div>
{/if}

<style>
  .poster {
    display: block;
    aspect-ratio: 2 / 3;
    object-fit: cover;
    border-radius: 8px;
    background: var(--surface-2);
    width: 100%;
  }
  .sm {
    width: 44px;
    border-radius: 5px;
  }
  .lg {
    width: 180px;
  }
  .blank {
    display: grid;
    place-items: center;
    color: var(--muted);
    font-size: 2rem;
    font-weight: 700;
  }
  .sm.blank {
    font-size: 1rem;
  }
</style>
