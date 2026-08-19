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
 * 「距离封号还剩」那一格必须与**真实处置**对得上。
 *
 * # 事故长什么样
 *
 * 处置由两条线的 OR 触发（账号总量线、单类型线），所以用户真正会被处置的时点由
 * **最先到达的那条线**决定。而这一格一度写成 `ban_threshold > 0 ? remaining : 不限`，
 * 后端的 `remaining` 又只算账号总量线。于是在「账号线 10、某一类 3」这种再普通
 * 不过的配置下，同一屏上：
 *
 *     页面顶部统计：距离封号还剩 8
 *     下方公示卡片：距离封号还差 1 次
 *
 * 用户下一次命中就被封了。封号之后那个数字还会变成 7 —— 一个每次调用都 403 的人，
 * 页面头条告诉他还有 7 次机会。
 *
 * 少给信息会让人保守，**给反了会让人放心**，所以这是同一张页面上最贵的一种错。
 *
 * # 这里钉住三件事
 *
 *   1. 已被处置时不再显示任何倒计时（`banned` 压倒一切）；
 *   2. 有没有线看 `remaining_line`，不看 `ban_threshold` —— 账号线关着、
 *      某一类开着 3 次是完全合法的配置，那种站点上 `ban_threshold` 是 0；
 *   3. 撞的是哪条线要说出来，且「已达门槛」与「已经被处置」是两句不同的话。
 *
 * 文案走真实的 `src/i18n/qy/zh.json`。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  qyRemainingDisplay,
  qyRemainingLineKey,
} from '../../../lib/violation-thresholds'

const here = dirname(fileURLToPath(import.meta.url))
const pagesDir = join(here, '..', '..')
const zhKeys = zh as Record<string, string>
const enKeys = en as Record<string, string>

/* ── 1. 三态本身 ─────────────────────────────────────────────────────── */

describe('距离处置还剩几次的三态', () => {
  const cases: {
    name: string
    summary: {
      ban_threshold: number
      remaining: number
      remaining_line?: 'none' | 'account' | 'category'
      banned?: boolean
    }
    want: ReturnType<typeof qyRemainingDisplay>
  }[] = [
    {
      name: '一条线都没配',
      summary: { ban_threshold: 0, remaining: 0, remaining_line: 'none' },
      want: { kind: 'none' },
    },
    {
      name: '账号总量线',
      summary: { ban_threshold: 10, remaining: 8, remaining_line: 'account' },
      want: { kind: 'countdown', line: 'account', remaining: 8 },
    },
    {
      // 事故本体：账号线 10、某一类 3，用户已命中 2 次。
      // 后端已经把 remaining 算成 1，前端不许再拿 ban_threshold 复算成 8。
      name: '单类型线更近时报的是单类型线的余量',
      summary: { ban_threshold: 10, remaining: 1, remaining_line: 'category' },
      want: { kind: 'countdown', line: 'category', remaining: 1 },
    },
    {
      // 这一条是 `ban_threshold > 0` 那个旧判据唯一会翻车的形状：
      // 账号线关着，用户离封号只有 2 次，旧代码写「不限」。
      name: '账号线关着但某一类开着 —— 绝不是"不限"',
      summary: { ban_threshold: 0, remaining: 2, remaining_line: 'category' },
      want: { kind: 'countdown', line: 'category', remaining: 2 },
    },
    {
      name: '已经被处置：倒计时一律作废',
      summary: {
        ban_threshold: 10,
        remaining: 7,
        remaining_line: 'account',
        banned: true,
      },
      want: { kind: 'banned' },
    },
    {
      name: '已被处置且余量为 0 时同样报已封，不报倒计时',
      summary: {
        ban_threshold: 3,
        remaining: 0,
        remaining_line: 'category',
        banned: true,
      },
      want: { kind: 'banned' },
    },
    {
      name: '旧后端没有 remaining_line 时回退到账号总量线',
      summary: { ban_threshold: 10, remaining: 4 },
      want: { kind: 'countdown', line: 'account', remaining: 4 },
    },
    {
      name: '旧后端且账号线也关着时按无门槛处理',
      summary: { ban_threshold: 0, remaining: 0 },
      want: { kind: 'none' },
    },
  ]

  for (const item of cases) {
    test(item.name, () => {
      assert.deepEqual(qyRemainingDisplay(item.summary), item.want)
    })
  }

  test('两条线各自有名字，且两个键都登记过', () => {
    const account = qyRemainingLineKey('account')
    const category = qyRemainingLineKey('category')
    assert.notEqual(
      account,
      category,
      '两条线共用一个说明 —— 同一个「还剩 1 次」落在账号线上和落在某一类上，用户该收敛的行为不是同一件事'
    )
    for (const key of [account, category]) {
      assert.ok(zhKeys[key], `zh 缺少 ${key}`)
      assert.ok(enKeys[key], `en 缺少 ${key}`)
    }
  })
})

/* ── 2. 页面确实用的是它 ─────────────────────────────────────────────── */

