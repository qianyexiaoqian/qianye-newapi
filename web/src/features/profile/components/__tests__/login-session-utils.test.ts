/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { TFunction } from 'i18next'

import type { LoginSession } from '@/stores/auth-store'

import {
  buildLoginSessionsView,
  LOGIN_SESSIONS_PAGE_SIZE,
  loginMethodLabel,
  sessionDevice,
} from '../login-session-utils'

const translate = ((key: string) => key) as TFunction

const NOW = 1_700_000_000

function makeSession(sid: string, expiresAt = NOW + 3600): LoginSession {
  return {
    sid,
    current: false,
    login_method: 'password',
    ip: '203.0.113.7',
    user_agent: '',
    created_at: NOW - 7200,
    last_active_at: NOW - 60,
    expires_at: expiresAt,
  }
}

function makeSessions(count: number): LoginSession[] {
  return Array.from({ length: count }, (_, index) =>
    makeSession(`sid-${index + 1}`)
  )
}

function visibleSids(sessions: LoginSession[], page: number): string[] {
  return buildLoginSessionsView(sessions, page, NOW).visible.map(
    (entry) => entry.session.sid
  )
}

describe('login session presentation', () => {
  test('labels built-in and provider OAuth login methods', () => {
    assert.equal(loginMethodLabel('password', translate), 'Password')
    assert.equal(
      loginMethodLabel('2fa', translate),
      'Two-factor Authentication'
    )
    assert.equal(loginMethodLabel('oauth:github', translate), 'OAuth · GitHub')
    assert.equal(
      loginMethodLabel('oauth:custom-provider', translate),
      'OAuth · custom-provider'
    )
  })

  test('labels iPad Safari as iOS when its user agent also mentions Mac OS X', () => {
    const userAgent =
      'Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1'

    assert.equal(
      sessionDevice(userAgent, 'Unknown device', 'Browser'),
      'Safari · iOS'
    )
  })

  test('labels a touch-capable current iPad session as iOS when its desktop user agent says Macintosh', () => {
    const userAgent =
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15'

    assert.equal(
      sessionDevice(userAgent, 'Unknown device', 'Browser', 5),
      'Safari · iOS'
    )
  })

  test('keeps touch-capable Windows Chrome sessions identifiable', () => {
    const userAgent =
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36'

    assert.equal(
      sessionDevice(userAgent, 'Unknown device', 'Browser', 10),
      'Chrome · Windows'
    )
  })

  test('keeps Android Chrome sessions identifiable when their user agent mentions Linux', () => {
    const userAgent =
      'Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36'

    assert.equal(
      sessionDevice(userAgent, 'Unknown device', 'Browser', 5),
      'Chrome · Android'
    )
  })

  test('keeps genuine macOS Safari sessions identifiable', () => {
    const userAgent =
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15'

    assert.equal(
      sessionDevice(userAgent, 'Unknown device', 'Browser'),
      'Safari · macOS'
    )
  })

  test('falls back to the unknown-device label for an empty user agent', () => {
    assert.equal(
      sessionDevice('', 'Unknown device', 'Browser'),
      'Unknown device'
    )
  })
})

describe('login session pagination and stats', () => {
  test('an empty list stays on page one with no pages to turn', () => {
    const view = buildLoginSessionsView([], 1, NOW)

    assert.deepEqual(view.summary, { total: 0, active: 0, expired: 0 })
    assert.equal(view.page, 1)
    assert.equal(view.pageCount, 1)
    assert.deepEqual(view.visible, [])
  })

  test('a full single page does not spill into a second one', () => {
    const view = buildLoginSessionsView(
      makeSessions(LOGIN_SESSIONS_PAGE_SIZE),
      1,
      NOW
    )

    assert.equal(view.pageCount, 1)
    assert.equal(view.visible.length, LOGIN_SESSIONS_PAGE_SIZE)
  })

  test('one session past the page size opens a second page holding the remainder', () => {
    const sessions = makeSessions(LOGIN_SESSIONS_PAGE_SIZE + 1)

    assert.equal(buildLoginSessionsView(sessions, 1, NOW).pageCount, 2)
    assert.deepEqual(visibleSids(sessions, 1), [
      'sid-1',
      'sid-2',
      'sid-3',
      'sid-4',
      'sid-5',
      'sid-6',
    ])
    assert.deepEqual(visibleSids(sessions, 2), ['sid-7'])
  })

  test('every session is reachable exactly once across the pages', () => {
    const sessions = makeSessions(13)
    const view = buildLoginSessionsView(sessions, 1, NOW)
    const walked = Array.from({ length: view.pageCount }, (_, index) =>
      visibleSids(sessions, index + 1)
    ).flat()

    // 这是"统计数字 == 实际能翻到的条数"的判据：列表不按到期与否过滤，
    // 所以逐页走完必须原样复原整份列表，一条不多一条不少。
    assert.deepEqual(
      walked,
      sessions.map((session) => session.sid)
    )
    assert.equal(walked.length, view.summary.total)
  })

  test('revoking the last session on the final page falls back one page', () => {
    const sessions = makeSessions(LOGIN_SESSIONS_PAGE_SIZE + 1)
    assert.equal(buildLoginSessionsView(sessions, 2, NOW).page, 2)

    const afterRevoke = buildLoginSessionsView(
      sessions.slice(0, LOGIN_SESSIONS_PAGE_SIZE),
      2,
      NOW
    )

    assert.equal(afterRevoke.page, 1)
    assert.equal(afterRevoke.visible.length, LOGIN_SESSIONS_PAGE_SIZE)
  })

  test('out-of-range page numbers clamp back into the valid range', () => {
    const sessions = makeSessions(LOGIN_SESSIONS_PAGE_SIZE + 1)

    assert.equal(buildLoginSessionsView(sessions, 0, NOW).page, 1)
    assert.equal(buildLoginSessionsView(sessions, -5, NOW).page, 1)
    assert.equal(buildLoginSessionsView(sessions, 99, NOW).page, 2)
    assert.equal(buildLoginSessionsView(sessions, Number.NaN, NOW).page, 1)
  })

  test('expiry is decided by expires_at against the supplied clock', () => {
    const sessions = [
      makeSession('expired', NOW - 1),
      makeSession('just-expired', NOW),
      makeSession('still-valid', NOW + 1),
    ]
    const view = buildLoginSessionsView(sessions, 1, NOW)

    assert.deepEqual(
      view.visible.map((entry) => entry.expired),
      [true, true, false]
    )
    assert.deepEqual(view.summary, { total: 3, active: 1, expired: 2 })
  })

  test('the stats describe the whole list, not just the visible page', () => {
    const sessions = [
      ...makeSessions(10),
      makeSession('gone-1', NOW - 10),
      makeSession('gone-2', NOW - 20),
      makeSession('gone-3', NOW - 30),
    ]
    const view = buildLoginSessionsView(sessions, 2, NOW)

    assert.equal(view.pageCount, 3)
    assert.equal(view.visible.length, LOGIN_SESSIONS_PAGE_SIZE)
    assert.deepEqual(view.summary, { total: 13, active: 10, expired: 3 })
    assert.equal(view.summary.active + view.summary.expired, view.summary.total)
  })
})
