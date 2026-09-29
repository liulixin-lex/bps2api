import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { MonitorCoverage, MonitorHealth, MonitorMatrixRow, MonitorMetric } from '@/api/channelMonitorV2'
import { hasMonitorSamples, monitorCardTimeline, monitorRefreshSeconds } from '../monitorCards'
import MonitorStatusCards from '../MonitorStatusCards.vue'

vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
vi.mock('@/composables/useChannelMonitorFormat', () => ({ useChannelMonitorFormat: () => ({ providerLabel: (name: string) => name, providerBadgeClass: () => '' }) }))
const metrics: MonitorMetric = {
  success_requests: 0, error_requests: 0, request_count: 0, token_count: 0, rpm: 0, tpm: 0,
  error_rate: 0.1, cache_rate: 0.5, cache_rate_numerator: 0, cache_rate_denominator: 0,
  ttft: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
  duration: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
}
const health: MonitorHealth = { overall: 'healthy', error_rate: 'healthy', ttft: 'unknown', minimum_sample: 20 }
const coverage = { requested_start: '2026-09-29T00:00:00Z', requested_end: '2026-09-29T01:30:00Z' } as MonitorCoverage
const row = (sampled = false): MonitorMatrixRow => ({ platform: 'openai', group_id: 1, group_name: 'Passive group', metrics: { ...metrics, has_samples: sampled }, health, buckets: [] })

describe('passive monitor cards', () => {
  it('distinguishes redacted traffic from no observations', () => {
    expect(hasMonitorSamples(metrics)).toBe(false)
    expect(hasMonitorSamples({ ...metrics, has_samples: true })).toBe(true)
    expect(hasMonitorSamples({ ...metrics, request_count: 1 })).toBe(true)
  })
  it('keeps missing traffic unknown and the worst observed health per window', () => {
    const item = row()
    item.buckets = [
      { bucket_start: '2026-09-29T00:01:00Z', metrics: { ...metrics, has_samples: true }, health },
      { bucket_start: '2026-09-29T00:02:00Z', metrics: { ...metrics, has_samples: true }, health: { ...health, overall: 'critical' } },
      { bucket_start: '2026-09-29T00:06:00Z', metrics, health },
      { bucket_start: 'invalid', metrics: { ...metrics, has_samples: true }, health },
      { bucket_start: '2026-09-29T01:30:00Z', metrics: { ...metrics, has_samples: true }, health },
    ]
    const bars = monitorCardTimeline(item, coverage)
    expect(bars).toHaveLength(18)
    expect(bars[0]).toMatchObject({ observed: true, state: 'critical' })
    expect(bars.slice(1).every(bar => !bar.observed && bar.state === 'unknown')).toBe(true)
  })
  it('rejects malformed or reversed coverage', () => {
    expect(monitorCardTimeline(row(), undefined)).toEqual([])
    expect(monitorCardTimeline(row(), { ...coverage, requested_end: coverage.requested_start })).toEqual([])
  })
  it.each([[undefined, 300], [0, 300], [-1, 300], [NaN, 300], [Infinity, 300], [15, 60], [600, 600]])('bounds passive refresh %s to %s seconds', (configured, expected) => {
    expect(monitorRefreshSeconds(configured, false)).toBe(expected)
    expect(monitorRefreshSeconds(configured, true)).toBe(10)
  })
  it('renders privacy-safe availability for redacted samples without probe controls', () => {
    const wrapper = mount(MonitorStatusCards, { props: { items: [row(true)], loading: false, countdown: 60, coverage }, global: { stubs: { ProviderIcon: true } } })
    expect(wrapper.text()).toContain('90.0%')
    expect(wrapper.text()).toContain('50.0%')
    expect(wrapper.find('[data-testid="candy-history"]').exists()).toBe(false)
    expect(wrapper.findAll('button')).toHaveLength(0)
  })
  it('does not invent availability when traffic is empty', () => {
    const wrapper = mount(MonitorStatusCards, { props: { items: [row()], loading: false, countdown: 60 }, global: { stubs: { ProviderIcon: true } } })
    expect(wrapper.text()).not.toContain('90.0%')
    expect(wrapper.text()).toContain('—')
  })
  it('shows loading and empty states', () => {
    const loading = mount(MonitorStatusCards, { props: { items: [], loading: true, countdown: 60 } })
    expect(loading.findAll('.animate-pulse')).toHaveLength(6)
    const empty = mount(MonitorStatusCards, { props: { items: [], loading: false, countdown: 60 } })
    expect(empty.text()).toContain('channelMonitorV2.empty.title')
  })
})