describe('违规记录页那一格的接线', () => {
  const source = readFileSync(join(pagesDir, 'violations', 'index.tsx'), 'utf8')

  test('顶部统计走 qyRemainingDisplay', () => {
    assert.ok(
      source.includes('qyRemainingDisplay('),
      '页面没有调用 qyRemainingDisplay —— 三态又被就地写成了三元'
    )
    assert.ok(
      source.includes('qy_vio_my_remaining_banned'),
      '已被处置时没有专门的文案，倒计时会继续显示给一个已经 403 的人'
    )
  })

  test('那一格连"这条线自己的窗口与阈值"一起说', () => {
    // 上面那个「N / M」统计块与它的窗口提示描述的始终是**账号总量线**。
    // 只报「触发线：类型」而不给类型线自己的数，被类型线封掉的人看到的就是
    // 「触发线：类型」配上「阈值 0、窗口 24 小时」—— 一句话里混着两条线的数字，
    // 而真正把他封掉的那条线是「阈值 2、不限期限」。
    assert.ok(
      source.includes('summary.remaining_threshold'),
      '没有用最近那条线自己的阈值 —— 提示里的分母仍是账号总量线的'
    )
    assert.ok(
      source.includes('summary.remaining_window_hours'),
      '没有用最近那条线自己的窗口 —— 不限期限的类型线会被写成 24 小时'
    )
    for (const key of [
      'qy_vio_my_remaining_line_scale',
      'qy_vio_my_remaining_line_scale_unlimited',
    ]) {
      assert.ok(source.includes(key), `页面没有用 ${key}`)
      assert.ok(zhKeys[key], `zh 缺少 ${key}`)
      assert.ok(enKeys[key], `en 缺少 ${key}`)
    }
  })

  test('那一格不再拿 ban_threshold 自己推倒计时', () => {
    // `ban_threshold` 只描述账号总量线。它还可以合法地用在「N / M」统计块与
    // 进度条上（那两处问的就是账号线），但**不许**再用来决定倒计时显示什么。
    assert.ok(
      !source.includes('summary.ban_threshold > 0 && summary.remaining'),
      '倒计时又回到了 ban_threshold 推导 —— 账号线关着时会把 2 次说成"不限"'
    )
    assert.ok(
      !source.includes(
        'summary.ban_threshold > 0\n                          ? summary.remaining'
      ),
      '倒计时又回到了 ban_threshold 推导'
    )
  })
})

/* ── 3. 公示卡片：「已达门槛」与「已被处置」是两句话 ─────────────────── */

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

async function mountCard(banned: boolean) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QyMyViolationCategoriesCard
        data={{
          account_threshold: 10,
          account_hit_count: 3,
          account_window_hours: 24,
          policy_action: 'ban',
          banned,
          threshold_semantics: 'any_line',
          items: [
            {
              id: 5,
              title: '指令注入',
              description: '',
              threshold: 3,
              window_hours: 24,
              hit_count: 3,
              remaining: 0,
            },
          ],
        }}
      />
    )
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
}

describe('公示卡片在"已达门槛"之后说的话', () => {
  test('尚未被处置时是预告', async () => {
    const container = await mountCard(false)
    const text = container.textContent ?? ''
    // 期望值独立算出：从真实 zh.json 原文自己插值。
    const want = zhKeys['qy_vio_cat_remaining_none'].replace(
      '{{action}}',
      zhKeys['qy_vio_policy_action_ban']
    )
    assert.ok(text.includes(want), `没有渲染出预告句，实际：${text}`)
  })

  test('已经被处置时改成"已按 X 处理"，不再预告一件已经发生的事', async () => {
    const container = await mountCard(true)
    const text = container.textContent ?? ''
    const done = zhKeys['qy_vio_cat_remaining_done'].replace(
      '{{action}}',
      zhKeys['qy_vio_policy_action_ban']
    )
    const stale = zhKeys['qy_vio_cat_remaining_none'].replace(
      '{{action}}',
      zhKeys['qy_vio_policy_action_ban']
    )
    assert.ok(text.includes(done), `没有渲染出"已处理"那一句，实际：${text}`)
    assert.ok(
      !text.includes(stale),
      '已经被封的人仍被告知"下一次违规才会被处理" —— 那是一句已经过期的预告'
    )
  })

  test('"已处理"那一句必须给出下一步（申诉），中英齐备', () => {
    for (const [lang, dict] of [
      ['zh', zhKeys],
      ['en', enKeys],
    ] as const) {
      const text = dict['qy_vio_cat_remaining_done']
      assert.ok(text, `${lang} 缺少 qy_vio_cat_remaining_done`)
      assert.ok(
        text.includes('{{action}}'),
        `${lang}: 处置动作被写死了，「仅记录」档下会变成一句吓人的假话`
      )
      assert.ok(
        lang === 'zh'
          ? text.includes('申诉')
          : text.toLowerCase().includes('appeal'),
        `${lang}: 已经被处置的人最需要的是申诉入口，这一句没有给`
      )
    }
  })
})
