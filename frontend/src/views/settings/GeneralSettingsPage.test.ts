// @vitest-environment happy-dom
//
// Mounted-DOM coverage for the General settings page (/settings/general) in
// BOTH locales (zh-CN and en ship different copy, so each is asserted on the
// copy itself):
//
//   - render: both persistent-form items appear with the committed values
//     (retention days input, key-recovery switch + interval input) — TC-06
//   - save: changing retention and saving PUTs the retention endpoint with
//     the CAS version; toggling the recovery switch and saving PUTs the
//     key-auto-recovery endpoint — TC-06 (both arms), TC-10
//   - the retention illegal-value matrix (-1 / 1.5 / 3651 / cleared) each
//     blocks the save with its own localized message and never PUTs —
//     TC-02's frontend half (matrix shape aligned with the key-recovery
//     modal's, whose pure logic lives in utils/keyAutoRecovery.test.ts)
//   - one representative illegal interval blocks the recovery save the same
//     way — TC-10's "modal behavior preserved in form shape" arm
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import naive from 'naive-ui'
import { defineComponent, nextTick } from 'vue'

import {
  getKeyAutoRecovery,
  getRequestLogRetention,
  updateKeyAutoRecovery,
  updateRequestLogRetention,
} from '../../api/systemSettings'

vi.mock('../../api/systemSettings', () => ({
  getKeyAutoRecovery: vi.fn(),
  getRequestLogRetention: vi.fn(),
  updateKeyAutoRecovery: vi.fn(),
  updateRequestLogRetention: vi.fn(),
}))

const getRetentionMock = vi.mocked(getRequestLogRetention)
const putRetentionMock = vi.mocked(updateRequestLogRetention)
const getRecoveryMock = vi.mocked(getKeyAutoRecovery)
const putRecoveryMock = vi.mocked(updateKeyAutoRecovery)

// Must be imported after the vi.mock factory is registered.
import GeneralSettingsPage from './GeneralSettingsPage.vue'
import en from '../../locales/en'
import zhCN from '../../locales/zh-CN'

// App.vue nests pages under n-config-provider > n-message-provider; the
// page's useMessage() throws without that ancestry.
const Host = defineComponent({
  components: { GeneralSettingsPage },
  template: '<NConfigProvider><NMessageProvider><GeneralSettingsPage /></NMessageProvider></NConfigProvider>',
})

type Locale = 'en' | 'zh-CN'
const navCopy = { en: en.nav, 'zh-CN': zhCN.nav } as const
const gsCopy = { en: en.generalSettings, 'zh-CN': zhCN.generalSettings } as const
const karCopy = { en: en.keyAutoRecovery, 'zh-CN': zhCN.keyAutoRecovery } as const

let wrapper: VueWrapper | null = null

// The committed rows the two GETs resolve with. Retention starts at the
// fail-open default 0 (keep forever); recovery starts disabled.
function primeSettings(retentionDays = 0, retentionVersion = 5) {
  getRetentionMock.mockResolvedValue({ retention_days: retentionDays, version: retentionVersion })
  getRecoveryMock.mockResolvedValue({ enabled: false, interval_minutes: 15, version: 9 })
}

async function mountPage(locale: Locale) {
  const i18n = createI18n({ legacy: false, locale, fallbackLocale: 'zh-CN', messages: { en, 'zh-CN': zhCN } })
  wrapper = mount(Host, { attachTo: document.body, global: { plugins: [i18n, naive] } })
  // Both mount-time GETs are mocked to resolve instantly; settle them.
  await vi.waitFor(() => expect(getRetentionMock).toHaveBeenCalled())
  await nextTick()
  await nextTick()
}

function retentionInput(): HTMLInputElement {
  const input = document.body.querySelector<HTMLInputElement>('.gs-retention__days input')
  expect(input, 'retention days input rendered').toBeTruthy()
  return input!
}

function intervalInput(): HTMLInputElement {
  const input = document.body.querySelector<HTMLInputElement>('.gs-recovery__interval input')
  expect(input, 'recovery interval input rendered').toBeTruthy()
  return input!
}

function saveButton(section: 'retention' | 'recovery'): HTMLButtonElement {
  const btn = document.body.querySelector<HTMLButtonElement>(`.gs-${section}__save`)
  expect(btn, `${section} save button rendered`).toBeTruthy()
  return btn!
}

function recoverySwitch(): HTMLElement {
  const el = document.body.querySelector<HTMLElement>('.gs-recovery__switch')
  expect(el, 'recovery switch rendered').toBeTruthy()
  return el!
}

