import { defineStore } from 'pinia'
import { api, clearCSRF, setCSRF } from '@/lib/api'

export const useAuthStore = defineStore('auth', {
  state: () => ({ username: '', ready: false }),
  getters: { authenticated: (state) => Boolean(state.username) },
  actions: {
    async restore() {
      try { this.username = (await api.me()).username } catch { this.username = '' }
      this.ready = true
    },
    async login(username: string, password: string) {
      const result = await api.login(username, password)
      setCSRF(result.csrf_token)
      this.username = (await api.me()).username
    },
    async logout() { try { await api.logout() } finally { clearCSRF(); this.username = '' } },
  },
})
