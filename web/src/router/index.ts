import { createRouter, createWebHistory } from 'vue-router'

import OverviewPage from '@/pages/OverviewPage.vue'
import LoginPage from '@/pages/LoginPage.vue'
import SetupPage from '@/pages/SetupPage.vue'
import QueriesPage from '@/pages/QueriesPage.vue'
import RulesPage from '@/pages/RulesPage.vue'
import DevicesPage from '@/pages/DevicesPage.vue'
import SystemPage from '@/pages/SystemPage.vue'
import VersionsPage from '@/pages/VersionsPage.vue'
import UpstreamsPage from '@/pages/UpstreamsPage.vue'
import SettingsPage from '@/pages/SettingsPage.vue'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginPage, meta: { public: true } },
    { path: '/setup', name: 'setup', component: SetupPage, meta: { public: true } },
    { path: '/', name: 'overview', component: OverviewPage },
    { path: '/queries', name: 'queries', component: QueriesPage },
    { path: '/rules', name: 'rules', component: RulesPage },
    { path: '/versions', name: 'versions', component: VersionsPage },
    { path: '/devices', name: 'devices', component: DevicesPage },
    { path: '/system', name: 'system', component: SystemPage },
    { path: '/upstreams', name: 'upstreams', component: UpstreamsPage },
    { path: '/settings', name: 'settings', component: SettingsPage },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.ready) await auth.restore()
  if (auth.setupRequired && to.name !== 'setup') return { name: 'setup' }
  if (!auth.setupRequired && to.name === 'setup') return auth.authenticated ? { name: 'overview' } : { name: 'login' }
  if (!to.meta.public && !auth.authenticated) return { name: 'login' }
  if (to.name === 'login' && auth.authenticated) return { name: 'overview' }
})

export default router
