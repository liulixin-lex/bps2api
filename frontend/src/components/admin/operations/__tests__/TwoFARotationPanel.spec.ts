import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import TwoFARotationPanel from '../TwoFARotationPanel.vue'
import * as api from '@/api/admin/accountTwoFARotation'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@/api/admin/accountTwoFARotation', async importOriginal => {
  const actual = await importOriginal<typeof import('@/api/admin/accountTwoFARotation')>()
  return { ...actual, startTwoFARotation: vi.fn(), listTwoFARotations: vi.fn(), verifyTwoFARotation: vi.fn(), getTwoFARotationResult: vi.fn() }
})

let wrapper: VueWrapper | undefined
const job = { id: '0123456789abcdef0123456789abcdef', email: 'u@example.com', status: 'queued', login_verified: false, rotated_pending_verify: false, retryable: false, created_at: 1 }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listTwoFARotations).mockResolvedValue([])
  vi.mocked(api.startTwoFARotation).mockResolvedValue(job)
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals() })

async function inputAndConfirm() {
  await wrapper!.get('textarea').setValue('u@example.com----pass----JBSWY3DPEHPK3PXP')
  await wrapper!.get('input[type="checkbox"]').setValue(true)
}

describe('explicit 2FA rotation workflow', () => {
  it('requires confirmation and saved worker configuration', async () => {
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await wrapper.get('textarea').setValue('u@example.com----pass----JBSWY3DPEHPK3PXP')
    await wrapper.get('form').trigger('submit')
    expect(api.startTwoFARotation).not.toHaveBeenCalled()
    await inputAndConfirm()
    await wrapper.setProps({ disabled: true })
    await wrapper.get('form').trigger('submit')
    expect(api.startTwoFARotation).not.toHaveBeenCalled()
    await wrapper.setProps({ disabled: false, configured: false })
    await wrapper.get('form').trigger('submit')
    expect(api.startTwoFARotation).not.toHaveBeenCalled()
  })
  it('reuses the original request id and payload after a lost response', async () => {
    vi.mocked(api.startTwoFARotation).mockRejectedValueOnce(new Error('lost response'))
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await inputAndConfirm()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const [firstEntry, firstID] = vi.mocked(api.startTwoFARotation).mock.calls[0]
    const originalEntry = { ...firstEntry }
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    const retry = wrapper.findAll('button').find(button => button.text().includes('retrySubmit'))!
    await retry.trigger('click')
    await flushPromises()
    expect(api.startTwoFARotation).toHaveBeenCalledTimes(2)
    expect(vi.mocked(api.startTwoFARotation).mock.calls[1][1]).toBe(firstID)
    // Submitted secrets are erased from component memory after acceptance.
    expect(originalEntry.password).toBe('pass')
    expect(firstEntry.password).toBe('')
    expect(wrapper.text()).not.toContain('retrySubmit')
  })
  it('uses the verification endpoint for a checkpointed job, never starts another rotation', async () => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([{ ...job, status: 'error', rotated_pending_verify: true, retryable: true }])
    vi.mocked(api.verifyTwoFARotation).mockResolvedValue(job)
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('rotation.copy')
    const verify = wrapper.findAll('button').find(button => button.text() === 'tokenGuard.rotation.verify')!
    await verify.trigger('click')
    await flushPromises()
    expect(api.verifyTwoFARotation).toHaveBeenCalledWith(job.id)
    expect(api.startTwoFARotation).not.toHaveBeenCalled()
  })
  it('shows a safe pre-rotation login failure without a verify or copy action', async () => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([{ ...job, status: 'login_failed', error_code: 'login_access_denied' }])
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    expect(wrapper.text()).toContain('rotation.states.login_failed')
    expect(wrapper.text()).toContain('rotation.errors.login_access_denied')
    expect(wrapper.text()).not.toContain('rotation.states.needs_review')
    expect(wrapper.text()).not.toContain('rotation.verify')
    expect(wrapper.text()).not.toContain('rotation.copy')
    expect(api.startTwoFARotation).not.toHaveBeenCalled()
  })
  it.each(['login_workspace_selection_failed', 'login_session_incomplete'])('explains login completion failure: %s', async code => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([{ ...job, status: 'login_failed', error_code: code }])
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    expect(wrapper.text()).toContain('rotation.errors.' + code)
    expect(wrapper.findAll('tbody button')).toHaveLength(0)
  })
  it('never displays unrecognized worker diagnostic text', async () => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([{ ...job, status: 'login_failed', error_code: 'private-secret-from-worker' }])
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    expect(wrapper.text()).toContain('rotation.errors.login_failed')
    expect(wrapper.text()).not.toContain('private-secret')
  })

  it('explains a disable server error while keeping the uncertain task blocked', async () => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([{ ...job, status: 'needs_review', error_code: 'rotation_disable_server_error' }])
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    expect(wrapper.text()).toContain('rotation.states.needs_review')
    expect(wrapper.text()).toContain('rotation.errors.rotation_disable_server_error')
    expect(wrapper.text()).not.toContain('rotation.verify')
    expect(wrapper.text()).not.toContain('rotation.copy')
    expect(api.startTwoFARotation).not.toHaveBeenCalled()
  })

  it('uses a generic explanation for unknown rotation errors without exposing text', async () => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([{ ...job, status: 'needs_review', error_code: 'private-upstream-token' }])
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    expect(wrapper.text()).toContain('rotation.errors.rotation_unconfirmed')
    expect(wrapper.text()).not.toContain('private-upstream-token')
  })

  it('hides superseded credential actions while allowing the latest success for another account', async () => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([
      { ...job, id: 'newer', status: 'needs_review', created_at: 2 },
      { ...job, id: 'other', email: 'other@example.com', status: 'success', login_verified: true },
      { ...job, status: 'success', login_verified: true }
    ])
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows[2].text()).toContain('rotation.superseded')
    expect(rows[2].findAll('button')).toHaveLength(0)
    expect(rows[1].text()).toContain('rotation.copy')
    expect(rows[1].text()).toContain('rotation.apply')
    expect(api.getTwoFARotationResult).not.toHaveBeenCalled()
  })

})


