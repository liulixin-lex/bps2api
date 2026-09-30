<template>
  <section class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-600 dark:bg-dark-800">
    <div class="mb-4 flex items-start justify-between gap-3">
      <div><h3 class="font-semibold">{{ t('tokenGuard.sessions.title') }}</h3><p class="mt-1 text-sm text-gray-500">{{ t('tokenGuard.sessions.description') }}</p></div>
      <button type="button" class="btn btn-secondary" :disabled="!configured || loading" @click="refresh">{{ t('tokenGuard.refresh') }}</button>
    </div>
    <p v-if="!configured" class="mb-3 text-sm text-amber-600">{{ t('tokenGuard.rotation.configureFirst') }}</p>
    <p v-if="disabled" class="mb-3 text-sm text-amber-600">{{ t('tokenGuard.rotation.saveFirst') }}</p>
    <form @submit.prevent="submit">
      <label for="logout-credentials" class="mb-2 block text-sm">{{ t('tokenGuard.sessions.credentials') }}</label>
      <textarea id="logout-credentials" v-model="input" rows="2" class="input w-full font-mono text-sm" autocomplete="off" spellcheck="false" :disabled="busy || Boolean(pending)" placeholder="email@example.com----password----CURRENT_TOTP_SECRET" aria-describedby="logout-input-hint" required />
      <p id="logout-input-hint" class="mt-2 text-xs text-gray-500">{{ t('tokenGuard.sessions.credentialsHint') }}</p>
      <p v-if="parsed" role="status" class="mt-2 text-sm text-emerald-600">{{ t('tokenGuard.sessions.recognized', { email: parsed.email }) }}</p>
      <p v-else-if="input.trim()" role="status" class="mt-2 text-sm text-amber-600">{{ t('tokenGuard.sessions.invalid') }}</p>
      <label class="my-4 flex items-start gap-2 text-sm"><input v-model="confirmed" type="checkbox" class="mt-1" /><span>{{ t('tokenGuard.sessions.confirm') }}</span></label>
      <div class="flex flex-wrap gap-2">
        <button type="submit" class="btn btn-primary" :disabled="!configured || disabled || !confirmed || busy || Boolean(pending)">{{ t('tokenGuard.sessions.submit') }}</button>
        <button v-if="pending" type="button" class="btn btn-secondary" :disabled="!configured || disabled || !confirmed || busy" @click="sendPending">{{ t('tokenGuard.rotation.retrySubmit') }}</button>
      </div>
    </form>
    <p v-if="message" role="status" class="mt-3 text-sm text-amber-600">{{ message }}</p>
    <p v-if="rejected" role="status" class="mt-3 text-sm text-amber-600">
      {{ rejected.email }} — {{ t('tokenGuard.submission.' + rejected.reason) }}
      <a v-for="job in relatedJobs()" :key="job.id" :href="'#logout-job-' + job.id" class="ml-2 underline">{{ t('tokenGuard.submission.relatedJob') }} {{ job.id }}</a>
    </p>
    <p class="mt-3 text-xs text-gray-500">{{ t('tokenGuard.sessions.scope') }}</p>
    <div v-if="jobs.length" class="mt-5 overflow-x-auto">
      <table class="w-full text-left text-sm"><thead><tr class="border-b dark:border-dark-600"><th class="p-2">{{ t('tokenGuard.account') }}</th><th class="p-2">{{ t('tokenGuard.sessions.status') }}</th></tr></thead>
        <tbody><tr v-for="job in jobs" :id="'logout-job-' + job.id" :key="job.id" class="border-b dark:border-dark-600">
          <td class="p-2"><div>{{ job.email }}</div><small class="text-gray-400">{{ job.id }}</small></td>
          <td class="p-2"><div>{{ stateText(job) }}</div><p v-if="detailText(job)" class="mt-1 max-w-xl text-xs text-amber-600">{{ detailText(job) }}</p></td>
        </tr></tbody>
      </table>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { listSessionLogouts, parseSessionLogoutText, startSessionLogout, type SessionLogoutJob } from '@/api/admin/accountSessionLogout'
