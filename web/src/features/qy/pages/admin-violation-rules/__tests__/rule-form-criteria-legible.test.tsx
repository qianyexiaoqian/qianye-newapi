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
 * 规则编辑抽屉**第一屏**就要能读出「这条规则现在是什么阶段、什么匹配方式」。
 *
 * # 这里守的是什么
 *
 * 上一轮把试跑面板做对了：判据条写着「当前判据：生效阶段「转发前」 × 匹配方式
 * 「正则」」，缺席的维度也逐条解释了。但那一块在 850 行表单的**最底部**，
 * 而抽屉一打开，最上面那三个下拉写的是：
 *
 *     违规类型  2
 *     生效阶段  prompt
 *     匹配方式  regex
 *
 * 也就是说同一张表单上同一个字段有两种写法，先看到的那种是英文与裸主键。
 * 项目方说「看不出这条规则是什么阶段」，看的就是这一屏。
 *
 * 根因不在这一页：`@/components/ui/select` 的 `<SelectValue/>` 把解析交给
 * Base UI，而 Base UI 只从 Root 的 `items` 里查译名，查不到就渲染枚举原始取值；
 * `<SelectContent>` 走 Portal 且关闭时不 keepMounted，`<SelectItem>` 的中文
 * 标签从未注册过。修复落在共享组件里（自动从 `<SelectItem>` 子树推出 `items`），
 * 所以这里既钉住**这一页的最终屏幕文字**，也钉住**共享组件的那条契约**。
 *
 * # 为什么挂真组件而不是照抄形状
 *
 * 「把 rule-form-sheet 的三个 Select 的形状搬过来再断言」测的是抄本，不是产品：
 * 有人给这一页的 `<SelectValue/>` 加上 `children` 或换掉组件，抄本照样绿。
 * 这里挂的是真的 `QyRuleFormSheet`，文案走真实的 `src/i18n/qy/zh.json`，
 * 违规类型清单用真实的 query key 预置进 QueryClient —— 断言的就是管理员
 * 打开抽屉那一刻屏幕上的字。
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
const { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } =
  await import('@/components/ui/select')
const { qyKeys } = await import('../../../lib/query-keys')

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

async function mountTree(node: React.ReactNode, seed?: (qc: never) => void) {
  // 每次挂载前先把上一棵树卸干净。
  //
  // 下面的断言按 `document` 全局找（抽屉走 Portal，内容根本不在容器里），
  // 所以上一棵树留在文档里就会污染下一条用例 —— 而污染的方式很隐蔽：
  // 上一棵树里的 useQuery 在用例结束**之后**才落到 error 态，违规类型清单被清空，
  // 那个下拉于是回落到裸主键 "2"，正好被下一条用例的反向断言抓到。
  // 症状表现为"单跑绿、合跑红"，最容易被当成 flaky 而不是被当成隔离缺陷。
  for (const entry of roots.splice(0)) {
    entry.root.unmount()
    entry.container.remove()
  }
  await act(async () => {})

  // `refetchOnMount: false` 是必须的，不是调优。
  //
  // 违规类型清单的 queryOptions 带 `staleTime: 0`，所以预置的数据一挂载就会被
  // 判为陈旧并触发一次真实 fetch；这里没有网络，那次请求会失败。失败之后这一格
  // 回落到裸主键 "2" —— 于是这条用例在单独跑时绿、在整包跑时红（前面的文件先
  // 让事件循环转了几圈，那次 refetch 正好赶在断言之前落地）。
  // 这条用例问的是"屏幕上写的是什么"，不是"拉取怎么处理"，所以把网络整个拿掉。
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnMount: false } },
  })
  seed?.(queryClient as never)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>{node}</QueryClientProvider>
    )
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
}

/**
 * 抽屉走 Portal，内容挂在 `document.body` 而不是容器里，所以一律全局找。
 * 收起态的下拉在触发器里渲染 `<span data-slot="select-value">`，那一格的文字
 * 就是管理员看到的字。
 */
