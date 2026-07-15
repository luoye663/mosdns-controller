import { defineStore } from 'pinia'
import { api, clearCSRF, setCSRF } from '@/lib/api'

export const useAuthStore = defineStore('auth', {
  state: () => ({ username: '', ready: false, setupRequired: false }),
  getters: { authenticated: (state) => Boolean(state.username) },
  actions: {
    async restore() {
      try {
        this.username = (await api.me()).username
        this.setupRequired = false
      } catch {
        this.username = ''
        try { this.setupRequired = (await api.bootstrapStatus()).required }
        catch { this.setupRequired = false }
      }
      this.ready = true
    },
    async login(username: string, password: string) {
      const result = await api.login(username, password)
      setCSRF(result.csrf_token)
      this.username = (await api.me()).username
    },
    async bootstrap(username: string, password: string) {
      const result = await api.bootstrap(username, password)
      setCSRF(result.csrf_token)
      this.username = (await api.me()).username
      this.setupRequired = false
    },
    async logout() { try { await api.logout() } finally { clearCSRF(); this.username = '' } },
  },
})
