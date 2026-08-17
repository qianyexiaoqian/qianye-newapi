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
/*
 * 统计窗口设成「不限期限」之后，用户端公示那一句话的时间口径。
 *
 * # 事故长什么样
 *
 * 公示卡片上写的是「你违规了【X】3 次，到 5 次封号（24 小时内累计）」。窗口改成
 * 不限期限之后，后端下发的 `window_hours` 是哨兵 -1。这一格如果不动，用户会读到
 *
 *     你违规了【X】3 次，到 5 次封号（-1 小时内累计）。
 *
 * 而**更糟的修法**是把 -1 折成 24 再显示：那不是乱码，是一句读起来完全正常的
 * 假话 —— 用户会以为等一天就清零，于是继续踩线，而实际那三次永远算数、
 * 第五次就被封。少给一个数字只是信息不全，给一个错的时间口径是主动误导。
 *
 * # 这里钉住三件事
 *
 *  1. 有限窗口照旧说「N 小时内累计」（既有行为不能被改坏）；
 *  2. 不限期限时整句换成不带时间口径的那一句，且**整块渲染里不许出现小时数**；
 *  3. 账号总量线那一条同样要换（它与每一类的线并排显示，只换一半更难读）。
 *
 * 期望值一律从真实的 `src/i18n/qy/zh.json` 自己插值算出来，不抄字面量。
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

import type { QyMyViolationCategories } from '../types'

const zhKeys = zh as Record<string, string>

/** 与后端 `violation.WindowUnlimited` 同值。 */
const UNLIMITED = -1

const domWindow = new Window({ height: 900, width: 1280 })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhKeys } },
})

const { QyMyViolationCategoriesCard } =
  await import('../components/categories-card')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: { container: HTMLElement; root: { unmount: () => void } }[] = []
after(() => {
  for (const entry of roots) {
    entry.root.unmount()
    entry.container.remove()
  }
})

/** 从真实 zh.json 自己插值，得到期望的那一整句。 */
function say(key: string, vars: Record<string, string | number>): string {
  let out = zhKeys[key]
  assert.ok(out, `zh.json 缺少 ${key}`)
  for (const [name, value] of Object.entries(vars)) {
    out = out.replaceAll(`{{${name}}}`, String(value))
  }
  assert.ok(
    !out.includes('{{'),
    `${key} 还有没被替换的占位符，期望值算错了：${out}`
  )
  return out
}

type Item = QyMyViolationCategories['items'][number]

async function mount(
  items: Item[],
  overrides: Partial<QyMyViolationCategories> = {}
): Promise<string> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QyMyViolationCategoriesCard
        data={{
          account_threshold: 0,
          account_hit_count: 0,
          account_window_hours: 24,
          policy_action: 'ban',
          banned: false,
          threshold_semantics: 'any_line',
          items,
          ...overrides,
        }}
      />
    )
  })
  await act(async () => {})
  roots.push({ container, root })
  return container.textContent ?? ''
}

function item(over: Partial<Item> & Pick<Item, 'id' | 'title'>): Item {
  return {
    description: '',
    threshold: 0,
    window_hours: 24,
    hit_count: 0,
    remaining: 0,
    ...over,
  } as Item
}

const BAN = zhKeys['qy_vio_policy_action_ban']

/* ── 1. 单类型线的三态 ─────────────────────────────────────────────── */

describe('单个违规类型那一句里的时间口径', () => {
  const row = (windowHours: number): Item =>
    item({
      id: 2,
      title: '绕过安全策略',
      threshold: 5,
      hit_count: 3,
      remaining: 2,
      window_hours: windowHours,
    })

  test('有限窗口：照旧说「N 小时内累计」', async () => {
    const text = await mount([row(24)])
    assert.ok(
      text.includes(
        say('qy_vio_cat_sentence', {
          title: '绕过安全策略',
          hit: 3,
          threshold: 5,
          hours: 24,
          action: BAN,
        })
      ),
      `有限窗口的既有说法被改坏了：${text}`
    )
  })

  test('不限期限：整句换成不带时间口径的那一句', async () => {
    const text = await mount([row(UNLIMITED)])
    assert.ok(
      text.includes(
        say('qy_vio_cat_sentence_unlimited', {
          title: '绕过安全策略',
          hit: 3,
          threshold: 5,
          action: BAN,
        })
      ),
      `不限期限没有换句：${text}`
    )
  })

  test('不限期限：渲染里不许出现任何小时数，尤其不许出现 24', async () => {
    // 两条线一起设成不限期限：这一条断言的是**整块渲染**里没有时间口径，
    // 留一条有限的账号线在上面，它自己那句合法的「24 小时内」会让断言失效。
    const text = await mount([row(UNLIMITED)], {
      account_window_hours: UNLIMITED,
    })
    // -1 是"没换句"，24 是"折成了一个具体小时数"——后者更危险，因为它读起来
    // 完全正常。两种都必须为假。
    assert.ok(!text.includes('-1'), `哨兵被直接渲染出来了：${text}`)
    assert.ok(
      !text.includes('24 小时'),
      `窗口被折成了 24 小时。这不是乱码，是一句读起来正常的假话：${text}`
    )
    assert.ok(
      !text.includes('小时内'),
      `不限期限的句子里仍然有时间口径：${text}`
    )
  })
})

