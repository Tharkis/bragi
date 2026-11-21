// Live updates from /api/v1/events. Pages call onEvents(types, fn) and get
// a debounced callback when something they show changes.

let source = null
const listeners = new Set()

function connect() {
  if (source) return
  source = new EventSource('/api/v1/events', { withCredentials: true })
  for (const type of ['request', 'series', 'episodes', 'download', 'scan']) {
    source.addEventListener(type, (e) => {
      let data = null
      try {
        data = JSON.parse(e.data)
      } catch {}
      for (const l of listeners) if (l.types.includes(type)) l.fire(data)
    })
  }
}

export function onEvents(types, fn, delay = 400) {
  connect()
  let timer = null
  const l = {
    types,
    fire(data) {
      clearTimeout(timer)
      timer = setTimeout(() => fn(data), delay)
    },
  }
  listeners.add(l)
  return () => {
    clearTimeout(timer)
    listeners.delete(l)
  }
}
