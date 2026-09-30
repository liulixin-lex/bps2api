<template>
  <section class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-600 dark:bg-dark-800">
    <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
      <div><h3 class="font-semibold">{{ t('tokenGuard.rotation.title') }}</h3><p class="mt-1 text-sm text-gray-500">{{ t('tokenGuard.rotation.description') }}</p></div>
      <button class="btn btn-secondary" :disabled="!configured || loading" @click="refresh">{{ t('tokenGuard.refresh') }}</button>
    </div>
    <p v-if="!configured" class="mb-3 text-sm text-amber-600">{{ t('tokenGuard.rotation.configureFirst') }}</p>
    <p v-if="disabled" class="mb-3 text-sm text-amber-600">{{ t('tokenGuard.rotation.saveFirst') }}</p>
    <form @submit.prevent="startBatch">
      <label for="rotation-input" class="mb-2 block text-sm">{{ t('tokenGuard.rotation.input') }}</label>
      <textarea id="rotation-input" v-model="input" rows="3" class="input w-full font-mono text-sm" autocomplete="off" spellcheck="false" :disabled="busy || pending.length > 0" placeholder="email@example.com----password----CURRENT_TOTP_SECRET" />
      <label class="my-3 flex items-start gap-2 text-sm"><input v-model="confirmed" type="checkbox" class="mt-1" /><span>{{ t('tokenGuard.rotation.confirm') }}</span></label>
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-primary" :disabled="!configured || disabled || !confirmed || busy || pending.length > 0">{{ t('tokenGuard.rotation.start') }}</button>
        <button v-if="pending.length" type="button" class="btn btn-secondary" :disabled="busy || disabled || !confirmed" @click="submitPending">{{ t('tokenGuard.rotation.retrySubmit') }} ({{ pending.length }})</button>
      </div>
    </form>
    <p v-if="message" role="status" class="mt-3 text-sm text-amber-600">{{ message }}</p>
    <div v-if="jobs.length" class="mt-4 overflow-x-auto">
      <table class="w-full text-left text-sm">
        <thead><tr class="border-b dark:border-dark-600"><th class="p-2">{{ t('tokenGuard.account') }}</th><th class="p-2">{{ t('tokenGuard.rotation.status') }}</th><th class="p-2">{{ t('tokenGuard.actions') }}</th></tr></thead>
        <tbody>
          <tr v-for="job in jobs" :key="job.id" class="border-b dark:border-dark-600">
            <td class="p-2"><div>{{ job.email }}</div><small class="text-gray-400">{{ job.id }}</small></td>
            <td class="p-2"><div>{{ stateText(job) }}</div><p v-if="failureText(job)" class="mt-1 max-w-lg text-xs text-amber-600">{{ failureText(job) }}</p></td>
            <td class="p-2">
              <div class="flex flex-wrap gap-2">
                <button v-if="job.retryable" type="button" class="btn btn-secondary" :disabled="actionBusy === job.id || disabled" @click="verify(job)">{{ t('tokenGuard.rotation.verify') }}</button>
                <button v-if="latestVerified(job)" type="button" class="btn btn-secondary" :disabled="actionBusy === job.id" @click="copyResult(job)">{{ t('tokenGuard.rotation.copy') }}</button>
                <button v-if="showApply && latestVerified(job)" type="button" class="btn btn-secondary" :disabled="actionBusy === job.id || disabled" @click="apply(job)">{{ t('tokenGuard.rotation.apply') }}</button>
                <span v-if="job.status === 'success' && job.login_verified && !latestVerified(job)" class="max-w-xs text-xs text-gray-500">{{ t('tokenGuard.rotation.superseded') }}</span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TokenGuardReloginAccount } from '@/api/admin/accountTokenGuard'
import {
  formatTwoFARotationCredentials, getTwoFARotationResult, listTwoFARotations, parseTwoFARotationText,
  startTwoFARotation, verifyTwoFARotation, type TwoFARotationJob, type TwoFARotationResult
} from '@/api/admin/accountTwoFARotation'

const props = withDefaults(defineProps<{ configured: boolean; disabled?: boolean; showApply?: boolean }>(), { showApply: true })
const emit = defineEmits<{ apply: [result: TwoFARotationResult] }>()
const { t } = useI18n()
const input = ref(''), confirmed = ref(false), message = ref(''), actionBusy = ref('')
const busy = ref(false), loading = ref(false)
const jobs = ref<TwoFARotationJob[]>([])
const pending = ref<{ id: string; entry: TokenGuardReloginAccount }[]>([])
let alive = true
let timer: ReturnType<typeof setInterval> | undefined

