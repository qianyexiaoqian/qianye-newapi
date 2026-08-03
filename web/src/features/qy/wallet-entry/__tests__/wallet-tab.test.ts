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
 * 用 `oxc-parser` 而不是正则数尖括号：注释、字符串、条件渲染里都可能出现
 * `<Tabs`，正则版会在这些地方给出假绿。它不可用时本测试会直接抛错
 * （import 失败），不会静默跳过。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { parseSync } from 'oxc-parser'

import { WALLET_TAB_VALUES } from '@/features/wallet/constants'

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
const walletIndexPath = join(srcDir, 'features', 'wallet', 'index.tsx')
const walletSource = readFileSync(walletIndexPath, 'utf8')

/* ── JSX 祖先链 ─────────────────────────────────────────────────────── */

type JsxNode = Record<string, unknown>

function jsxName(node: JsxNode): string | null {
  const opening = node.openingElement as JsxNode | undefined
  const name = opening?.name as JsxNode | undefined
  if (name == null) return null
  if (name.type === 'JSXIdentifier') return name.name as string
  // <Foo.Bar /> —— 本测试只关心裸标识符，成员表达式一律记成完整写法。
  if (name.type === 'JSXMemberExpression') {
    const object = name.object as JsxNode
    const property = name.property as JsxNode
    return `${object.name as string}.${property.name as string}`
  }
  return null
}

/** 组件名 → 它每一次出现时的祖先 JSX 标签名列表（由外到内）。 */
function collectJsxAncestors(source: string): Map<string, string[][]> {
  const parsed = parseSync('index.tsx', source)
  assert.deepEqual(parsed.errors, [], '解析 wallet/index.tsx 失败')

  const found = new Map<string, string[][]>()
  const stack: string[] = []

  const visit = (node: unknown) => {
    if (node == null || typeof node !== 'object') return
    if (Array.isArray(node)) {
      for (const child of node) visit(child)
      return
    }
    const current = node as JsxNode
    const isElement = current.type === 'JSXElement'
    let name: string | null = null
    if (isElement) {
      name = jsxName(current)
      if (name != null) {
        const list = found.get(name) ?? []
        list.push([...stack])
        found.set(name, list)
        stack.push(name)
      }
    }
    for (const [key, value] of Object.entries(current)) {
      if (key === 'type' || key === 'start' || key === 'end') continue
      visit(value)
    }
    if (isElement && name != null) stack.pop()
    return
  }

  visit(parsed.program)
  return found
}

const jsx = collectJsxAncestors(walletSource)

function occurrences(name: string): string[][] {
  return jsx.get(name) ?? []
}

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

  test('入口卡仍在 Tabs 之外（它不属于任何一格）', () => {
    const spots = occurrences('QyWalletSections')
    assert.equal(spots.length, 1)
    assert.ok(
      !(spots[0] ?? []).includes('Tabs'),
      '入口卡被塞进了某一格：另外两格的用户就看不到它了'
    )
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