import { credentialSubmissionRejection, type CredentialSubmissionRejection } from '@/api/admin/credentialSubmission'
import type { TokenGuardReloginAccount } from '@/api/admin/accountTokenGuard'
const props = defineProps<{ configured: boolean; disabled?: boolean; active: boolean }>()
const { t } = useI18n()
const input = ref(''), confirmed = ref(false), message = ref('')
const parsed = computed(() => {
  try { return parseSessionLogoutText(input.value) } catch { return null }
})
const busy = ref(false), loading = ref(false), jobs = ref<SessionLogoutJob[]>([])
const pending = ref<{ id: string; entry: TokenGuardReloginAccount } | null>(null)
const rejected = ref<{ email: string; reason: CredentialSubmissionRejection } | null>(null)
let alive = true
let timer: ReturnType<typeof setInterval> | undefined
const states = ['queued', 'logging_in', 'revoking', 'accepted', 'login_failed', 'failed', 'needs_review', 'interrupted']
const codes = ['login_failed', 'login_interaction_required', 'login_workspace_selection_failed', 'login_session_incomplete', 'invalid_credentials', 'account_die', 'logout_control_missing', 'logout_rejected', 'logout_unconfirmed', 'worker_interrupted', 'login_access_denied', 'login_rate_limited', 'identity_mismatch']
function stateText(job: SessionLogoutJob) { return t('tokenGuard.sessions.states.' + (states.includes(job.status) ? job.status : 'needs_review')) }
function detailText(job: SessionLogoutJob) {
  if (job.status === 'accepted') return t('tokenGuard.sessions.acceptedHint')
  if (!job.error_code) return ''
  return t('tokenGuard.sessions.errors.' + (codes.includes(job.error_code) ? job.error_code : 'logout_unconfirmed'))
}
function relatedJobs() {
  if (rejected.value?.reason !== 'account_has_unresolved_logout') return []
  return jobs.value.filter(job => job.email.trim().toLowerCase() === rejected.value?.email.trim().toLowerCase()
    && job.status === 'needs_review')
}
async function refresh() {
  if (!alive || !props.active || !props.configured || loading.value) return
  loading.value = true
  try { const result = await listSessionLogouts(); if (alive) jobs.value = result }
  catch { if (alive) message.value = t('tokenGuard.sessions.loadFailed') }
  finally { loading.value = false }
}
async function submit() {
  if (!confirmed.value || !props.configured || props.disabled || busy.value || pending.value) return
  if (!parsed.value) { message.value = t('tokenGuard.sessions.invalid'); return }
  pending.value = { id: crypto.randomUUID(), entry: { ...parsed.value } }
  input.value = ''
  rejected.value = null
  await sendPending()
}
async function sendPending() {
  if (!pending.value || busy.value || props.disabled || !props.configured || !confirmed.value) return
  busy.value = true; message.value = ''
  const request = pending.value
  try {
    await startSessionLogout(request.entry, request.id)
    request.entry.password = ''; request.entry.mfa_secret = ''; pending.value = null
    if (alive) message.value = t('tokenGuard.sessions.submitted')
  } catch (error) {
    if (alive) {
      const reason = credentialSubmissionRejection(error)
      if (reason) {
        rejected.value = { email: request.entry.email, reason }
        request.entry.password = ''; request.entry.mfa_secret = ''; pending.value = null
      } else {
        message.value = t('tokenGuard.sessions.submitUncertain')
      }
    }
  }
  finally { busy.value = false }
  await refresh()
}
watch(() => [props.active, props.configured], () => { void refresh() })
onMounted(() => { void refresh(); timer = setInterval(() => { void refresh() }, 5000) })
onBeforeUnmount(() => {
  alive = false; if (timer) clearInterval(timer)
  input.value = ''
  if (pending.value) { pending.value.entry.password = ''; pending.value.entry.mfa_secret = '' }
})
</script>
