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
 * 用户端那一句话:「你违规了【X】N 次,到 M 次封号」。
 *
 * # 事故长什么样
 *
 * 项目方原话:「用户前端我的违规那展示的,当前是『你违规了【绕过安全策略】0 次。』
 * 你要显示达到多少次封号。」—— 现网十四个公示类型的 threshold 全是 0,于是
 * 每一行都落进「这一类仅记录,不计封号门槛」那一支,「到多少次封号」在界面上
 * 等于不存在。
 *
 * 修法是给类型配上阈值,而这一步会**同时**打开另一个坑:一旦有人图省事把这一格
 * 写成永远走 `qy_vio_cat_sentence`,还没配线的类型就会显示成
 *
 *     你违规了【网络攻击与逆向】0 次,到 0 次封号(24 小时内累计)。
 *
 * ——「到 0 次封号」是一句字面上说"你已经该被封了"的话,而后端的 0 恰恰是
 * "这一类不单独计门槛"。少显示一个数字只是信息不全,**显示反了会让人吓到、
 * 或者(更糟)让读到别人那一行 0 的人以为门槛已经作废**。
 *
 * # 这里钉住四件事
 *
 *  1. 配了线的类型:一整句必须同时给出"我几次"「到几次」「到了会怎样」「窗口多长」;
 *  2. 没配线的类型:必须是「仅记录」那一句,而且整块渲染里**一个「到 0 次」都不许出现**;
 *  3. 已达门槛的两种形态(尚未处置 / 已处置)是两句不同的话;
 *  4. 处置动作跟着策略档走,不写死 —— 「仅记录」档下写"封号"是一句吓人的假话。
 *
 * 期望值一律从真实的 `src/i18n/qy/zh.json` 自己插值算出来,不抄字面量:
 * 抄字面量的用例在文案改了之后会跟着改,于是永远绿。
 *
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

import type { QyMyViolationCategories } from '../types'

const zhKeys = zh as Record<string, string>

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

const { QyMyViolationCategoriesCard } = await import(
  '../components/categories-card'
)

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

