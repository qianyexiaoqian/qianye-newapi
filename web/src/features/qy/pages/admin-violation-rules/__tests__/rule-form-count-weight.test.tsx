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
 * 「规则已经绑了违规类型、类型也带阈值了，计数权重还留着做什么」。
 *
 * # 这里守的是什么
 *
 * 答案是：它与阈值不是同一件事，而是**乘数**。命中一次给所选类型加
 * `count_weight`，加到该类型的 `threshold` 才触发处置。所以这一格必须把算式
 * 直接写在屏幕上 —— 「约 N 次命中到线（N × 权重 ≥ 阈值）」，其中
 * `N = ceil(threshold / weight)`。
 *
 * 三档口径各有一条用例：
 *   - 权重 > 0 且类型配了阈值 → 写出具体的 N，中性样式；
 *   - 权重 > 0 但类型没配阈值 → 类型线不会触发，告警样式；
 *   - 权重 = 0               → 一条线都不推进，这条规则永远不会封人，告警样式。
 *
 * 不整除那一档（权重 2 配阈值 3 → 2 次而不是 1 次）是这张表的重点：向下取整会
 * 让界面告诉运营「1 次就封」，而真实是第 2 次。那是一句关于什么时候封人的谎，
 * 而它不会有任何报错。后端同一条判据见
 * `qianye/modules/violation/count_weight_test.go`。
 *
 * 另一半是 severity 的退场：它从头到尾只写不读，这一轮连同表单、API 字段与
 * 内置种子一起撤掉（数据库列保留）。表单上不能再出现它 —— 摆在计数权重旁边
 * 只会让人以为「级别越高越容易被封」。
 *
 * # 为什么挂真组件
 *
 * 与 rule-form-category-binding.test.tsx 同一个理由：抄一份表单形状再断言，
 * 测的是抄本。这里挂真的 `QyRuleFormSheet`，文案走真实的 `src/i18n/qy/zh.json`。
 *
 * 变异实验见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

import type { QyViolationRule } from '../types'

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
const { QueryClient, QueryClientProvider } = await import(
  '@tanstack/react-query'
)

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zh as Record<string, string> } },
})

const { QyRuleFormSheet } = await import('../components/rule-form-sheet')
const { qyKeys } = await import('../../../lib/query-keys')

const dict = zh as Record<string, string>

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

/** 类型清单。`threshold` 为 0 表示这一类还没配线（现网六类都是这一档）。 */
function categoryList(threshold: number) {
  return {
    fallback_id: 1,
    threshold_semantics: 'any_line',
    items: [
      {
        rule_count: 10,
        threshold_state: threshold > 0 ? 'active' : 'unset',
        category: {
          id: 2,
          key: 'jailbreak',
          name: '破限(越狱)',
          remark: '内部判据，绝不渲染到用户端',
          public_title: '绕过安全策略',
          public_desc: '',
          published: true,
          enabled: threshold > 0,
          window_hours: 24,
          threshold,
          sort_order: 10,
          is_fallback: false,
          created_at: 0,
          updated_at: 0,
        },
      },
      {
        rule_count: 4,
        threshold_state: 'unset',
        category: {
          id: 1,
          key: 'uncategorized',
          name: '未分类',
          remark: '',
          public_title: '',
          public_desc: '',
          published: false,
          enabled: false,
          window_hours: 24,
          threshold: 0,
          sort_order: 999,
          is_fallback: true,
          created_at: 0,
          updated_at: 0,
        },
      },
    ],
  }
}

const baseRule = {
  id: 46,
  name: '破限-伪造开发者/调试模式',
  remark: '',
  category_id: 2,
  enabled: true,
  mode: 'enforce',
  priority: 100,
  phase: 'prompt',
  match_type: 'regex',
  pattern: '(?i)developer\\s*mode',
  action: 'block_and_charge',
  fee_mode: 'model_price_multiple',
  fee_fixed: '0',
  fee_multiple: '10',
  fee_max_quota: 0,
  public_reason: '内容不合规',
  block_message: '',
  group_scope_mode: 'include',
  group_scope: '',
  model_scope: '',
  status_scope: '',
  count_weight: 1,
  archive_context: false,
  ai_min_confidence: '0',
  created_at: 0,
  updated_at: 0,
} as unknown as QyViolationRule