/* ── 2. 账号总量线 ─────────────────────────────────────────────────── */

describe('账号总量线那一条的时间口径', () => {
  test('配了阈值：不限期限时换成「累计」那一句', async () => {
    const text = await mount([], {
      account_threshold: 10,
      account_hit_count: 4,
      account_window_hours: UNLIMITED,
      items: [item({ id: 1, title: '任意' })],
    })
    assert.ok(
      text.includes(
        say('qy_vio_cat_account_line_unlimited', {
          hit: 4,
          threshold: 10,
          action: BAN,
        })
      ),
      `账号线没有换句：${text}`
    )
    assert.ok(!text.includes('-1'), `哨兵被直接渲染出来了：${text}`)
  })

  test('没配阈值：不限期限时同样换句，而不是留一个 24 小时的假口径', async () => {
    const text = await mount([item({ id: 1, title: '任意' })], {
      account_threshold: 0,
      account_hit_count: 4,
      account_window_hours: UNLIMITED,
    })
    assert.ok(
      text.includes(say('qy_vio_cat_account_line_off_unlimited', { hit: 4 })),
      `账号线（未设门槛）没有换句：${text}`
    )
    assert.ok(
      !text.includes(say('qy_vio_cat_account_line_off', { hit: 4, hours: 24 })),
      `账号线仍然在说 24 小时：${text}`
    )
  })
})

/* ── 3. 变异验证 ───────────────────────────────────────────────────── */

/**
 * 上面那些断言必须真的**跟着 window_hours 走**，而不是碰巧永远成立。
 *
 * 两个方向都验：
 *   - 24 → 不限期限：必须从带小时数的那句变成不带的；
 *   - 不限期限 → 24：必须变回带小时数的那句。
 *
 * 只验一个方向的话，一份"永远走无限句"或"永远走小时句"的实现都能骗过用例。
 */
describe('变异验证：这一格真的由 window_hours 决定', () => {
  const row = (windowHours: number): Item =>
    item({
      id: 2,
      title: '绕过安全策略',
      threshold: 5,
      hit_count: 3,
      remaining: 2,
      window_hours: windowHours,
    })

  test('24 ↔ 不限期限：两个方向各自换句，且两次渲染确实不同', async () => {
    const finite = await mount([row(24)])
    const unlimited = await mount([row(UNLIMITED)])

    const finiteLine = say('qy_vio_cat_sentence', {
      title: '绕过安全策略',
      hit: 3,
      threshold: 5,
      hours: 24,
      action: BAN,
    })
    const unlimitedLine = say('qy_vio_cat_sentence_unlimited', {
      title: '绕过安全策略',
      hit: 3,
      threshold: 5,
      action: BAN,
    })

    assert.ok(finite.includes(finiteLine) && !finite.includes(unlimitedLine))
    assert.ok(
      unlimited.includes(unlimitedLine) && !unlimited.includes(finiteLine)
    )
    assert.notEqual(
      finite,
      unlimited,
      '改了 window_hours 而渲染一个字没变，这一格根本没接上'
    )
  })

  test('两句话本身必须不同：文案抄成一样的话上面的断言全部退化', async () => {
    assert.notEqual(
      zhKeys['qy_vio_cat_sentence'],
      zhKeys['qy_vio_cat_sentence_unlimited']
    )
    assert.ok(
      !zhKeys['qy_vio_cat_sentence_unlimited'].includes('{{hours}}'),
      '「不限期限」那一句里还有 {{hours}} 占位符，它会渲染成 -1 小时'
    )
    assert.ok(
      !zhKeys['qy_vio_cat_account_line_unlimited'].includes('{{hours}}'),
      '账号线的「不限期限」句里还有 {{hours}} 占位符'
    )
    assert.ok(
      !zhKeys['qy_vio_cat_account_line_off_unlimited'].includes('{{hours}}')
    )
  })
})
