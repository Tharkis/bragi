import { writeFileSync } from 'node:fs'
import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Vite empties dist/ on every build. Put the .gitkeep back so the Go
// //go:embed directive always has a folder to embed.
const keepDist = {
  name: 'bragi-keep-dist',
  closeBundle() {
    writeFileSync('dist/.gitkeep', '')
  },
}

export default defineConfig({
  plugins: [svelte(), keepDist],
  server: {
    // `npm run dev` talks to a Bragi server on :8787.
    proxy: { '/api': 'http://localhost:8787' },
  },
  build: { outDir: 'dist', emptyOutDir: true },
})
