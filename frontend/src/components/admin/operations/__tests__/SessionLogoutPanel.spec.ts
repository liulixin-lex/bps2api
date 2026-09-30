import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import SessionLogoutPanel from '../SessionLogoutPanel.vue'
import * as api from '@/api/admin/accountSessionLogout'
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/accountSessionLogout', async importOriginal => ({ ...await importOriginal<typeof import('@/api/admin/accountSessionLogout')>(), deleteSessionLogouts: vi.fn(), listSessionLogouts: vi.fn(), startSessionLogout: vi.fn() }))
const job = { id: 'a'.repeat(32), email: 'test@example.com', status: 'queued', created_at: 1 }
let wrapper: VueWrapper | undefined
beforeEach(() => { vi.clearAllMocks(); vi.mocked(api.listSessionLogouts).mockResolvedValue([]); vi.mocked(api.startSessionLogout).mockResolvedValue(job) })
afterEach(() => { wrapper?.unmount(); wrapper = undefined })
async function fill() {
  await wrapper!.get('#logout-credentials').setValue('test@example.com---- p|a,ss$[]+----word ----JBSWY3DPEHPK3PXP')
}
describe('independent session logout', () => {
  it('requires explicit confirmation and saved worker settings', async () => {
    wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
    await fill(); await wrapper.get('form').trigger('submit')
    expect(api.startSessionLogout).not.toHaveBeenCalled()
    await wrapper.get('input[type=checkbox]').setValue(true)
    await wrapper.setProps({ disabled: true }); await wrapper.get('form').trigger('submit')
    expect(api.startSessionLogout).not.toHaveBeenCalled()
    await wrapper.setProps({ disabled: false, configured: false }); await wrapper.get('form').trigger('submit')
    expect(api.startSessionLogout).not.toHaveBeenCalled()
  })
  it('submits original password bytes then clears sensitive inputs and memory', async () => {
    let snapshot: unknown
    vi.mocked(api.startSessionLogout).mockImplementation(async entry => { snapshot = { ...entry }; return job })
    wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
    await fill(); await wrapper.get('input[type=checkbox]').setValue(true); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(snapshot).toEqual({ email: 'test@example.com', password: ' p|a,ss$[]+----word ', mfa_secret: 'JBSWY3DPEHPK3PXP' })
    expect((wrapper.get('#logout-credentials').element as HTMLTextAreaElement).value).toBe('')
    expect(vi.mocked(api.startSessionLogout).mock.calls[0][0].password).toBe('')
    expect(wrapper.text()).toContain('sessions.submitted')
  })
  it('retries an uncertain submission with the same request id without creating duplicates', async () => {
    vi.mocked(api.startSessionLogout).mockRejectedValueOnce(new Error('lost'))
    wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
    await fill(); await wrapper.get('input[type=checkbox]').setValue(true); await wrapper.get('form').trigger('submit'); await flushPromises()
    const first = vi.mocked(api.startSessionLogout).mock.calls[0]
    expect(wrapper.text()).toContain('sessions.submitUncertain')
    expect((wrapper.get('#logout-credentials').element as HTMLTextAreaElement).disabled).toBe(true)
    await wrapper.findAll('button').find(b => b.text() === 'tokenGuard.rotation.retrySubmit')!.trigger('click'); await flushPromises()
    expect(api.startSessionLogout).toHaveBeenCalledTimes(2)
    expect(vi.mocked(api.startSessionLogout).mock.calls[1][1]).toBe(first[1])
    expect(wrapper.text()).not.toContain('rotation.retrySubmit')
  })
  it('keeps status across navigation and refresh without dispatching an operation', async () => {
    vi.mocked(api.listSessionLogouts).mockResolvedValue([{ ...job, status: 'accepted' }])
    wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: false } })
    await flushPromises(); expect(api.listSessionLogouts).not.toHaveBeenCalled()
    await wrapper.setProps({ active: true }); await flushPromises()
    expect(wrapper.text()).toContain('sessions.states.accepted')
    expect(wrapper.text()).toContain('sessions.acceptedHint')
    expect(api.startSessionLogout).not.toHaveBeenCalled()
  })
  it.each(['login_workspace_selection_failed', 'login_session_incomplete'])('explains login completion failure: %s', async code => {
    vi.mocked(api.listSessionLogouts).mockResolvedValue([{ ...job, status: 'login_failed', error_code: code }])
    wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
    await flushPromises()
    expect(wrapper.text()).toContain('sessions.errors.' + code)
    expect(wrapper.findAll('tbody button')).toHaveLength(0)
  })
  it('does not display raw worker messages or retry a terminal uncertain operation', async () => {
    vi.mocked(api.listSessionLogouts).mockResolvedValue([{ ...job, status: 'unknown-private', error_code: 'private-cookie' }])
    wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } }); await flushPromises()
    expect(wrapper.text()).toContain('sessions.states.needs_review')
    expect(wrapper.text()).toContain('sessions.errors.logout_unconfirmed')
    expect(wrapper.text()).not.toContain('private-')
    expect(wrapper.findAll('tbody button')).toHaveLength(0)
    expect(api.startSessionLogout).not.toHaveBeenCalled()
  })
})


