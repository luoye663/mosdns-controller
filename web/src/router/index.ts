import { createRouter, createWebHistory } from 'vue-router'

import OverviewPage from '@/pages/OverviewPage.vue'

export default createRouter({
  history: createWebHistory(),
  routes: [{ path: '/', name: 'overview', component: OverviewPage }],
})
