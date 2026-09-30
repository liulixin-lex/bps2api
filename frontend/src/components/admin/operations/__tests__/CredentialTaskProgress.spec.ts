import { afterEach, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import CredentialTaskProgress from '../CredentialTaskProgress.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, args?: unknown) => key + (args ? JSON.stringify(args) : '') }) }))
let wrapper: VueWrapper | undefined
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })
it('shows real stages and elapsed time, pauses on stale status and stops on completion', async () => {
  vi.useFakeTimers(); vi.setSystemTime(100000)
  wrapper = mount(CredentialTaskProgress, { props: { active: true, stale: false, fallback: 'queued', job: { created_at: 80, started_at: 90, phase: 'password', phase_started_at: 95 } } })
  expect(wrapper.get('[data-testid="progress-spinner"]').classes()).toContain('animate-spin')
  expect(wrapper.text()).toContain('phases.password')
  expect(wrapper.text()).toContain('"seconds":10')
  await vi.advanceTimersByTimeAsync(2000)
  expect(wrapper.text()).toContain('"seconds":12')
  await wrapper.setProps({ job: { created_at: 80, started_at: 90, phase: 'verify_mfa', phase_started_at: 102 } })
  expect(wrapper.text()).toContain('phases.verify_mfa')
  await wrapper.setProps({ stale: true })
  expect(wrapper.find('[data-testid="progress-spinner"]').exists()).toBe(false)
  expect(wrapper.text()).toContain('progress.stale')
  expect(wrapper.text()).toContain('progress.lastKnown')
  expect(vi.getTimerCount()).toBe(0)
  await wrapper.setProps({ stale: false, active: false })
  expect(wrapper.find('[data-testid="task-progress"]').exists()).toBe(false)
  expect(vi.getTimerCount()).toBe(0)
})
it('never renders an arbitrary worker phase and clears its timer on unmount', () => {
  vi.useFakeTimers()
  wrapper = mount(CredentialTaskProgress, { props: { active: true, stale: false, fallback: 'known-state', job: { created_at: 1, phase: 'private-cookie', phase_started_at: 1 } } })
  expect(wrapper.text()).toContain('known-state'); expect(wrapper.text()).not.toContain('private-cookie')
  expect(wrapper.text()).not.toContain('phaseElapsed')
  wrapper.unmount(); wrapper = undefined; expect(vi.getTimerCount()).toBe(0)
})