it('releases a rejected logout submission and links the protected historical task', async () => {
  vi.mocked(api.startSessionLogout).mockRejectedValueOnce({ status: 409, reason: 'account_has_unresolved_logout', metadata: { submission: 'not_accepted' }, message: 'private-cookie' })
  vi.mocked(api.listSessionLogouts).mockResolvedValue([{ ...job, status: 'needs_review' }])
  wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
  await fill(); await wrapper.get('input[type=checkbox]').setValue(true); await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(wrapper.text()).toContain('submission.account_has_unresolved_logout')
  expect(wrapper.text()).not.toContain('submitUncertain')
  expect(wrapper.text()).not.toContain('retrySubmit')
  expect(wrapper.text()).not.toContain('private-cookie')
  expect((wrapper.get('#logout-credentials').element as HTMLTextAreaElement).disabled).toBe(false)
  expect(vi.mocked(api.startSessionLogout).mock.calls[0][0].password).toBe('')
  expect(vi.mocked(api.startSessionLogout).mock.calls[0][0].mfa_secret).toBe('')
  expect(wrapper.get('a').attributes('href')).toBe('#logout-job-' + job.id)
  await wrapper.findAll('button').find(b => b.text() === 'tokenGuard.refresh')!.trigger('click'); await flushPromises()
  expect(api.startSessionLogout).toHaveBeenCalledTimes(1)
})


it('recognizes one pasted line without submitting or exposing parsed secrets in status text', async () => {
  wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
  await fill(); await flushPromises()
  expect(wrapper.text()).toContain('sessions.recognized')
  expect(wrapper.text()).not.toContain('p|a,ss$[]+')
  expect(wrapper.text()).not.toContain('JBSWY3DPEHPK3PXP')
  expect(wrapper.findAll('input[type=email], input[type=password]')).toHaveLength(0)
  expect(api.startSessionLogout).not.toHaveBeenCalled()
})

it.each(['invalid', 'u@example.com----pass----', 'u@example.com----pass----SEED\nv@example.com----pass----SEED'])('rejects malformed or multiple pasted accounts without dispatch: %s', async raw => {
  wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
  await wrapper.get('#logout-credentials').setValue(raw)
  await wrapper.get('input[type=checkbox]').setValue(true)
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(wrapper.text()).toContain('sessions.invalid')
  expect(api.startSessionLogout).not.toHaveBeenCalled()
  expect((wrapper.get('#logout-credentials').element as HTMLTextAreaElement).disabled).toBe(false)
})

it('clears a pasted pending credential on unmount after an uncertain response', async () => {
  vi.mocked(api.startSessionLogout).mockRejectedValueOnce(new Error('lost response'))
  wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
  await fill(); await wrapper.get('input[type=checkbox]').setValue(true)
  await wrapper.get('form').trigger('submit'); await flushPromises()
  const entry = vi.mocked(api.startSessionLogout).mock.calls[0][0]
  expect(entry.password).not.toBe('')
  wrapper.unmount(); wrapper = undefined
  expect(entry.password).toBe(''); expect(entry.mfa_secret).toBe('')
})

