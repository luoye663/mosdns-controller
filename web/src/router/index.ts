import { createRouter, createWebHistory } from 'vue-router'

import OverviewPage from '@/pages/OverviewPage.vue'
import LoginPage from '@/pages/LoginPage.vue'
import QueriesPage from '@/pages/QueriesPage.vue'
import RulesPage from '@/pages/RulesPage.vue'
import UnavailablePage from '@/pages/UnavailablePage.vue'
import VersionsPage from '@/pages/VersionsPage.vue'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginPage, meta: { public: true } },
    { path: '/', name: 'overview', component: OverviewPage },
    { path: '/queries', name: 'queries', component: QueriesPage },
    { path: '/rules', name: 'rules', component: RulesPage },
    { path: '/versions', name: 'versions', component: VersionsPage },
    { path: '/devices', name: 'devices', component: UnavailablePage, props: { title: '设备管理', endpoint: 'GET /api/v1/devices' } },
    { path: '/system', name: 'system', component: UnavailablePage, props: { title: '系统状态', endpoint: 'GET /api/v1/system/status' } },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.ready) await auth.restore()
  if (!to.meta.public && !auth.authenticated) return { name: 'login' }
  if (to.name === 'login' && auth.authenticated) return { name: 'overview' }
})

export default router
