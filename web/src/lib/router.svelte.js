// A tiny history router: the server serves index.html for any non-API path,
// so plain URLs like /settings/indexers survive a reload.

export const route = $state({ path: window.location.pathname })

export function navigate(path, replace = false) {
  if (path === route.path) return
  if (replace) window.history.replaceState({}, '', path)
  else window.history.pushState({}, '', path)
  route.path = path
}

window.addEventListener('popstate', () => {
  route.path = window.location.pathname
})

// Use as onclick on <a href="..."> to navigate without a page load.
export function link(event) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey) return
  event.preventDefault()
  navigate(event.currentTarget.getAttribute('href'))
}