function triggerTexts(): string[] {
  return [...document.querySelectorAll('[data-slot="select-value"]')].map(
    (node) => (node.textContent ?? '').trim()
  )
}

/**
 * 按**表单字段的标签**定位那一格显示的字。
 *
 * 不能只在整屏的下拉里 `includes(译名)`：抽屉底部的试跑面板自己也有一对
 * 生效阶段/匹配方式下拉（那两个显式传了 `items`），全屏搜索会被它们满足，
 * 于是表单顶部那两格漏出 `prompt` / `regex` 时测试照样绿 —— 而顶部那两格
 * 正是项目方看到的第一屏。所以必须锚在 `<FormItem>` 上。
 */
function fieldValueText(label: string): string | null {
  for (const item of document.querySelectorAll('[data-slot="form-item"]')) {
    const labelNode = item.querySelector('[data-slot="form-label"]')
    if ((labelNode?.textContent ?? '').trim() !== label) continue
    const value = item.querySelector('[data-slot="select-value"]')
    if (value == null) continue
    return (value.textContent ?? '').trim()
  }
  return null
}

const sentinelRule: QyViolationRule = {
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
  group_scope_mode: 'all',
  group_scope: '',
  model_scope: '',
  status_scope: '',
  window_seconds: 0,
  rate_threshold: 0,
  ai_categories: '',
  ai_min_confidence: '0',
  created_at: 0,
  updated_at: 0,
} as unknown as QyViolationRule

/* ── 1. 抽屉第一屏的三个下拉，写的是人话 ─────────────────────────────── */

describe('规则编辑抽屉第一屏能读出判据', () => {
  test('生效阶段/匹配方式/违规类型三格显示译名，不是 prompt / regex / 2', async () => {
    await mountTree(
      <QyRuleFormSheet
        open
        onOpenChange={() => {}}
        rule={sentinelRule}
        onSaved={() => {}}
      />,
      (queryClient) => {
        // 违规类型清单用真实 query key 预置：抽屉靠它把 category_id=2 翻成中文名。
        // 拉不到时下拉里根本没有这一项，届时显示裸主键是**正确**行为
        // （译名不存在就不能编一个），所以这里必须把清单给足。
        ;(
          queryClient as unknown as {
            setQueryData: (key: unknown, data: unknown) => void
          }
        ).setQueryData(qyKeys.adminViolationCategories(), {
          fallback_id: 1,
          threshold_semantics: 'any_line',
          items: [
            {
              rule_count: 10,
              threshold_state: 'unset',
              category: {
                id: 2,
                key: 'jailbreak',
                name: '破限(越狱)',
                remark: '内部判据，绝不渲染到用户端',
                public_title: '绕过安全策略',
                public_desc: '',
                published: true,
                enabled: false,
                window_hours: 24,
                threshold: 0,
                sort_order: 10,
                is_fallback: false,
                created_at: 0,
                updated_at: 0,
              },
            },
          ],
        })
      }
    )

    const dict = zh as Record<string, string>
    // 期望值独立算出：直接从真实 zh.json 取，不从产品代码回读。
    assert.equal(dict['qy_vio_phase_prompt'], '转发前')
    assert.equal(dict['qy_vio_match_regex'], '正则')

    const cases: {
      field: string
      want: string
      leaked: string
      exact?: boolean
    }[] = [
      {
        field: dict['qy_vio_field_phase'] ?? '',
        want: '转发前',
        leaked: 'prompt',
      },
      {
        field: dict['qy_vio_field_match_type'] ?? '',
        want: '正则',
        leaked: 'regex',
      },
      {
        // 违规类型那一项的文案由**两段**拼成（类型名 + 「 · 不单独触发」）。
        // 这里要的是逐字相等而不是前缀相等：把 `<SelectItem>` 的子树折成文本时
        // 若图省事写 `String(children)`，多段子节点会被 JS 用逗号连起来
        // （"破限(越狱), · ,不单独触发"），前缀断言完全看不出来。
        field: dict['qy_vio_field_category'] ?? '',
        want: `破限(越狱) · ${dict['qy_vcat_threshold_off']}`,
        leaked: '2',
        exact: true,
      },
    ]

    for (const item of cases) {
      assert.notEqual(item.field, '', '字段标签的 i18n 键取不到，测试锚点已失效')
      const shown = fieldValueText(item.field)
      assert.notEqual(
        shown,
        null,
        `「${item.field}」这一格在抽屉里找不到，全屏下拉：${JSON.stringify(triggerTexts())}`
      )
      // 反向先断：漏出枚举原始取值 / 裸主键正是项目方截图里看到的字。
      assert.notEqual(
        shown,
        item.leaked,
        `「${item.field}」那一格漏出了原始取值「${item.leaked}」`
      )
      if (item.exact === true) {
        assert.equal(
          shown,
          item.want,
          `「${item.field}」那一格应当逐字写「${item.want}」，实际写的是「${shown}」`
        )
      } else {
        assert.ok(
          (shown ?? '').startsWith(item.want),
          `「${item.field}」那一格应当写「${item.want}」，实际写的是「${shown}」`
        )
      }
    }
  })
})