it('paginates logout history, selects only safe rows and clamps the page after deletion', async () => {
  const records = Array.from({ length: 21 }, (_, i) => ({ ...job, id: i.toString(16).padStart(32, '0'), status: i === 0 ? 'needs_review' : 'accepted', deletable: i !== 0 }))
  vi.mocked(api.listSessionLogouts).mockResolvedValue(records)
  wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } }); await flushPromises()
  expect(wrapper.findAll('tbody tr')).toHaveLength(10)
  expect((wrapper.get('[data-testid="select-history-row"]').element as HTMLInputElement).disabled).toBe(true)
  await wrapper.get('[data-testid="select-page"]').setValue(true)
  expect(wrapper.findAll('[data-testid="select-history-row"]').filter(b => (b.element as HTMLInputElement).checked)).toHaveLength(9)
  const pagination = wrapper.findComponent({ name: 'Pagination' }); pagination.vm.$emit('update:page', 3); await flushPromises()
  expect(wrapper.findAll('tbody tr')).toHaveLength(1)
  expect((wrapper.get('[data-testid="delete-selected"]').element as HTMLButtonElement).disabled).toBe(true)
  await wrapper.get('[data-testid="select-history-row"]').setValue(true)
  vi.mocked(api.deleteSessionLogouts).mockResolvedValue({ deleted_ids: [records[20].id] })
  vi.mocked(api.listSessionLogouts).mockResolvedValue(records.slice(0, 20))
  await wrapper.get('[data-testid="delete-selected"]').trigger('click'); await flushPromises()
  expect(api.deleteSessionLogouts).toHaveBeenCalledWith([records[20].id])
  expect(pagination.props('page')).toBe(2)
  expect(wrapper.findAll('tbody tr')).toHaveLength(10)
  expect(api.startSessionLogout).not.toHaveBeenCalled()
})

it('keeps records when deletion fails and protects active, uncertain and unknown states', async () => {
  vi.mocked(api.listSessionLogouts).mockResolvedValue(['queued', 'logging_in', 'revoking', 'needs_review', 'unknown', 'accepted'].map((status, i) => ({ ...job, id: String(i).repeat(32), status })))
  wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } }); await flushPromises()
  const boxes = wrapper.findAll('[data-testid="select-history-row"]')
  expect(boxes.slice(0, 5).every(b => (b.element as HTMLInputElement).disabled)).toBe(true)
  await boxes[5].setValue(true)
  vi.mocked(api.deleteSessionLogouts).mockRejectedValue(new Error('private-worker-message'))
  await wrapper.get('[data-testid="delete-selected"]').trigger('click'); await flushPromises()
  expect(wrapper.findAll('tbody tr')).toHaveLength(6)
  expect(wrapper.text()).toContain('history.deleteFailed'); expect(wrapper.text()).not.toContain('private-worker-message')
})

it('shows changing progress, warns on failed refresh and stops spinning on a terminal status', async () => {
  const refresh = async () => { await wrapper!.findAll('button').find(b => b.text() === 'tokenGuard.refresh')!.trigger('click'); await flushPromises() }
  vi.mocked(api.listSessionLogouts).mockResolvedValue([{ ...job, status: 'logging_in', phase: 'mfa' }])
  wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } }); await flushPromises()
  expect(wrapper.text()).toContain('progress.phases.mfa')
  expect(wrapper.find('[data-testid="progress-spinner"]').exists()).toBe(true)
  vi.mocked(api.listSessionLogouts).mockRejectedValueOnce(new Error('offline')); await refresh()
  expect(wrapper.text()).toContain('progress.stale')
  expect(wrapper.find('[data-testid="progress-spinner"]').exists()).toBe(false)
  vi.mocked(api.listSessionLogouts).mockResolvedValue([{ ...job, status: 'revoking', phase: 'revoking' }]); await refresh()
  expect(wrapper.text()).toContain('progress.phases.revoking')
  expect(wrapper.text()).not.toContain('progress.stale')
  vi.mocked(api.listSessionLogouts).mockResolvedValue([{ ...job, status: 'accepted', phase: 'revoking' }]); await refresh()
  expect(wrapper.find('[data-testid="active-task-summary"]').exists()).toBe(false)
  expect(api.startSessionLogout).not.toHaveBeenCalled()
})
