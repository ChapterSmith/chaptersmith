import { defineConfig } from 'vite';

// BOOK_UI_MOCK=1 swaps the native bridge for dev/mock-runtime.js so the
// interface can be reviewed in a plain browser (npm run dev:mock).
const mock = process.env.BOOK_UI_MOCK === '1';

export default defineConfig({
  resolve: mock
    ? { alias: { '@wailsio/runtime': new URL('./dev/mock-runtime.js', import.meta.url).pathname } }
    : {},
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: mock ? 5174 : 5173,
    hmr: process.env.BOOK_NATIVE_DEV === '1' ? false : undefined,
    strictPort: true,
  },
});