async function mountSheet(countWeight: number, threshold: number) {
  // 上一棵树必须先卸干净：抽屉走 Portal，内容挂在 document.body 上。
  for (const entry of roots.splice(0)) {
    entry.root.unmount()
    entry.container.remove()
  }
  await act(async () => {})

  // `refetchOnMount: false` 是必须的，不是调优：类型清单的 queryOptions 带
  // `staleTime: 0`，预置的数据一挂载就会被判为陈旧并触发一次真实 fetch，
  // 而这里没有网络。失败会把清单清空 —— 表现为"单跑绿、合跑红"。
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnMount: false } },
  })
  queryClient.setQueryData(
    qyKeys.adminViolationCategories(),
    categoryList(threshold)
  )
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <QyRuleFormSheet
          open
          onOpenChange={() => {}}
          rule={{ ...baseRule, count_weight: countWeight }}
          onSaved={() => {}}
        />
      </QueryClientProvider>
    )
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
}

/** 规则表单自己的字段，按屏幕上的先后顺序。抽屉底部的试跑面板不算在内。 */
function formFieldLabels(): string[] {
  const form = document.querySelector('#qy-violation-rule-form')
  assert.ok(form, '规则表单没有渲染')
  return [...form.querySelectorAll('[data-slot="form-item"]')]
    .map((item) =>
      (
        item.querySelector('[data-slot="form-label"]')?.textContent ?? ''
      ).trim()
    )
    .filter((label) => label !== '')
}

/** 按标签定位那一格的 `<FormItem>`。 */
function fieldItem(label: string): Element {
  const form = document.querySelector('#qy-violation-rule-form')
  assert.ok(form, '规则表单没有渲染')
  for (const item of form.querySelectorAll('[data-slot="form-item"]')) {
    const text = (
      item.querySelector('[data-slot="form-label"]')?.textContent ?? ''
    ).trim()
    if (text === label) return item
  }
  assert.fail(`表单上找不到「${label}」这一格`)
}

const WEIGHT_LABEL = dict.qy_vio_field_count_weight

/** 那一格里的结论行（算式那一句），连同它的样式。 */
function calloutOf(label: string) {
  const nodes = [...fieldItem(label).querySelectorAll('p')]
  assert.ok(nodes.length > 0, `「${label}」这一格没有结论行`)
  return nodes[0]
}

/* ── 1. 权重与阈值的关系写在屏幕上 ─────────────────────────────────────── */

describe('计数权重与类型阈值的关系', () => {
  const cases: {
    name: string
    weight: number
    threshold: number
    hits: number
  }[] = [
    { name: '权重 1 配阈值 3', weight: 1, threshold: 3, hits: 3 },
    { name: '权重 1 配阈值 10', weight: 1, threshold: 10, hits: 10 },
    { name: '权重 2 配阈值 10：一半次数', weight: 2, threshold: 10, hits: 5 },
    // 下面三条是不整除档：向下取整会分别写成 1 / 3 / 1，全部比真实早一次。
    { name: '权重 2 配阈值 3：向上取整', weight: 2, threshold: 3, hits: 2 },
    { name: '权重 3 配阈值 10：向上取整', weight: 3, threshold: 10, hits: 4 },
    { name: '权重 7 配阈值 10：向上取整', weight: 7, threshold: 10, hits: 2 },
    { name: '权重大于阈值：一次到线', weight: 5, threshold: 3, hits: 1 },
  ]

  for (const tc of cases) {
    test(`${tc.name} → 写出「约 ${tc.hits} 次到线」`, async () => {
      await mountSheet(tc.weight, tc.threshold)
      const expected = dict.qy_vio_field_count_weight_math_active
        .replaceAll('{{name}}', '破限(越狱)')
        .replaceAll('{{weight}}', String(tc.weight))
        .replaceAll('{{threshold}}', String(tc.threshold))
        .replaceAll('{{hours}}', '24')
        .replaceAll('{{hits}}', String(tc.hits))
      const text = fieldItem(WEIGHT_LABEL).textContent ?? ''
      assert.ok(
        text.includes(expected),
        `算式对不上。期望包含：${expected}\n实际：${text.slice(0, 500)}`
      )
    })
  }

  test('能触发处置的那一档用中性样式，不喊狼来了', async () => {
    await mountSheet(2, 10)
    const cls = calloutOf(WEIGHT_LABEL).getAttribute('class') ?? ''
    assert.ok(
      !cls.includes('text-warning'),
      `一条配置正确的规则不该用告警样式：${cls}`
    )
  })
})