/* ── 2. 共享 Select 的那条契约 ───────────────────────────────────────── */

describe('共享 Select 收起时显示的是选项文案', () => {
  test('调用方不传 items 也能显示译名', async () => {
    await mountTree(
      <Select value='prompt'>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value='prompt'>转发前</SelectItem>
          <SelectItem value='upstream_err'>上游报错后</SelectItem>
        </SelectContent>
      </Select>
    )
    assert.ok(
      triggerTexts().includes('转发前'),
      `没有推出译名，实际：${JSON.stringify(triggerTexts())}`
    )
  })

  test('数值主键同样翻成名字，而不是裸数字', async () => {
    await mountTree(
      <Select value={String(2)}>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value='0'>不指定</SelectItem>
          <SelectItem value='2'>破限(越狱)</SelectItem>
        </SelectContent>
      </Select>
    )
    const texts = triggerTexts()
    assert.ok(
      texts.includes('破限(越狱)'),
      `没有推出译名，实际：${JSON.stringify(texts)}`
    )
    assert.ok(!texts.includes('2'), `仍然漏出裸主键，实际：${JSON.stringify(texts)}`)
  })

  test('调用方显式给的 items 优先，推导不许顶掉它', async () => {
    await mountTree(
      <Select value='prompt' items={{ prompt: '调用方说了算' }}>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value='prompt'>子树里的文案</SelectItem>
        </SelectContent>
      </Select>
    )
    const texts = triggerTexts()
    assert.ok(
      texts.includes('调用方说了算'),
      `显式 items 被推导顶掉了，实际：${JSON.stringify(texts)}`
    )
  })

  test('placeholder 仍然照常显示 —— 推导不许吞掉它', async () => {
    // Base UI 的 hasNullItemLabel:映射里出现 "null" 这个键时会**吞掉**
    // placeholder。推导必须跳过 value 为空的项，否则这是一次与本修复
    // 无关的行为改变，而它只在"未选中"的表单上才看得见。
    await mountTree(
      <Select>
        <SelectTrigger>
          <SelectValue placeholder='请选择' />
        </SelectTrigger>
        <SelectContent>
          {/* 取值为 null 的「不限」项：Base UI 的 hasNullItemLabel 查的就是
              映射里有没有 "null" 这个键，所以必须真的摆一个出来，
              否则这条断言测不到那个分支。 */}
          <SelectItem value={null}>不限</SelectItem>
          <SelectItem value='a'>甲</SelectItem>
        </SelectContent>
      </Select>
    )
    assert.ok(
      triggerTexts().includes('请选择'),
      `placeholder 被吞掉了，实际：${JSON.stringify(triggerTexts())}`
    )
  })
})

/*
 * ── 变异实验记录 ────────────────────────────────────────────────────
 * 见 PR 说明。每一条都跑过一次「改坏 → 必须变红 → 还原 → 必须变绿」。
 */
