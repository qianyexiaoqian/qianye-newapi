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
 * 划转流水的余额快照按展示币种（默认 USD）显示，不再印 quota 原始整数（需求 3）。
 *
 * # 守什么
 *
 * 项目方原话：「划转流水余额快照(转出 / 收款)，你转成 USD 显示不要显示这个原始值。」
 *
 * 这条断言分两层，缺任何一层都能给出假绿：
 *
 *   1. **组件确实换算了**（本文件下半段，真渲染）。只断言"页面里出现了
 *      `<QyBalanceSnapshot`"是挡不住"组件内部照样 `{props.before}`"的。
 *   2. **页面确实用上了它**（本文件上半段，AST）。组件写对了却没接到列上，
 *      正是本仓累计出现十几次的"断链"形状；同时反向钉住那段被替换掉的
 *      模板字符串不许回来。
 *
 * 换算口径不在这里自建：`QyAmountText` → `formatQuotaWithCurrency`，与钱包页
 * 余额、金额列同一套。所以下面的期望值用系统默认配置（USD、quotaPerUnit
 * 500000）算出来，一旦有人给 qy 加第二套汇率/精度，这几条会立刻变红。
 */
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import { readJsxTree } from '../../../__tests__/jsx-tree'

/* ── 一、页面确实接上了这个组件 ─────────────────────────────────────── */

// __tests__ → admin-transfer-records → pages → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..'
)
const page = readJsxTree(
  join(srcDir, 'features', 'qy', 'pages', 'admin-transfer-records', 'index.tsx')
)

describe('划转流水的快照列（AST）', () => {
  test('转出方与收款方各接一个 QyBalanceSnapshot', () => {
    assert.equal(
      page.occurrences('QyBalanceSnapshot').length,
      2,
      '快照列应当是转出、收款两行；数量不对说明有一方还在走旧写法'
    )
  })

  test('不再把 quota 原始整数插进单元格文本', () => {
    for (const field of [
      'from_quota_before',
      'from_quota_after',
      'to_quota_before',
      'to_quota_after',
    ]) {
      assert.ok(
        !page.source.includes(`\${row.${field}}`),
        `快照列又把 ${field} 原样拼进字符串了：主显示必须是换算后的金额`
      )
    }
  })
})

/* ── 二、组件确实做了换算 ───────────────────────────────────────────── */

const domWindow = new Window()
const domGlobals = [
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
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QyBalanceSnapshot } = await import('../components/balance-snapshot')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

async function mount(node: React.ReactNode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(node)
  })
  roots.push({ container, root })
  return container
}

after(async () => {
  for (const { container, root } of roots) {
    await act(async () => root.unmount())
    container.remove()
  }
})

describe('QyBalanceSnapshot 的展示口径', () => {
  // 默认展示配置：USD、quotaPerUnit = 500000。
  // 1500000 quota = $3，250000 quota = $0.5。
  test('主显示是金额，不是 quota 整数', async () => {
    const c = await mount(
      <QyBalanceSnapshot label='转出' before={1500000} after={250000} />
    )
    const text = c.textContent ?? ''

    assert.ok(text.includes('$3'), `没看到换算后的金额：${text}`)
    assert.ok(text.includes('$0.5'), `没看到换算后的金额：${text}`)
    // 原始整数不能出现在可见文本里 —— 这正是项目方点名要去掉的那个值。
    assert.ok(!text.includes('1500000'), `quota 原始值仍是主显示：${text}`)
    assert.ok(!text.includes('250000'), `quota 原始值仍是主显示：${text}`)
  })

  test('原始值保留在 hover 提示里（仲裁时要和后端账本逐位对）', async () => {
    const c = await mount(
      <QyBalanceSnapshot label='收款' before={1500000} after={250000} />
    )
    const holder = c.querySelector('[title]')
    assert.ok(holder != null, '快照行没有挂 title：原始值彻底看不到了')
    assert.equal(holder.getAttribute('title'), '1500000 → 250000')
  })

  test('两端都是 0 时也照常换算，不退回原始值', async () => {
    const c = await mount(
      <QyBalanceSnapshot label='转出' before={0} after={0} />
    )
    const text = c.textContent ?? ''
    assert.ok(text.includes('$0'), `零余额没有走金额口径：${text}`)
  })
})