/* ── 2. 两档「配了也不会封人」 ─────────────────────────────────────────── */

describe('推进不了任何线的那两档', () => {
  test('权重 0：只按处置动作办，一条线都不推进', async () => {
    await mountSheet(0, 10)
    const item = fieldItem(WEIGHT_LABEL)
    assert.ok(
      (item.textContent ?? '').includes(
        dict.qy_vio_field_count_weight_math_zero
      ),
      `权重 0 缺少结论：${(item.textContent ?? '').slice(0, 500)}`
    )
    // 这一档必须显眼：它的表现是"规则配好了、也命中了、就是不封人"，
    // 而那正是运营最容易误判的一格。
    assert.ok(
      (calloutOf(WEIGHT_LABEL).getAttribute('class') ?? '').includes(
        'text-warning'
      ),
      '权重 0 的结论没有用告警样式'
    )
  })

  test('类型没配阈值：类型线不会触发', async () => {
    await mountSheet(2, 0)
    const expected = dict.qy_vio_field_count_weight_math_idle
      .replaceAll('{{name}}', '破限(越狱)')
      .replaceAll('{{weight}}', '2')
    const item = fieldItem(WEIGHT_LABEL)
    assert.ok(
      (item.textContent ?? '').includes(expected),
      `缺少未配阈值结论：${(item.textContent ?? '').slice(0, 500)}`
    )
    assert.ok(
      (calloutOf(WEIGHT_LABEL).getAttribute('class') ?? '').includes(
        'text-warning'
      ),
      '未配阈值的结论没有用告警样式'
    )
  })
})

/* ── 3. severity 退场 ─────────────────────────────────────────────────── */

describe('严重级别已经从表单上撤掉', () => {
  test('表单上不再有这一格', async () => {
    await mountSheet(1, 3)
    const labels = formFieldLabels()
    assert.ok(
      !labels.includes('严重级别'),
      `表单上仍然有「严重级别」：${labels.join(' / ')}`
    )
    // 计数权重必须还在 —— 两个字段一起删掉是另一种错，而且是更贵的那种。
    assert.ok(
      labels.includes(WEIGHT_LABEL),
      `表单上找不到「${WEIGHT_LABEL}」：${labels.join(' / ')}`
    )
  })

  test('译文表里也没有它的键，不留一个查不到落点的死键', async () => {
    assert.equal(dict.qy_vio_field_severity, undefined)
  })
})

/*
 * ── 变异验证（逐条改坏产品代码再跑本文件，改完即还原；括号里是实测数字）──
 *
 * 基线：12 pass / 0 fail。
 *
 * `rule-form-sheet.tsx`：
 *  M1  `Math.ceil(threshold / weight)` 改成 `Math.floor`
 *                              → 8 pass / 4 fail。红的正好是四条不整除档
 *                                （2/3、10/3、10/7、3/5），整除档一条不红 ——
 *                                这正是这张表要区分的那一半：向下取整会让界面
 *                                比真实提前一次说"到线"。
 *  M4  权重 0 的分支判据 `countWeight <= 0` 改成 `< 0`
 *                              → 11 pass / 1 fail（「权重 0」）
 *  M6  两个告警档的样式换成 `text-muted-foreground`
 *                              → 10 pass / 2 fail（「权重 0」「类型没配阈值」），
 *                                文本断言仍绿 —— 两条各守各的：写没写、显不显眼
 *  M7  中性档也改成 `text-warning`
 *                              → 11 pass / 1 fail（「不喊狼来了」）
 *  M8  把「严重级别」那一格加回表单
 *                              → 11 pass / 1 fail（「表单上不再有这一格」）
 *
 * 后端同一条判据的变异见 `qianye/modules/violation/count_weight_test.go` 末尾。
 */
