<template>
  <div v-if="active" data-testid="task-progress" class="space-y-1">
    <div role="status" class="flex items-center gap-2 text-sm" :class="stale ? 'text-amber-600' : 'text-primary-600 dark:text-primary-400'">
      <span v-if="!stale" data-testid="progress-spinner" aria-hidden="true" class="h-4 w-4 shrink-0 animate-spin rounded-full border-2 border-current border-t-transparent" />
      <span>{{ stale ? t('tokenGuard.progress.stale') : phaseText }}</span>
    </div>
    <p v-if="stale" class="text-xs text-gray-500">{{ t('tokenGuard.progress.lastKnown', { phase: phaseText }) }}</p>
    <p v-else class="text-xs text-gray-500">
      {{ t('tokenGuard.progress.elapsed', { seconds: elapsed }) }}
      <span v-if="phaseElapsed !== null"> · {{ t('tokenGuard.progress.phaseElapsed', { seconds: phaseElapsed }) }}</span>
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
const props = defineProps<{
  active: boolean
  stale: boolean
  fallback: string
  job: { created_at: number; started_at?: number; phase?: string; phase_started_at?: number }
}>()
const { t } = useI18n()
const phases = ['login_start', 'password', 'mfa', 'mfa_retry', 'workspace', 'session', 'preflight', 'rotating', 'logout_preflight', 'revoking',
  'verify_login_start', 'verify_password', 'verify_mfa', 'verify_mfa_retry', 'verify_workspace', 'verify_session', 'verify_preflight']
const phaseText = computed(() => phases.includes(props.job.phase ?? '') ? t('tokenGuard.progress.phases.' + props.job.phase) : props.fallback)
const now = ref(Date.now() / 1000)
function secondsSince(value?: number) { return value && Number.isFinite(value) ? Math.max(0, Math.floor(now.value - value)) : 0 }
const elapsed = computed(() => secondsSince(props.job.started_at ?? props.job.created_at))
const phaseElapsed = computed(() => props.job.phase_started_at && phases.includes(props.job.phase ?? '') ? secondsSince(props.job.phase_started_at) : null)
let timer: ReturnType<typeof setInterval> | undefined
watch(() => props.active && !props.stale, running => {
  if (timer) clearInterval(timer)
  timer = undefined
  if (running) {
    now.value = Date.now() / 1000
    timer = setInterval(() => { now.value = Date.now() / 1000 }, 1000)
  }
}, { immediate: true })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>