// NInputNumber keeps its v-model in sync per keystroke (updateValueOnInput,
// no min/max/precision props on these fields), so a native input event with
// the new string drives the form state exactly like typing does.
function setInputValue(input: HTMLInputElement, value: string) {
  input.value = value
  input.dispatchEvent(new Event('input'))
}

beforeEach(() => {
  primeSettings()
  putRetentionMock.mockReset()
  putRecoveryMock.mockReset()
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  vi.clearAllMocks()
  document.body.innerHTML = ''
})

describe.each<Locale>(['zh-CN', 'en'])('GeneralSettingsPage render (%s)', (locale) => {
  it('renders both persistent-form items with the committed values', async () => {
    await mountPage(locale)
    const text = document.body.textContent ?? ''
    // The page header and both section cards.
    expect(text).toContain(navCopy[locale].generalSettings)
    expect(text).toContain(gsCopy[locale].retention.title)
    expect(text).toContain(karCopy[locale].title)
    expect(text).toContain(gsCopy[locale].retention.days)
    expect(text).toContain(karCopy[locale].enabled)
    expect(text).toContain(karCopy[locale].interval)
    // Committed values: retention shows the seeded 0, interval shows 15.
    expect(retentionInput().value).toBe('0')
    expect(intervalInput().value).toBe('15')
    expect(recoverySwitch().getAttribute('aria-checked')).toBe('false')
    // Both save buttons are present and enabled (both GETs resolved).
    expect(saveButton('retention').disabled).toBe(false)
    expect(saveButton('recovery').disabled).toBe(false)
  })
})

describe('GeneralSettingsPage save interactions (en)', () => {
  it('PUTs the retention endpoint with the CAS version when the days change (TC-06 retention arm)', async () => {
    await mountPage('en')
    setInputValue(retentionInput(), '30')
    await nextTick()
    saveButton('retention').dispatchEvent(new Event('click'))
    await vi.waitFor(() => expect(putRetentionMock).toHaveBeenCalledTimes(1))
    expect(putRetentionMock).toHaveBeenCalledWith({ retention_days: 30, version: 5 })
    // Save succeeds → the committed row is re-read (the GET fires again).
    await vi.waitFor(() => expect(getRetentionMock).toHaveBeenCalledTimes(2))
    expect(document.body.textContent ?? '').toContain(gsCopy.en.retention.saved)
  })

  it('PUTs the existing key-auto-recovery endpoint when the switch is toggled (TC-06 / TC-10)', async () => {
    await mountPage('en')
    recoverySwitch().dispatchEvent(new Event('click'))
    await nextTick()
    expect(recoverySwitch().getAttribute('aria-checked')).toBe('true')
    saveButton('recovery').dispatchEvent(new Event('click'))
    await vi.waitFor(() => expect(putRecoveryMock).toHaveBeenCalledTimes(1))
    // Existing API, existing payload shape: only the switch changed, the
    // loaded interval and version ride along.
    expect(putRecoveryMock).toHaveBeenCalledWith({ enabled: true, interval_minutes: 15, version: 9 })
    expect(document.body.textContent ?? '').toContain(karCopy.en.saved)
  })
})

describe('GeneralSettingsPage retention illegal-value matrix (en) — TC-02 frontend half', () => {
  // The same drafts the pure validator rejects (utils/requestLogRetention),
  // asserted here at the form level: each one blocks the save with its own
  // message and the PUT never fires.
  const drafts: { input: string; message: string }[] = [
    { input: '-1', message: gsCopy.en.retention.daysMin },
    { input: '1.5', message: gsCopy.en.retention.daysWholeNumber },
    { input: '3651', message: gsCopy.en.retention.daysMax },
    // Clearing the field is "empty", not "zero": NInputNumber reports null.
    { input: '', message: gsCopy.en.retention.daysRequired },
  ]

  it.each(drafts)('input "$input" is rejected with its own message and no PUT', async (draft) => {
    await mountPage('en')
    setInputValue(retentionInput(), draft.input)
    await nextTick()
    saveButton('retention').dispatchEvent(new Event('click'))
    await nextTick()
    await nextTick()
    expect(document.body.textContent ?? '', 'the form shows the issue message').toContain(draft.message)
    expect(putRetentionMock, 'an illegal retention never reaches the wire').not.toHaveBeenCalled()
  })
})

describe('GeneralSettingsPage recovery interval stays validated in form shape (en) — TC-10', () => {
  it('blocks the save when the interval is cleared, with the modal-era message', async () => {
    await mountPage('en')
    setInputValue(intervalInput(), '')
    await nextTick()
    saveButton('recovery').dispatchEvent(new Event('click'))
    await nextTick()
    await nextTick()
    expect(document.body.textContent ?? '').toContain(karCopy.en.intervalRequired)
    expect(putRecoveryMock).not.toHaveBeenCalled()
  })
})
