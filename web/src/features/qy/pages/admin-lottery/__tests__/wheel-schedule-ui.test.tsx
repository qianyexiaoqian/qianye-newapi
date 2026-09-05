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
/**
 * 已发布转盘的「改排期」：入口真的挂在详情页上，弹窗提交的请求体是后端认的形状。
 *
 * 项目方原话（2026-09-04）：「抽奖活动对于星屑转盘为何无法更改开启时间，或提前
 * 结束转盘，编辑转盘？」「转盘应像游戏抽卡卡池一样：开始时间、结束时间就可以了。」
 *
 * 两层：
 *   1. 源码：详情页对 `draw_mode='wheel' && status='published'` 渲染「改排期」与
 *      「立即开始」，判据只有一处；错误码有登记、两种语言都有话说；
 *   2. happy-dom：把弹窗真的挂起来、改一格结束时间、点提交，断言打出去的是
 *      `PUT /admin/lottery/activities/:act_no/schedule`，body 恰好 `{open_at, close_at}`
 *      两格 unix 秒 —— 后端 `ShouldBindJSON` 对未知字段静默丢弃、对缺字段按 0 处理，
 *      发错形状的表现是 400「两个时刻都必须填写」，而不是编译错误。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

const dir = dirname(fileURLToPath(import.meta.url))
const read = (...p: string[]) => readFileSync(join(dir, '..', ...p), 'utf8')
const detail = read('detail.tsx')
const dialog = read('components', 'lottery-schedule-dialog.tsx')

// 排期文案已并入主包(集成前曾在 pending-schedule.*.json 片段里)。
const zhKeys: Record<string, string> = zh
const enKeys: Record<string, string> = en

describe('已发布转盘的详情页有改排期的入口', () => {
  test('判据只有一处：转盘 && published', () => {
    assert.ok(
      detail.includes(
        "const canEditSchedule = isWheel && activity?.status === 'published'"
      ),
      '判据必须与后端那条 `WHERE status=published` 同一口径，且只写一次'
    )
    assert.ok(detail.includes("t('qy_lot_schedule_change')"))
    assert.ok(detail.includes('QyLotScheduleDialog'))
    // 「立即开始」只对还没到开始时间的场次渲染：已经开放的转盘按下去什么都不变。
    assert.ok(detail.includes('const canStartNow ='))
    assert.ok(detail.includes("t('qy_lot_schedule_start_now')"))
    assert.ok(detail.includes('QyConfirmDialog'))
    // 立即开始走的是同一条接口，open_at = now、结束时间原样回传。
    assert.match(detail, /open_at: Math\.floor\(Date\.now\(\) \/ 1000\)/)
  })

  test('弹窗不从向导文件 import 时刻输入格', () => {
    assert.ok(!dialog.includes('lottery-create-wizard'))
    assert.ok(dialog.includes("type='datetime-local'"))
  })

  test('两个错误码有登记、两种语言都有话说', async () => {
    const { QY_ERROR_CODE_I18N } = await import('../../../lib/api')
    for (const code of [
      'qy_lot_wheel_schedule_locked',
      'qy_lot_schedule_not_wheel',
    ]) {
      const key = QY_ERROR_CODE_I18N[code]
      assert.ok(key != null, `${code} 没有登记进 QY_ERROR_CODE_I18N`)
      assert.ok(zhKeys[key] != null, `缺少 ${key} 的中文文案`)
      assert.ok(enKeys[key] != null, `缺少 ${key} 的英文文案`)
      // 「从此做不到」不能塌成「刷新后重试」。
      assert.notEqual(key, QY_ERROR_CODE_I18N.qy_lot_status_conflict)
    }
    // 事件流里的新动作要有名字，否则表格里显示的是裸的 schedule_changed。
    assert.ok(zhKeys.qy_lot_event_schedule_changed != null)
    assert.ok(enKeys.qy_lot_event_schedule_changed != null)
  })
})

// ───────────────────────────── happy-dom ─────────────────────────────

const domWindow = new Window({ height: 900, width: 1280 })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLTextAreaElement',
  'HTMLButtonElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MouseEvent',
  'PointerEvent',
  'KeyboardEvent',
  'MutationObserver',
  'ResizeObserver',
  'IntersectionObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'DOMRect',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}
for (const name of ['scrollTo', 'scrollIntoView'] as const) {
  const noop = () => {}
  Object.defineProperty(globalThis, name, { configurable: true, value: noop })
  Object.defineProperty(domWindow, name, { configurable: true, value: noop })
}
Object.defineProperty(globalThis.Element.prototype, 'scrollIntoView', {
  configurable: true,
  value: () => {},
})
// QyResponsiveDialog 靠 `(max-width: 767px)` 判桌面 / 移动，这里固定成桌面。
Object.defineProperty(globalThis, 'matchMedia', {
  configurable: true,
  value: (queryText: string) => ({
    matches: false,
    media: queryText,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false,
  }),
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { QyLotScheduleDialog } =
  await import('../components/lottery-schedule-dialog')
type QyLotAdminActivity = import('../types').QyLotAdminActivity

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhKeys } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const originalAdapter = api.defaults.adapter
const mounted: { container: HTMLElement; root: { unmount: () => void } }[] = []

after(async () => {
  api.defaults.adapter = originalAdapter
  for (const entry of mounted) {
    await act(async () => {
      entry.root.unmount()
    })
    entry.container.remove()
  }
  mounted.length = 0
})

type Sent = { method: string; url: string; body: unknown }

/** 一场已发布、已经开放一分钟、还剩一小时的转盘。 */
function publishedWheel(): QyLotAdminActivity {
  const now = Math.floor(Date.now() / 1000)
  return {
    act_no: 'LW-SCHED-1',
    kind: 'draw',
    draw_mode: 'wheel',
    status: 'published',
    outcome: '',
    title: '星屑转盘',
    intro: '',
    stake_quota: 100,
    open_at: now - 60,
    close_at: now + 3600,
    draw_at: now + 3600,
    settle_deadline: 0,
    commit_hash: 'c'.repeat(64),
    rules_hash: 'r'.repeat(64),
    spec_hash: 's'.repeat(64),
    algo: 'lot-v2',
    rules_text: '{}',
    allow_multi_win: false,
    fee_bps: 0,
    min_entries_to_hold: 0,
    max_entries_per_user: 0,
    max_attempts_per_user: 0,
    max_total_entries: 0,
    max_total_users: 0,
    max_per_inviter: 0,
    cooldown_seconds: 0,
    dedup_ip: false,
    bet_min_quota: 0,
    bet_max_quota: 0,
    entry_seq: 0,
    active_count: 0,
    pool_quota: 0,
    platform_fee_quota: 0,
    payout_quota: 0,
    refund_quota: 0,
    roster_hash: '',
    roster_count: 0,
    chain_head: '',
    win_option_id: 0,
    result_evidence: '',
    result_by: 0,
    cancel_reason: '',
    created_by: 1,
    created_at: now - 600,
    published_at: now - 300,
    locked_at: 0,
    revealed_at: 0,
    settled_at: 0,
    hidden_at: 0,
    hidden_by: 0,
    hidden_reason: '',
  }
}

