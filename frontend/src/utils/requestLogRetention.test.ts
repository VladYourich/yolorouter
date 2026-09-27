import { describe, expect, it } from 'vitest'
import {
  REQUEST_LOG_MAX_RETENTION_DAYS,
  checkRetentionDays,
  retentionFormFromSetting,
  retentionIssueMessageKey,
  retentionSavePlan,
  type RetentionIssue,
} from './requestLogRetention'
import en from '../locales/en/generalSettings'
import zhCN from '../locales/zh-CN/generalSettings'

// The message keys must exist in BOTH locale files with non-empty text, so
// renaming or deleting a message fails here instead of surfacing as a raw
// key in the UI. Drives the real locale modules, not a copy of them.
const messagesByLocale: Record<string, Record<string, string>> = {
  'zh-CN': zhCN.retention as Record<string, string>,
  en: en.retention as Record<string, string>,
}

const ALL_ISSUES: RetentionIssue[] = ['empty', 'notWholeNumber', 'belowMin', 'aboveMax']

describe('checkRetentionDays', () => {
  it('accepts whole days at and between the bounds — including 0, keep forever', () => {
    expect(checkRetentionDays(0)).toEqual({ ok: true, days: 0 })
    expect(checkRetentionDays(1)).toEqual({ ok: true, days: 1 })
    expect(checkRetentionDays(30)).toEqual({ ok: true, days: 30 })
    expect(checkRetentionDays(REQUEST_LOG_MAX_RETENTION_DAYS)).toEqual({ ok: true, days: 3650 })
  })

  it('rejects a cleared or unparsable input as empty — the shapes NInputNumber emits', () => {
    expect(checkRetentionDays(null)).toEqual({ ok: false, issue: 'empty' })
    expect(checkRetentionDays(undefined)).toEqual({ ok: false, issue: 'empty' })
    expect(checkRetentionDays(Number.NaN)).toEqual({ ok: false, issue: 'empty' })
  })

  it('rejects non-integer days rather than rounding them', () => {
    expect(checkRetentionDays(1.5)).toEqual({ ok: false, issue: 'notWholeNumber' })
    expect(checkRetentionDays(0.5)).toEqual({ ok: false, issue: 'notWholeNumber' })
  })

  it('rejects negative days — unlike the interval, 0 is legal, so only negatives fall below the bound', () => {
    expect(checkRetentionDays(-1)).toEqual({ ok: false, issue: 'belowMin' })
    expect(checkRetentionDays(-30)).toEqual({ ok: false, issue: 'belowMin' })
  })

  it('rejects above the ten-year maximum', () => {
    expect(checkRetentionDays(REQUEST_LOG_MAX_RETENTION_DAYS + 1)).toEqual({
      ok: false,
      issue: 'aboveMax',
    })
  })
})

describe('retentionIssueMessageKey', () => {
  it('maps every issue to a distinct, existing message in both locales', () => {
    const keys = ALL_ISSUES.map((issue) => retentionIssueMessageKey(issue).replace('generalSettings.retention.', ''))
    // Distinct: two issues sharing one message would hide which bound broke.
    expect(new Set(keys).size).toBe(ALL_ISSUES.length)
    for (const key of keys) {
      expect(messagesByLocale['zh-CN'][key], `zh-CN.retention.${key}`).toBeTruthy()
      expect(messagesByLocale.en[key], `en.retention.${key}`).toBeTruthy()
    }
  })
})

describe('retentionFormFromSetting', () => {
  it('projects the GET payload into the editable form state', () => {
    expect(retentionFormFromSetting({ retention_days: 0 })).toEqual({ days: 0 })
    expect(retentionFormFromSetting({ retention_days: 30 })).toEqual({ days: 30 })
  })
})

describe('retentionSavePlan', () => {
  it('assembles the PUT payload with the CAS version when the retention is valid', () => {
    expect(retentionSavePlan({ days: 30 }, 5)).toEqual({
      ok: true,
      payload: { retention_days: 30, version: 5 },
    })
    expect(retentionSavePlan({ days: 0 }, 2)).toEqual({
      ok: true,
      payload: { retention_days: 0, version: 2 },
    })
  })

  it('blocks the save and reports the issue for every invalid retention shape', () => {
    const drafts: { days: number | null; issue: RetentionIssue }[] = [
      { days: null, issue: 'empty' },
      { days: 1.5, issue: 'notWholeNumber' },
      { days: -1, issue: 'belowMin' },
      { days: 3651, issue: 'aboveMax' },
    ]
    for (const { days, issue } of drafts) {
      expect(retentionSavePlan({ days }, 3)).toEqual({ ok: false, issue })
    }
  })
})
