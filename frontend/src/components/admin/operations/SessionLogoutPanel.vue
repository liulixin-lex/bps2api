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
        <button type="submit" class="btn btn-primary" :disabled="!configured || disabled || !confirmed || busy || Boolean(pending)"><span v-if="busy" aria-hidden="true" class="mr-2 inline-block h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" />{{ busy ? t('tokenGuard.progress.submitting') : t('tokenGuard.sessions.submit') }}</button>
        <button v-if="pending" type="button" class="btn btn-secondary" :disabled="!configured || disabled || !confirmed || busy" @click="sendPending">{{ t('tokenGuard.rotation.retrySubmit') }}</button>
      </div>
    </form>
    <div v-if="busy || activeJobs.length" data-testid="active-task-summary" class="mt-4 space-y-3 rounded-lg border border-blue-200 bg-blue-50 p-3 dark:border-blue-900 dark:bg-blue-950/30">
      <p class="text-sm font-medium">{{ t('tokenGuard.progress.title') }}</p>
      <p v-if="busy" role="status" class="flex items-center gap-2 text-sm"><span v-if="busy" aria-hidden="true" class="mr-2 inline-block h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" />{{ t('tokenGuard.progress.submitting') }}</p>
      <div v-for="job in activeJobs" :key="job.id">
        <p class="mb-1 text-xs text-gray-500">{{ job.email }}</p>
        <CredentialTaskProgress :job="job" :active="true" :stale="statusStale" :fallback="stateText(job)" />
      </div>
      <p class="text-xs text-gray-500">{{ t('tokenGuard.progress.hint') }}</p>
    </div>
    <p v-if="message" role="status" class="mt-3 text-sm text-amber-600">{{ message }}</p>
    <p v-if="rejected" role="status" class="mt-3 text-sm text-amber-600">
      {{ rejected.email }} — {{ t('tokenGuard.submission.' + rejected.reason) }}
      <a v-for="job in relatedJobs()" :key="job.id" :href="'#logout-job-' + job.id" @click.prevent="showRelatedJob(job.id)" class="ml-2 underline">{{ t('tokenGuard.submission.relatedJob') }} {{ job.id }}</a>
    </p>
    <p class="mt-3 text-xs text-gray-500">{{ t('tokenGuard.sessions.scope') }}</p>
    <div v-if="jobs.length" class="mt-5">
      <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
        <p class="text-xs text-gray-500">{{ t('tokenGuard.history.logoutDeleteHint') }}</p>
        <button type="button" data-testid="delete-selected" class="btn btn-secondary" :disabled="!selected.length || deleting || loading || disabled" @click="deleteSelected">{{ t('tokenGuard.history.deleteSelected', { count: selected.length }) }}</button>
      </div>
      <div class="overflow-x-auto">
      <table class="w-full text-left text-sm"><thead><tr class="border-b dark:border-dark-600"><th class="p-2"><input type="checkbox" data-testid="select-page" :checked="allPageSelected" :disabled="!selectableJobs.length || deleting || loading || disabled" :aria-label="t('tokenGuard.history.selectPage')" @change="togglePageSelection" /></th><th class="p-2">{{ t('tokenGuard.account') }}</th><th class="p-2">{{ t('tokenGuard.sessions.status') }}</th></tr></thead>
        <tbody><tr v-for="job in visibleJobs" :id="'logout-job-' + job.id" :key="job.id" class="border-b dark:border-dark-600">
          <td class="p-2"><input v-model="selected" type="checkbox" :value="job.id" data-testid="select-history-row" :disabled="!canDelete(job) || deleting || loading || disabled" :aria-label="t('tokenGuard.history.selectRecord', { email: job.email })" /></td>
          <td class="p-2"><div>{{ job.email }}</div><small class="text-gray-400">{{ job.id }}</small></td>
          <td class="p-2"><div class="flex items-center gap-2"><span v-if="isActive(job) && !statusStale" aria-hidden="true" class="h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" />{{ stateText(job) }}</div><p v-if="detailText(job)" class="mt-1 max-w-xl text-xs text-amber-600">{{ detailText(job) }}</p></td>
        </tr></tbody>
      </table>
      </div>
      <Pagination v-model:page="page" :total="jobs.length" :page-size="pageSize" :show-page-size-selector="false" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Pagination from '@/components/common/Pagination.vue'
