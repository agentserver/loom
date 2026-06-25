import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  base: '/commander/',
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:18091',
      '/ws': { target: 'http://127.0.0.1:18091', ws: true },
    },
  },
  build: {
    outDir: '../assets/dist',
    emptyOutDir: true,
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: './src/test/setup.ts',
  },
});