const collapse = (raw: string) => raw.replaceAll(/\s+/g, ' ').trim()
const settle = async () => {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 60))
  })
}

async function mountDialog(activity: QyLotAdminActivity) {
  const sent: Sent[] = []
  api.defaults.adapter = async (config) => {
    let body: unknown = config.data
    if (typeof body === 'string') body = JSON.parse(body)
    sent.push({
      method: String(config.method ?? 'get').toUpperCase(),
      url: String(config.url ?? ''),
      body,
    })
    return {
      config,
      data: { success: true, message: '', data: {} },
      headers: {},
      status: 200,
      statusText: 'OK',
    }
  }
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <QyLotScheduleDialog open onOpenChange={() => {}} activity={activity} />
      </QueryClientProvider>
    )
  })
  await settle()
  mounted.push({ container, root })

  const field = (label: string) => {
    const tag = [...document.body.querySelectorAll('label')].find(
      (node) => collapse(node.textContent ?? '') === label
    )
    assert.ok(tag, `找不到输入格 ${label}`)
    const target = document.querySelector(`[id="${tag.htmlFor}"]`)
    assert.ok(target instanceof HTMLInputElement, `${label} 不是输入框`)
    return target
  }
  const typeInto = async (node: HTMLInputElement, text: string) => {
    await act(async () => {
      // React 19 + happy-dom：直接改 `.value` 会被 React 的受控值追踪吞掉，
      // 必须走原型上的 setter 再派发 input 事件。
      Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        'value'
      )?.set?.call(node, text)
      node.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  const button = (label: string) =>
    [...document.body.querySelectorAll<HTMLButtonElement>('button')].find(
      (node) => collapse(node.textContent ?? '') === label
    ) ?? null
  return { sent, field, typeInto, button }
}

/** 与 `lib/datetime.ts` 的 `qyLotToLocalInput` 同一格式，但独立写一遍：夹具不该依赖被测代码。 */
function localInput(seconds: number): string {
  const date = new Date(seconds * 1000)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

describe('改排期弹窗提交的请求体', () => {
  test('改结束时间并提交 → PUT …/schedule，body 恰好 {open_at, close_at}', async () => {
    const activity = publishedWheel()
    const screen = await mountDialog(activity)

    const submit = screen.button(zhKeys.qy_common_submit)
    assert.ok(submit, '弹窗里没有提交键')
    assert.equal(submit.disabled, true, '没改任何一格时不该能提交')

    // 结束时间往后推两小时（按分钟对齐：datetime-local 只到分钟）。
    const newClose = Math.floor((activity.close_at + 7200) / 60) * 60
    await screen.typeInto(
      screen.field(zhKeys.qy_lot_wheel_end_at),
      localInput(newClose)
    )
    assert.equal(submit.disabled, false)
    await act(async () => {
      submit.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await settle()

    assert.equal(screen.sent.length, 1, '应当恰好打出一条请求')
    const [req] = screen.sent
    assert.equal(req.method, 'PUT')
    assert.ok(
      req.url.endsWith('/admin/lottery/activities/LW-SCHED-1/schedule'),
      req.url
    )
    assert.deepEqual(req.body, {
      open_at: activity.open_at,
      close_at: newClose,
    })
  })

  test('结束时间早于开始 → 本地就拒，一条请求都不发', async () => {
    const activity = publishedWheel()
    const screen = await mountDialog(activity)
    await screen.typeInto(
      screen.field(zhKeys.qy_lot_wheel_end_at),
      localInput(activity.open_at - 3600)
    )
    const submit = screen.button(zhKeys.qy_common_submit)
    assert.ok(submit)
    assert.equal(submit.disabled, true)
    assert.ok(
      collapse(document.body.textContent ?? '').includes(
        zhKeys.qy_lot_schedule_v_order
      )
    )
    assert.equal(screen.sent.length, 0)
  })
})