describe('copy updated credentials', () => {
  it('loads an existing successful task after remount and copies all three fields without another rotation', async () => {
    const success = { ...job, status: 'success', login_verified: true }
    vi.mocked(api.listTwoFARotations).mockResolvedValue([success])
    vi.mocked(api.getTwoFARotationResult).mockResolvedValue({ id: job.id, email: job.email, password: 'p|a,ss$[]+', mfa_secret: 'NEW_SECRET', login_verified: true })
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    wrapper = mount(TwoFARotationPanel, { props: { configured: true, showApply: false } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('rotation.apply')
    await wrapper.findAll('button').find(button => button.text() === 'tokenGuard.rotation.copy')!.trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith('u@example.com----p|a,ss$[]+----NEW_SECRET')
    expect(wrapper.text()).toContain('rotation.copied')
    expect(wrapper.text()).not.toContain('p|a,ss$[]+')
    expect(api.startTwoFARotation).not.toHaveBeenCalled()
  })
  it('does not overwrite the clipboard if a legacy worker omits the password', async () => {
    vi.mocked(api.listTwoFARotations).mockResolvedValue([{ ...job, status: 'success', login_verified: true }])
    vi.mocked(api.getTwoFARotationResult).mockResolvedValue({ id: job.id, email: job.email, mfa_secret: 'NEW_SECRET', login_verified: true } as api.TwoFARotationResult)
    const writeText = vi.fn()
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    wrapper = mount(TwoFARotationPanel, { props: { configured: true } })
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'tokenGuard.rotation.copy')!.trigger('click')
    await flushPromises()
    expect(writeText).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('rotation.actionFailed')
  })
})
