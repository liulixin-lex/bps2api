import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import TokenGuardView from '../TokenGuardView.vue'
import * as api from '@/api/admin/accountTokenGuard'

vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/accountTokenGuard', async importOriginal => ({
  ...await importOriginal<typeof import('@/api/admin/accountTokenGuard')>(),
  getTokenGuardStatus: vi.fn(), saveTokenGuardConfig: vi.fn(),
  startTokenGuardRun: vi.fn(), reloginTokenGuardAccount: vi.fn()
}))
vi.mock('@/api/admin/groups', async importOriginal => ({ ...await importOriginal<typeof import('@/api/admin/groups')>(), groupsAPI: { getAll: vi.fn().mockResolvedValue([]) } }))

const config = {
  enabled: false, auto_relogin: true, restore_schedulable: true,
  group_ids: [7], relogin_accounts: [], probe_headers: {}, relogin_headers: {},
  interval_seconds: 300, probe_concurrency: 6, fail_streak_threshold: 1,
  two_fa_rotation_endpoint: 'http://twofa-worker:8080', two_fa_rotation_token: 'existing-token'
} as unknown as api.TokenGuardConfig
const state = { config, runtime: { stats: {}, job: null }, accounts: [], events: [] } as unknown as api.TokenGuardStatus
let wrapper: VueWrapper | undefined
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.getTokenGuardStatus).mockResolvedValue(structuredClone(state))
  vi.mocked(api.saveTokenGuardConfig).mockImplementation(async value => value)
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined })
async function openPage() {
  wrapper = mount(TokenGuardView, { global: { stubs: {
    SessionLogoutPanel: { name: 'SessionLogoutPanel', props: ['configured', 'disabled', 'active'], template: '<div data-testid="logout-panel"><input data-testid="logout-draft" /></div>' },
    AppLayout: { template: '<div><slot /></div>' }, SmartOpsNav: true, Icon: true, Select: true,
    TwoFARotationPanel: { name: 'TwoFARotationPanel', props: ['configured', 'disabled', 'showApply'], template: '<div data-testid="rotation-panel"><input data-testid="rotation-draft" /></div>' }
  } } })
  await flushPromises()
  return wrapper
}

describe('focused 2FA workspace', () => {
  it('defaults to rotation with collapsed worker settings and no visible guard dashboard', async () => {
    const page = await openPage()
    expect(page.get('[data-testid="rotation-workspace"]').isVisible()).toBe(true)
    expect(page.get('[data-testid="guard-workspace"]').isVisible()).toBe(false)
    expect(page.find('.summary-grid').exists()).toBe(false)
    expect(page.findAll('button').some(button => button.isVisible() && button.text() === 'tokenGuard.runNow')).toBe(false)
    expect(page.findComponent({ name: 'TwoFARotationPanel' }).props('showApply')).toBe(false)
    expect(page.get('details').attributes('open')).toBeUndefined()
    expect(api.startTokenGuardRun).not.toHaveBeenCalled()
    expect(api.saveTokenGuardConfig).not.toHaveBeenCalled()
  })
  it('opens the independent guard tools without resetting rotation inputs or changing configuration', async () => {
    const page = await openPage()
    await page.get('[data-testid="rotation-draft"]').setValue('draft still here')
    await page.get('[data-testid="guard-tab"]').trigger('click')
    expect(page.get('[data-testid="guard-workspace"]').isVisible()).toBe(true)
    expect(page.get('[data-testid="rotation-workspace"]').isVisible()).toBe(false)
    expect(page.find('.summary-grid').exists()).toBe(true)
    await page.get('[data-testid="rotation-tab"]').trigger('click')
    expect((page.get('[data-testid="rotation-draft"]').element as HTMLInputElement).value).toBe('draft still here')
    expect(api.startTokenGuardRun).not.toHaveBeenCalled()
    expect(api.saveTokenGuardConfig).not.toHaveBeenCalled()
  })
  it('places logout first in the menu and preserves both operation forms', async () => {
    const page = await openPage()
    expect(page.findAll('nav button')[0].attributes('data-testid')).toBe('logout-tab')
    await page.get('[data-testid="rotation-draft"]').setValue('rotation draft')
    await page.get('[data-testid="logout-tab"]').trigger('click')
    expect(page.get('[data-testid="logout-workspace"]').isVisible()).toBe(true)
    expect(page.get('[data-testid="rotation-workspace"]').isVisible()).toBe(false)
    expect(page.findComponent({ name: 'SessionLogoutPanel' }).props('active')).toBe(true)
    expect(page.get('details').element.parentElement?.style.display).not.toBe('none')
    expect(page.get('details').attributes('open')).toBeUndefined()
    page.get('details').element.setAttribute('open', '')
    expect(page.get('details form').isVisible()).toBe(true)
    await page.get('[data-testid="logout-draft"]').setValue('logout draft')
    await page.get('[data-testid="rotation-tab"]').trigger('click')
    expect((page.get('[data-testid="rotation-draft"]').element as HTMLInputElement).value).toBe('rotation draft')
    await page.get('[data-testid="logout-tab"]').trigger('click')
    expect((page.get('[data-testid="logout-draft"]').element as HTMLInputElement).value).toBe('logout draft')
    expect(api.saveTokenGuardConfig).not.toHaveBeenCalled()
    expect(api.startTokenGuardRun).not.toHaveBeenCalled()
  })
  it('saves the worker endpoint while preserving all existing guard settings', async () => {
    const page = await openPage()
    await page.get('details input:not([type="password"])').setValue('http://replacement-worker:8080')
    await page.get('details form').trigger('submit')
    await flushPromises()
    expect(api.saveTokenGuardConfig).toHaveBeenCalledWith({ ...config, two_fa_rotation_endpoint: 'http://replacement-worker:8080' })
  })
})
