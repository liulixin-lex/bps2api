import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import SessionLogoutPanel from '../SessionLogoutPanel.vue'
import * as api from '@/api/admin/accountSessionLogout'
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/accountSessionLogout', () => ({ listSessionLogouts: vi.fn(), startSessionLogout: vi.fn() }))
const job = { id: 'a'.repeat(32), email: 'test@example.com', status: 'queued', created_at: 1 }
let wrapper: VueWrapper | undefined
beforeEach(() => { vi.clearAllMocks(); vi.mocked(api.listSessionLogouts).mockResolvedValue([]); vi.mocked(api.startSessionLogout).mockResolvedValue(job) })
afterEach(() => { wrapper?.unmount(); wrapper = undefined })
async function fill() {
  await wrapper!.get('#logout-email').setValue('test@example.com')
  await wrapper!.get('#logout-password').setValue(' p|a,ss$[]+----word ')
  await wrapper!.get('#logout-secret').setValue('JBSWY3DPEHPK3PXP')
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
    expect((wrapper.get('#logout-password').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('#logout-secret').element as HTMLInputElement).value).toBe('')
    expect(vi.mocked(api.startSessionLogout).mock.calls[0][0].password).toBe('')
    expect(wrapper.text()).toContain('sessions.submitted')
  })
  it('retries an uncertain submission with the same request id without creating duplicates', async () => {
    vi.mocked(api.startSessionLogout).mockRejectedValueOnce(new Error('lost'))
    wrapper = mount(SessionLogoutPanel, { props: { configured: true, active: true } })
    await fill(); await wrapper.get('input[type=checkbox]').setValue(true); await wrapper.get('form').trigger('submit'); await flushPromises()
    const first = vi.mocked(api.startSessionLogout).mock.calls[0]
    expect(wrapper.text()).toContain('sessions.submitUncertain')
    expect((wrapper.get('#logout-password').element as HTMLInputElement).disabled).toBe(true)
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
