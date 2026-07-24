import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const backendOrigin = (env.VITE_BACKEND_ORIGIN || 'http://127.0.0.1:8080').replace(/\/$/, '')

  return {
    plugins: [react()],
    server: {
      host: '127.0.0.1',
      port: 5174,
      strictPort: false,
      proxy: {
        '/api': {
          target: backendOrigin,
          changeOrigin: true,
        },
      },
    },
    preview: {
      host: '127.0.0.1',
      port: 4174,
      strictPort: false,
    },
  }
})
