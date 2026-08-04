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
 * 「余额划转」与「添加资金 / 订阅套餐」并列（需求 5）。
 *
 * # 守什么
 *
 * 项目方原话：「余额划转这个 tab 你和 添加资金/订阅套餐 这样放到一起，
 * 不要在底部添加，丑死了。」这件事只有"挂在哪"能证伪，所以断言分两层：
 *
 *   1. **挂载点（AST）**：解析上游 `features/wallet/index.tsx`，按 JSX 祖先链
 *      判定触发器确实在 `<TabsList>` 里、面板确实在 `<Tabs>` 里而不在
 *      `<TabsList>` 里、入口卡确实在 `<Tabs>` 外。
 *      本仓反复出现的形状正是"组件写好了却挂在插槽外，从没被渲染过"
 *      （见 `components/__tests__/qy-section-page-layout.test.tsx`），
 *      而"文件里出现了这个名字"根本挡不住它 —— 挂在 `</Tabs>` 后面
 *      （也就是上一轮那种"在底部添加"）同样能让名字出现。
 *   2. **取值不冲突**：qy 那一格的取值不能落进上游 `WALLET_TAB_VALUES`，
 *      否则路由 schema 的 `.catch(DEFAULT_WALLET_TAB)` 会把它改写回 `funds`。
 *
 * AST 走查（`readJsxTree`）而不是正则数尖括号：注释、字符串、条件渲染里都可能
 * 出现 `<Tabs`，正则版会在这些地方给出假绿。
 */
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { WALLET_TAB_VALUES } from '@/features/wallet/constants'

import { readJsxTree } from '../../__tests__/jsx-tree'
import type { QyConfig } from '../../lib/types'
import { QY_WALLET_TRANSFER_TAB, qyWalletTransferVisible } from '../tab'

// __tests__ → wallet-entry → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)
const wallet = readJsxTree(join(srcDir, 'features', 'wallet', 'index.tsx'))
const walletSource = wallet.source
const occurrences = wallet.occurrences

describe('qy 的「余额划转」格挂在上游 Tabs 上（需求 5）', () => {
  test('触发器恰好一处，且在 <TabsList> 里面', () => {
    const spots = occurrences('QyWalletTransferTrigger')
    assert.equal(
      spots.length,
      1,
      '钱包页应当恰好渲染一次 QyWalletTransferTrigger'
    )
    assert.ok(
      spots[0]?.includes('TabsList'),
      `触发器挂在了 <TabsList> 外（祖先链：${spots[0]?.join(' > ')}）——` +
        ' Base UI 的 Tabs.Tab 靠 list 的 context 取索引，放外面会得到一个按不动的按钮'
    )
  })

  test('面板恰好一处，在 <Tabs> 里、<TabsList> 外', () => {
    const spots = occurrences('QyWalletTransferPanel')
    assert.equal(spots.length, 1)
    const chain = spots[0] ?? []
    assert.ok(
      chain.includes('Tabs'),
      `面板挂在了 <Tabs> 外（祖先链：${chain.join(' > ')}）—— 这正是上一轮"在底部添加"的形状`
    )
    assert.ok(
      !chain.includes('TabsList'),
      '面板挂进了 <TabsList>：那里只该放触发器'
    )
  })

  test('Tabs 之外不再挂 qy 的入口卡（钱包页瘦身）', () => {
    // 曾经这里有一张「增长与结算」入口卡（QyWalletEntryCard / QyWalletSections），
    // 把推广佣金与提现两个链接又摆了一遍。项目方要求钱包页瘦身，它整块删除；
    // 侧栏里那两行入口才是唯一入口。留一行 `<QyWalletSections />` 在这里的话，
    // 组件删了会直接编译不过，而只删组件、忘了删这一行正是"断链"的反面形状，
    // 所以这条从"在 Tabs 外"改成"一次都不出现"。
    assert.equal(occurrences('QyWalletSections').length, 0)
    assert.equal(occurrences('QyWalletEntryCard').length, 0)
  })

  test('Tabs 的选中值认得 qy 那一格', () => {
    // 光把触发器挂进去还不够：`value` 不认它的话，点了没反应。
    assert.ok(
      walletSource.includes('QY_WALLET_TRANSFER_TAB'),
      'wallet/index.tsx 没有引用 QY_WALLET_TRANSFER_TAB'
    )
    const [, valueExpr] =
      /<Tabs\s+value=\{([\s\S]*?)\}\s*\n/.exec(walletSource) ?? []
    assert.ok(valueExpr != null, '没找到 <Tabs value={…}>')
    assert.ok(
      valueExpr.includes('QY_WALLET_TRANSFER_TAB'),
      `<Tabs value> 没有考虑 qy 那一格：${valueExpr}`
    )
  })

  test('标签栏不再被「有没有订阅套餐」一票否决', () => {
    // 上游原本是 `{showSubscriptionPanel && (<TabsList …>)}`：站点没配套餐时
    // 整条标签栏不渲染，划转入口会跟着一起消失 —— 两件无关的事。
    assert.ok(
      !/\{showSubscriptionPanel && \(\s*<TabsList/.test(walletSource),
      '整条 TabsList 又被 showSubscriptionPanel 单独把住了'
    )
  })
})

/* ── 取值与可见性 ───────────────────────────────────────────────────── */

function config(overrides: {
  enabled?: boolean
  transfer?: boolean
  walletEntry?: boolean
}): QyConfig {
  return {
    enabled: overrides.enabled ?? true,
    available: true,
    features: {
      transfer: overrides.transfer ?? true,
      commission: true,
      withdraw: true,
      availability: true,
      lottery: true,
      violation: true,
    },
    wallet: {
      show_transfer_entry: overrides.walletEntry ?? true,
      show_commission_entry: true,
      show_withdraw_entry: true,
    },
  } as unknown as QyConfig
}

describe('「余额划转」格的取值与可见性', () => {
  test('取值不在上游 WALLET_TAB_VALUES 里（否则会被路由 schema 改写回 funds）', () => {
    assert.ok(
      !(WALLET_TAB_VALUES as readonly string[]).includes(
        QY_WALLET_TRANSFER_TAB
      ),
      'qy 的取值撞上了上游的 ?tab= 白名单'
    )
    // 反过来也要成立：上游那两格必须还在，否则说明有人把白名单改坏了。
    assert.deepEqual([...WALLET_TAB_VALUES], ['funds', 'plans'])
  })

  test('功能开关 × 钱包入口开关，任一为假就不出现', () => {
    assert.equal(qyWalletTransferVisible(config({})), true)
    assert.equal(qyWalletTransferVisible(config({ enabled: false })), false)
    assert.equal(qyWalletTransferVisible(config({ transfer: false })), false)
    assert.equal(qyWalletTransferVisible(config({ walletEntry: false })), false)
  })
})