function stateText(job: TwoFARotationJob) {
  const state = job.rotated_pending_verify ? 'pendingVerify' : job.status
  return t('tokenGuard.rotation.states.' + (['queued', 'running', 'success', 'error', 'cancelled', 'needs_review', 'pendingVerify', 'login_failed', 'preflight_failed'].includes(state) ? state : 'needs_review'))
}

function failureText(job: TwoFARotationJob) {
  if (job.status === 'needs_review' && !job.rotated_pending_verify) {
    const allowed = ['rotation_disable_server_error', 'rotation_disable_rejected']
    const code = allowed.includes(job.error_code ?? '') ? job.error_code : 'rotation_unconfirmed'
    return t('tokenGuard.rotation.errors.' + code)
  }
  if (!['login_failed', 'preflight_failed'].includes(job.status) || job.rotated_pending_verify) return ''
  const allowed = ['login_access_denied', 'login_rate_limited', 'login_bootstrap_rejected',
    'login_interaction_required', 'login_workspace_selection_failed', 'login_session_incomplete', 'login_state_invalid', 'invalid_credentials', 'account_die',
    'login_failed', 'preflight_failed']
  const code = allowed.includes(job.error_code ?? '') ? job.error_code : 'login_failed'
  return t('tokenGuard.rotation.errors.' + code)
}

function latestVerified(job: TwoFARotationJob) {
  // The worker returns newest first and restricts result export to that job.
  return job.status === 'success' && job.login_verified
    && jobs.value.find(candidate => candidate.email === job.email)?.id === job.id
}

async function refresh() {
  if (!props.configured || loading.value || !alive) return
  loading.value = true
  try {
    const result = await listTwoFARotations()
    if (alive) jobs.value = result
  } catch {
    if (alive) message.value = t('tokenGuard.rotation.loadFailed')
  } finally { loading.value = false }
}

async function startBatch() {
  if (!confirmed.value || !props.configured || props.disabled || busy.value || pending.value.length) return
  try {
    pending.value = parseTwoFARotationText(input.value).map(entry => ({ id: crypto.randomUUID(), entry }))
  } catch { message.value = t('tokenGuard.twoFA.invalid'); return }
  input.value = ''
  await submitPending()
}

async function submitPending() {
  if (busy.value || !confirmed.value || props.disabled || !props.configured) return
  busy.value = true
  message.value = ''
  for (const item of [...pending.value]) {
    if (!alive) break
    try {
      await startTwoFARotation(item.entry, item.id)
      // An uncertain request keeps the SAME id and payload for a safe retry.
      pending.value = pending.value.filter(value => value.id !== item.id)
      item.entry.password = ''; item.entry.mfa_secret = ''
    } catch {
      if (alive) message.value = t('tokenGuard.rotation.submitUncertain')
      break
    }
  }
  busy.value = false
  await refresh()
}

async function verify(job: TwoFARotationJob) {
  if (!job.retryable || props.disabled || actionBusy.value) return
  actionBusy.value = job.id
  try { await verifyTwoFARotation(job.id); await refresh() }
  catch { message.value = t('tokenGuard.rotation.actionFailed') }
  finally { actionBusy.value = '' }
}

async function copyResult(job: TwoFARotationJob) {
  if (actionBusy.value) return
  actionBusy.value = job.id
  try {
    const result = await getTwoFARotationResult(job.id)
    if (!alive) return
    await navigator.clipboard.writeText(formatTwoFARotationCredentials(result))
    message.value = t('tokenGuard.rotation.copied')
  } catch { if (alive) message.value = t('tokenGuard.rotation.actionFailed') }
  finally { actionBusy.value = '' }
}

async function apply(job: TwoFARotationJob) {
  if (props.disabled || actionBusy.value) return
  actionBusy.value = job.id
  try {
    const result = await getTwoFARotationResult(job.id)
    if (alive) emit('apply', result)
  } catch { if (alive) message.value = t('tokenGuard.rotation.actionFailed') }
  finally { actionBusy.value = '' }
}

watch(() => props.configured, () => { void refresh() })
onMounted(() => {
  void refresh()
  timer = setInterval(() => {
    if (jobs.value.some(job => job.status === 'queued' || job.status === 'running') || busy.value) void refresh()
  }, 3000)
})
onBeforeUnmount(() => {
  alive = false
  if (timer) clearInterval(timer)
  input.value = ''
  for (const item of pending.value) { item.entry.password = ''; item.entry.mfa_secret = '' }
  pending.value = []
})
</script>
