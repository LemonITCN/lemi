<script setup lang="ts">
import Toasts from '@/components/ui/Toasts.vue'
import Confirm from '@/components/ui/Confirm.vue'
import ReauthDialog from '@/components/ReauthDialog.vue'
</script>

<template>
  <RouterView />
  <Toasts />
  <Confirm />
  <ReauthDialog />
</template>
