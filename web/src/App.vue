<script setup lang="ts">
import { RouterView } from 'vue-router'
import { NConfigProvider, NLayout, NLayoutContent, NLayoutHeader } from 'naive-ui'
</script>

<template>
  <NConfigProvider>
    <NLayout class="shell">
      <NLayoutHeader bordered class="header">mosdns 管理平台</NLayoutHeader>
      <NLayoutContent content-style="padding: 24px">
        <RouterView />
      </NLayoutContent>
    </NLayout>
  </NConfigProvider>
</template>