/** 从真实 zh.json 自己插值,得到期望的那一整句。 */
function say(key: string, vars: Record<string, string | number>): string {
  let out = zhKeys[key]
  assert.ok(out, `zh.json 缺少 ${key}`)
  for (const [name, value] of Object.entries(vars)) {
    out = out.replaceAll(`{{${name}}}`, String(value))
  }
  assert.ok(
    !out.includes('{{'),
    `${key} 还有没被替换的占位符,期望值算错了:${out}`
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

/** 现网 `GET /api/qy/violation/my-categories` 的真实形状(公示白名单只有这六列)。 */
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

/* ── 1. 三态各说各的话 ─────────────────────────────────────────────── */

describe('公示卡片上每一类的那一整句', () => {
  const cases: {
    name: string
    row: Item
    banned: boolean
    /** 必须出现的句子(自己从 zh.json 插值算出)。 */
    want: () => string[]
    /** 必须**不**出现的句子。 */
    notWant: () => string[]
  }[] = [
    {
      name: '配了线、还没到:给出门槛与还差几次',
      row: item({
        id: 3,
        title: '套取系统设定',
        threshold: 3,
        hit_count: 1,
        remaining: 2,
      }),
      banned: false,
      want: () => [
        say('qy_vio_cat_sentence', {
          title: '套取系统设定',
          hit: 1,
          threshold: 3,
          hours: 24,
          action: BAN,
        }),
        say('qy_vio_cat_remaining', { remaining: 2, action: BAN }),
      ],
      notWant: () => [
        say('qy_vio_cat_sentence_off', { title: '套取系统设定', hit: 1 }),
      ],
    },
    {
      name: '配了线、刚好到:门槛句照旧,下面换成预告',
      row: item({
        id: 2,
        title: '绕过安全策略',
        threshold: 3,
        hit_count: 3,
        remaining: 0,
      }),
      banned: false,
      want: () => [
        say('qy_vio_cat_sentence', {
          title: '绕过安全策略',
          hit: 3,
          threshold: 3,
          hours: 24,
          action: BAN,
        }),
        say('qy_vio_cat_remaining_none', { action: BAN }),
      ],
      notWant: () => [say('qy_vio_cat_remaining_done', { action: BAN })],
    },
    {
      name: '已经被处置:不再预告一件已经发生的事',
      row: item({
        id: 2,
        title: '绕过安全策略',
        threshold: 3,
        hit_count: 4,
        remaining: 0,
      }),
      banned: true,
      want: () => [say('qy_vio_cat_remaining_done', { action: BAN })],
      notWant: () => [say('qy_vio_cat_remaining_none', { action: BAN })],
    },
    {
      name: '没配线:必须是「仅记录」,绝不是「到 0 次封号」',
      row: item({ id: 131, title: '网络攻击与逆向', threshold: 0 }),
      banned: false,
      want: () => [
        say('qy_vio_cat_sentence_off', { title: '网络攻击与逆向', hit: 0 }),
      ],
      notWant: () => [
        say('qy_vio_cat_sentence', {
          title: '网络攻击与逆向',
          hit: 0,
          threshold: 0,
          hours: 24,
          action: BAN,
        }),
        // 「还差 0 次」同样不该出现:没有线就没有"还差几次"这回事。
        say('qy_vio_cat_remaining', { remaining: 0, action: BAN }),
        say('qy_vio_cat_remaining_none', { action: BAN }),
      ],
    },
  ]

  for (const c of cases) {
    test(c.name, async () => {
      const text = await mount([c.row], { banned: c.banned })
      for (const want of c.want()) {
        assert.ok(text.includes(want), `少了这一句:${want}\n实际:${text}`)
      }
      for (const no of c.notWant()) {
        assert.ok(!text.includes(no), `不该出现这一句:${no}\n实际:${text}`)
      }
    })
  }
})

/* ── 2. 配了线与没配线同屏:这是现网当下的真实形态 ───────────────────── */

describe('配了线与没配线的类型同屏时', () => {
  /**
   * 现网就是这个样子:五个本仓自有类型配了线,九个上游合规类型还没配。
   * 两支分别渲染,同屏里既要有「到 3 次封号」,也要有「仅记录」,
   * 而**任何一行都不许出现「到 0 次」**。
   */
  const mixed: Item[] = [
    item({
      id: 2,
      title: '绕过安全策略',
      threshold: 3,
      hit_count: 3,
      remaining: 0,
    }),
    item({
      id: 6,
      title: '上游安全拒绝',
      threshold: 10,
      hit_count: 0,
      remaining: 10,
    }),
    item({ id: 131, title: '网络攻击与逆向', threshold: 0 }),
    item({ id: 138, title: '自伤与自杀', threshold: 0 }),
  ]

  test('两支各说各的,且整块渲染里没有「到 0 次」', async () => {
    const text = await mount(mixed)

    assert.ok(
      text.includes(
        say('qy_vio_cat_sentence', {
          title: '绕过安全策略',
          hit: 3,
          threshold: 3,
          hours: 24,
          action: BAN,
        })
      ),
      `配了线的那一行没给出门槛:${text}`
    )
    assert.ok(
      text.includes(
        say('qy_vio_cat_sentence_off', { title: '网络攻击与逆向', hit: 0 })
      ),
      `没配线的那一行没落到「仅记录」:${text}`
    )

    // 这一条是整个文件的核心:threshold=0 的语义是"不单独计门槛",
    // 把它插进「到 {{threshold}} 次」的句式会得到一句意思相反的话。
    // 用句式里 threshold 前后的固定片段去搜,而不是搜 "0" —— 后者会被
    // hit_count 的 0 命中,那是一次假绿。
    const zeroLine = say('qy_vio_cat_sentence', {
      title: '任意',
      hit: 9,
      threshold: 0,
      hours: 24,
      action: BAN,
    })
    const zeroFragment = zeroLine.slice(zeroLine.indexOf('9 次') + 2)
    assert.ok(
      zeroFragment.includes('0'),
      `取到的片段不含那个 0,断言会退化成永远通过:${zeroFragment}`
    )
    assert.ok(
      !text.includes(zeroFragment),
      `渲染里出现了「到 0 次${BAN}」这种意思相反的话:${text}`
    )
  })
})

/* ── 3. 变异验证 ───────────────────────────────────────────────────── */

/**
 * 上面那些断言必须真的**跟着 threshold 走**,而不是碰巧永远成立。
 *
 * 两个方向都要验:
 *   - 把没配线的那一行改成配了线 → 必须从「仅记录」变成「到 M 次」;
 *   - 把配了线的那一行改成没配线 → 必须从「到 M 次」变回「仅记录」。
 *
 * 只验一个方向的话,一份"永远走仅记录"或"永远走门槛句"的实现都能骗过用例。
 */
describe('变异验证:这一格真的由 threshold 决定', () => {
  test('0 → 3:同一行从「仅记录」变成「到 3 次封号」', async () => {
    const off = await mount([item({ id: 131, title: '网络攻击与逆向' })])
    const on = await mount([
      item({
        id: 131,
        title: '网络攻击与逆向',
        threshold: 3,
        hit_count: 0,
        remaining: 3,
      }),
    ])

    const offLine = say('qy_vio_cat_sentence_off', {
      title: '网络攻击与逆向',
      hit: 0,
    })
    const onLine = say('qy_vio_cat_sentence', {
      title: '网络攻击与逆向',
      hit: 0,
      threshold: 3,
      hours: 24,
      action: BAN,
    })

    assert.ok(off.includes(offLine) && !off.includes(onLine))
    assert.ok(on.includes(onLine) && !on.includes(offLine))
    assert.notEqual(off, on, '改了 threshold 而渲染一个字没变,这一格根本没接上')
  })

  test('3 → 0:同一行从「到 3 次封号」变回「仅记录」,并且不再有倒计时', async () => {
    const on = await mount([
      item({
        id: 2,
        title: '绕过安全策略',
        threshold: 3,
        hit_count: 1,
        remaining: 2,
      }),
    ])
    const off = await mount([
      item({ id: 2, title: '绕过安全策略', hit_count: 1 }),
    ])

    assert.ok(on.includes(say('qy_vio_cat_remaining', { remaining: 2, action: BAN })))
    assert.ok(
      !off.includes('还差'),
      `没有线的类型不该有「还差几次」:${off}`
    )
    assert.ok(
      off.includes(say('qy_vio_cat_sentence_off', { title: '绕过安全策略', hit: 1 })),
      `落回「仅记录」失败:${off}`
    )
  })

  test('处置动作跟着策略档走,不是写死的「封号」', async () => {
    const banned = await mount([
      item({
        id: 2,
        title: '绕过安全策略',
        threshold: 3,
        hit_count: 1,
        remaining: 2,
      }),
    ])
    const recorded = await mount(
      [
        item({
          id: 2,
          title: '绕过安全策略',
          threshold: 3,
          hit_count: 1,
          remaining: 2,
        }),
      ],
      { policy_action: 'record' }
    )

    const recordLabel = zhKeys['qy_vio_policy_action_record']
    assert.ok(banned.includes(BAN))
    assert.ok(
      recorded.includes(
        say('qy_vio_cat_sentence', {
          title: '绕过安全策略',
          hit: 1,
          threshold: 3,
          hours: 24,
          action: recordLabel,
        })
      ),
      `「仅记录」档下仍然在说封号,那是一句吓人的假话:${recorded}`
    )
  })
})
