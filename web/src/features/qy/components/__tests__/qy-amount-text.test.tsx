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
 * 佣金账本的 decimal 字符串与余额行的整数，必须渲染成**一模一样**的东西。
 *
 * # 这条不变量为什么值得单独钉
 *
 * `gross_amount` / `settled_amount` / `unsettled_amount` 与 `available_quota`
 * 是同一个单位：`gross = base_quota × 费率`（`accrual.go` 的 `calcGross`），
 * 结算把 `floor(carry + Δgross)` 直接加进 `available_quota`
 * （`settle.go` 的 `computeSettlement`）。差别只有"后端以字符串下发以免 JS
 * 丢位"这一件事。
 *
 * 只要展示层把这两条路走岔（一条走换算件、一条 `String()` 一下就印出来），
 * 界面上就会出现 `$0.27` 与 `1370.0000000000` 并排，而它们能相加。所以这里
 * 把「同一个数的两种下发形态渲染结果逐字相同」直接断言死。
 *
 * 源码级守卫（"哪些位置必须走换算件"）在
 * `lib/__tests__/commission-amount-display.test.ts`；这里守的是换算件本身
 * 在边界值上的行为。两条缺一不可：守卫挡的是接线，这里挡的是算错。
 *
 * 期望值按站点默认展示配置算出（USD、quotaPerUnit = 500000、汇率 1），
 * 与钱包页、日志页同一套；有人给 qy 加第二套汇率/精度，这几条会立刻变红。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import { qyQuotaValue } from '../../lib/format'

/* ── 一、解析：什么算数，什么不算 ───────────────────────────────────── */

describe('额度金额的解析', () => {
  const cases: {
    name: string
    input: number | string | null | undefined
    want: number | null
  }[] = [
    { name: '整数', input: 137_200, want: 137_200 },
    { name: '零', input: 0, want: 0 },
    { name: '负数（冲正行）', input: -137_200, want: -137_200 },
    { name: 'decimal 字符串', input: '137200.0000000000', want: 137_200 },
    {
      name: '负的 decimal 字符串',
      input: '-137200.0000000000',
      want: -137_200,
    },
    { name: '不足 1 额度的余数', input: '0.4700000000', want: 0.47 },
    { name: '带空白的字符串', input: '  137200.0000000000  ', want: 137_200 },
    // 下面这些必须落到 `-`，绝不能变成 0：0 是"这个人没有佣金"，
    // 而这些情况是"这个数没取到"。把取不到显示成 0 会让人以为钱没了。
    { name: '空串', input: '', want: null },
    { name: '纯空白', input: '   ', want: null },
    { name: '非数字', input: 'abc', want: null },
    { name: 'NaN', input: Number.NaN, want: null },
    { name: 'Infinity', input: Number.POSITIVE_INFINITY, want: null },
    { name: 'null', input: null, want: null },
    { name: 'undefined', input: undefined, want: null },
  ]

  for (const c of cases) {
    test(c.name, () => {
      assert.equal(qyQuotaValue(c.input), c.want)
    })
  }
})

/* ── 二、渲染 ───────────────────────────────────────────────────────── */

const domWindow = new Window()
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QyAmountText } = await import('../qy-amount-text')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

after(async () => {
  for (const { container, root } of roots) {
    await act(async () => root.unmount())
    container.remove()
  }
})

async function render(node: React.ReactNode): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(node)
  })
  roots.push({ container, root })
  return container
}

describe('QyAmountText 的展示口径', () => {
  const cases: {
    name: string
    quota: number | string | null | undefined
    want: string
  }[] = [
    { name: '零余额照常换算，不退回原始 0', quota: 0, want: '$0' },
    { name: '一笔典型佣金', quota: 137_200, want: '$0.2744' },
    { name: '冲正行是负数', quota: -137_200, want: '-$0.2744' },
    // 旧的 int32 上界。它今天只是一个普通的大额度（common.MaxQuota 已是 2^43），
    // 留在表里是因为这一档的**四位分组 + 两位小数**渲染正是在它上面出的问题。
    { name: '十亿量级的额度', quota: 2_147_483_647, want: '$4,294.97' },
    // 后端 `amountSane` 的闸门是 1e19，落库前就被拒；这里只保证它不会
    // 渲染成 `NaN` 或者把整行撑爆。
    {
      name: '账本的合理性闸门',
      quota: '9999999999999999999.9999999999',
      want: '$20,000,000,000,000',
    },
    { name: '取不到的值显示短横', quota: null, want: '-' },
    { name: '空串同样是短横，不是 0', quota: '', want: '-' },
  ]

  for (const c of cases) {
    test(c.name, async () => {
      const container = await render(<QyAmountText quota={c.quota} />)
      assert.equal(container.textContent, c.want)
    })
  }

  test('负数带 destructive 着色，正数不带', async () => {
    const negative = await render(<QyAmountText quota={-137_200} />)
    const positive = await render(<QyAmountText quota={137_200} />)
    assert.match(
      negative.querySelector('span')?.className ?? '',
      /text-destructive/,
      '冲正与手工扣减在流水里必须一眼看出方向'
    )
    assert.doesNotMatch(
      positive.querySelector('span')?.className ?? '',
      /text-destructive/
    )
  })

  test('signed 打开时正数带 +，零与负数不带', async () => {
    const plus = await render(<QyAmountText quota={137_200} signed />)
    const zero = await render(<QyAmountText quota={0} signed />)
    const minus = await render(<QyAmountText quota={-137_200} signed />)
    assert.equal(plus.textContent, '+$0.2744')
    assert.equal(zero.textContent, '$0')
    assert.equal(minus.textContent, '-$0.2744')
  })
})

describe('decimal 字符串与整数是同一个数', () => {
  // 本文件的核心断言：佣金账本那一路（字符串）与余额那一路（整数）
  // 渲染结果必须逐字相同 —— 它们本来就是同一个单位，只是下发形态不同。
  const pairs: [number, string][] = [
    [0, '0.0000000000'],
    [137_200, '137200.0000000000'],
    [-137_200, '-137200.0000000000'],
    [2_147_483_647, '2147483647.0000000000'],
  ]

  for (const [asNumber, asString] of pairs) {
    test(`${asNumber} 与 "${asString}"`, async () => {
      const a = await render(<QyAmountText quota={asNumber} />)
      const b = await render(<QyAmountText quota={asString} />)
      assert.equal(
        b.textContent,
        a.textContent,
        '同一笔钱的两种下发形态渲染出了两个样子，看的人无从判断这两列能不能相加'
      )
    })
  }

  test('不足 1 额度的余数不显示成 0', async () => {
    // 「我用了一整天怎么没佣金」这个问题的答案就在这个数上。显示成 $0
    // 等于告诉用户平台把钱吞了。
    const container = await render(<QyAmountText quota='0.4700000000' />)
    assert.notEqual(container.textContent, '$0')
    assert.notEqual(container.textContent, '-')
  })
})