import CredentialTaskProgress from './CredentialTaskProgress.vue'
import { deleteSessionLogouts, listSessionLogouts, parseSessionLogoutText, startSessionLogout, type SessionLogoutJob } from '@/api/admin/accountSessionLogout'
import { credentialSubmissionRejection, type CredentialSubmissionRejection } from '@/api/admin/credentialSubmission'
import type { TokenGuardReloginAccount } from '@/api/admin/accountTokenGuard'
const props = defineProps<{ configured: boolean; disabled?: boolean; active: boolean }>()
const { t } = useI18n()
const input = ref(''), confirmed = ref(false), message = ref('')
const parsed = computed(() => {
  try { return parseSessionLogoutText(input.value) } catch { return null }
})
const busy = ref(false), loading = ref(false), jobs = ref<SessionLogoutJob[]>([])
const statusStale = ref(false)
const activeJobs = computed(() => jobs.value.filter(isActive))
const page = ref(1), pageSize = 10
const selected = ref<string[]>([]), deleting = ref(false)
const selectableJobs = computed(() => visibleJobs.value.filter(canDelete))
const allPageSelected = computed(() => selectableJobs.value.length > 0 && selectableJobs.value.every(job => selected.value.includes(job.id)))
function isActive(job: SessionLogoutJob) { return ['queued', 'logging_in', 'revoking'].includes(job.status) }
function canDelete(job: SessionLogoutJob) { return job.deletable !== false && ['accepted', 'login_failed', 'failed', 'interrupted'].includes(job.status) }
function togglePageSelection() {
  const ids = selectableJobs.value.map(job => job.id)
  selected.value = allPageSelected.value ? selected.value.filter(id => !ids.includes(id)) : [...new Set([...selected.value, ...ids])]
}
watch(page, () => { selected.value = [] })
async function deleteSelected() {
  if (deleting.value || loading.value || props.disabled || !props.configured || !selected.value.length) return
  deleting.value = true
  try {
    const result = await deleteSessionLogouts([...selected.value])
    if (!alive) return
    jobs.value = jobs.value.filter(job => !result.deleted_ids.includes(job.id))
    selected.value = []
    message.value = t('tokenGuard.history.deleted')
  } catch { if (alive) message.value = t('tokenGuard.history.deleteFailed') }
  finally { deleting.value = false; await refresh() }
}
const visibleJobs = computed(() => jobs.value.slice((page.value - 1) * pageSize, page.value * pageSize))
watch(jobs, () => {
  page.value = Math.min(page.value, Math.max(1, Math.ceil(jobs.value.length / pageSize)))
  selected.value = selected.value.filter(id => jobs.value.some(job => job.id === id && canDelete(job)))
})
const pending = ref<{ id: string; entry: TokenGuardReloginAccount } | null>(null)
const rejected = ref<{ email: string; reason: CredentialSubmissionRejection } | null>(null)
let alive = true
let timer: ReturnType<typeof setInterval> | undefined
const states = ['queued', 'logging_in', 'revoking', 'accepted', 'login_failed', 'failed', 'needs_review', 'interrupted']
const codes = ['login_password_rejected', 'login_mfa_retry_exhausted', 'login_mfa_rejected', 'login_upstream_error', 'login_browser_challenge', 'login_email_verification_required', 'login_failed', 'login_interaction_required', 'login_workspace_selection_failed', 'login_session_incomplete', 'invalid_credentials', 'account_die', 'logout_control_missing', 'logout_rejected', 'logout_unconfirmed', 'worker_interrupted', 'login_access_denied', 'login_rate_limited', 'identity_mismatch']
function stateText(job: SessionLogoutJob) { return t('tokenGuard.sessions.states.' + (states.includes(job.status) ? job.status : 'needs_review')) }
function detailText(job: SessionLogoutJob) {
  if (job.status === 'accepted') return t('tokenGuard.sessions.acceptedHint')
  if (!job.error_code) return ''
  return t('tokenGuard.sessions.errors.' + (codes.includes(job.error_code) ? job.error_code : 'logout_unconfirmed'))
}
async function showRelatedJob(id: string) {
  const index = jobs.value.findIndex(job => job.id === id)
  if (index < 0) return
  page.value = Math.floor(index / pageSize) + 1
  await nextTick()
  document.getElementById('logout-job-' + id)?.scrollIntoView?.({ block: 'nearest' })
}
function relatedJobs() {
  if (rejected.value?.reason !== 'account_has_unresolved_logout') return []
  return jobs.value.filter(job => job.email.trim().toLowerCase() === rejected.value?.email.trim().toLowerCase()
    && job.status === 'needs_review')
}
async function refresh() {
  if (!alive || !props.active || !props.configured || loading.value || deleting.value) return
  loading.value = true
  try { const result = await listSessionLogouts(); if (alive) { jobs.value = result; statusStale.value = false; if (message.value === t('tokenGuard.sessions.loadFailed')) message.value = '' } }
  catch { if (alive) { statusStale.value = true; message.value = t('tokenGuard.sessions.loadFailed') } }
  finally { loading.value = false }
}
async function submit() {
  if (!confirmed.value || !props.configured || props.disabled || busy.value || pending.value) return
  if (!parsed.value) { message.value = t('tokenGuard.sessions.invalid'); return }
  page.value = 1
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
    const job = await startSessionLogout(request.entry, request.id)
    if (alive) jobs.value = [job, ...jobs.value.filter(value => value.id !== job.id)]
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
onMounted(() => { void refresh(); timer = setInterval(() => { void refresh() }, 3000) })
onBeforeUnmount(() => {
  alive = false; if (timer) clearInterval(timer)
  input.value = ''
  if (pending.value) { pending.value.entry.password = ''; pending.value.entry.mfa_secret = '' }
})
</script>
