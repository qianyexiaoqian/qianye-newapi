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
 * 绑定的违规类型配成「不限期限」之后，规则抽屉里那三句话的时间口径。
 *
 * # 事故长什么样
 *
 * 统计窗口的「不限期限」是哨兵 `-1`，后端**原样下发**（刻意不折成小时数）。
 * 这一页有三处把 `window_hours` 直接插进带 `{{hours}} 小时内` 的文案：
 * 类型下拉的每一项、「命中记到哪」那条结论行、以及「几次命中到线」那条算式。
 * 三处都会渲染成
 *
 *     该类型 -1 小时内累计 5 次即触发处置
 *
 * 而**更糟的修法**是把 -1 折成 24 再显示：那不是乱码，是一句读起来完全正常的
 * 假话。这一页正是「这条规则触发了会记到哪、几次触发处置」的唯一答案页 ——
 * 印错口径等于让管理员照着一个不存在的配置去绑规则、去算那笔账。
 *
 * 同类失效在用户端已经被 `violations/__tests__/window-unlimited-sentence`
 * 钉住了，管理端这三处此前没有任何用例覆盖。
 *
 * # 这里钉住三件事
 *
 *  1. 有限窗口照旧说「24 小时内」（既有行为不能被改坏，所以每一档都配一个对照组）；
 *  2. 不限期限时三处各自换成不带时间口径的那一句；
 *  3. 换过之后，这两格里**一个「小时」字都不许出现** —— 这一条抓的是
 *     "折成 24 再显示" 那种修法，它能骗过前两条。
 *
 * 期望值一律从真实的 `src/i18n/qy/zh.json` 自己插值算出来，不抄字面量。
 * 变异验证见文件末尾。
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

/** 与后端 `violation.WindowUnlimited` 同值。 */
const UNLIMITED = -1

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

/** 一份配了线的类型清单，窗口由入参决定（24 = 有限，-1 = 不限期限）。 */
function categoryList(windowHours: number, threshold: number) {
  return {
    fallback_id: 1,
    threshold_semantics: 'any_line',
    items: [
      {
        rule_count: 10,
        threshold_state: 'active',
        category: {
          id: 2,
          key: 'jailbreak',
          name: '破限(越狱)',
          remark: '内部判据，绝不渲染到用户端',
          public_title: '绕过安全策略',
          public_desc: '',
          published: true,
          enabled: true,
          window_hours: windowHours,
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

async function mountSheet(opts: {
  windowHours: number
  threshold: number
  countWeight?: number
}) {
  // 上一棵树必须先卸干净：抽屉走 Portal，内容挂在 document.body 上而不是容器里。
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
    categoryList(opts.windowHours, opts.threshold)
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
          rule={{ ...baseRule, count_weight: opts.countWeight ?? 1 }}
          onSaved={() => {}}
        />
      </QueryClientProvider>
    )
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
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

/**
 * 那一格的**控件本体**。一律从标签的 `for` 反查 —— `<FormControl>` 走 Base UI
 * 的 `useRender`，会把 `data-slot="select-trigger"` 覆盖掉，按那个选择器全屏搜
 * 会搜到抽屉底部试跑面板里的下拉。
 */
function fieldControl(label: string): HTMLElement {
  const htmlFor = fieldItem(label)
    .querySelector('[data-slot="form-label"]')
    ?.getAttribute('for')
  assert.ok(htmlFor, `「${label}」这一格的标签没有 for`)
  const control = document.getElementById(htmlFor)
  assert.ok(control, `标签 for="${htmlFor}" 指向的控件不存在`)
  return control
}

/** 展开那一格的下拉。收起时 `<SelectContent>` 走 Portal 且不 keepMounted。 */
async function openSelect(label: string) {
  const control = fieldControl(label)
  await act(async () => {
    for (const type of [
      'pointerdown',
      'mousedown',
      'pointerup',
      'mouseup',
      'click',
    ]) {
      // happy-dom 的 Event 与 lib.dom 的 Event 不是同一个名义类型，转一次。
      control.dispatchEvent(
        new domWindow.Event(type, { bubbles: true }) as unknown as Event
      )
    }
  })
  await act(async () => {})
}

function fill(key: string, vars: Record<string, string>): string {
  let out = dict[key]
  assert.ok(out, `译文表里没有 ${key}`)
  for (const [name, value] of Object.entries(vars)) {
    out = out.replaceAll(`{{${name}}}`, value)
  }
  return out
}

const CATEGORY_LABEL = dict.qy_vio_field_category
const WEIGHT_LABEL = dict.qy_vio_field_count_weight

/* ── 1. 「命中记到哪」那条结论行 ───────────────────────────────────────── */

describe('命中记到哪：不限期限时换整句', () => {
  test('有限窗口照旧写「24 小时内累计」', async () => {
    await mountSheet({ windowHours: 24, threshold: 5 })
    const text = fieldItem(CATEGORY_LABEL).textContent ?? ''
    assert.ok(
      text.includes(
        fill('qy_vio_field_category_dest_active', {
          name: '破限(越狱)',
          count: '5',
          hours: '24',
        })
      ),
      `既有的有限窗口那一句被改坏了：${text.slice(0, 400)}`
    )
  })

  test('不限期限时换成不带时间口径的那一句', async () => {
    await mountSheet({ windowHours: UNLIMITED, threshold: 5 })
    const text = fieldItem(CATEGORY_LABEL).textContent ?? ''
    assert.ok(
      text.includes(
        fill('qy_vio_field_category_dest_active_unlimited', {
          name: '破限(越狱)',
          count: '5',
        })
      ),
      `不限期限没有换句：${text.slice(0, 400)}`
    )
  })

  test('这一格里一个「小时」字都不许出现', async () => {
    // 这一条抓的是"把 -1 折成 24 再显示"那种修法：它能骗过上面那条
    // （新句子确实在），却让管理员读到一个完全正常、但是假的时间口径。
    await mountSheet({ windowHours: UNLIMITED, threshold: 5 })
    const text = fieldItem(CATEGORY_LABEL).textContent ?? ''
    assert.ok(!text.includes('-1'), `渲染里漏出了哨兵：${text.slice(0, 400)}`)
    assert.ok(
      !text.includes('小时'),
      `不限期限的那一档不该出现任何小时数：${text.slice(0, 400)}`
    )
  })
})

/* ── 2. 「几次命中到线」那条算式 ───────────────────────────────────────── */

describe('几次命中到线：不限期限时换整句', () => {
  test('有限窗口照旧写「24 小时内 10」', async () => {
    await mountSheet({ windowHours: 24, threshold: 10, countWeight: 2 })
    const text = fieldItem(WEIGHT_LABEL).textContent ?? ''
    assert.ok(
      text.includes(
        fill('qy_vio_field_count_weight_math_active', {
          name: '破限(越狱)',
          weight: '2',
          threshold: '10',
          hours: '24',
          hits: '5',
        })
      ),
      `既有的算式被改坏了：${text.slice(0, 500)}`
    )
  })

  test('不限期限时换句，且那笔账本身不变', async () => {
    // 换的是时间口径，不是算术：5 次到线这个结论与窗口无关，
    // 顺手把它一起改掉会让"到底几次封号"这个问题再错一次。
    await mountSheet({ windowHours: UNLIMITED, threshold: 10, countWeight: 2 })
    const text = fieldItem(WEIGHT_LABEL).textContent ?? ''
    assert.ok(
      text.includes(
        fill('qy_vio_field_count_weight_math_active_unlimited', {
          name: '破限(越狱)',
          weight: '2',
          threshold: '10',
          hits: '5',
        })
      ),
      `不限期限没有换句：${text.slice(0, 500)}`
    )
  })

  test('这一格里一个「小时」字都不许出现', async () => {
    await mountSheet({ windowHours: UNLIMITED, threshold: 10, countWeight: 2 })
    const text = fieldItem(WEIGHT_LABEL).textContent ?? ''
    assert.ok(!text.includes('-1'), `渲染里漏出了哨兵：${text.slice(0, 500)}`)
    assert.ok(
      !text.includes('小时'),
      `不限期限的那一档不该出现任何小时数：${text.slice(0, 500)}`
    )
  })
})

/* ── 3. 类型下拉里的每一项 ─────────────────────────────────────────────── */

describe('类型下拉里的阈值摘要', () => {
  test('有限窗口照旧写「24 小时内 5 次」', async () => {
    await mountSheet({ windowHours: 24, threshold: 5 })
    await openSelect(CATEGORY_LABEL)
    const options = [
      ...document.querySelectorAll('[data-slot="select-item"]'),
    ].map((node) => (node.textContent ?? '').trim())
    const hit = options.find((text) => text.startsWith('破限(越狱)'))
    assert.ok(hit, `下拉里没有那一类：${JSON.stringify(options)}`)
    assert.ok(
      hit.includes(
        fill('qy_vcat_threshold_value', { count: '5', hours: '24' })
      ),
      `下拉项的阈值摘要被改坏了：${hit}`
    )
  })

  test('不限期限时下拉项也换句，且不漏出 -1', async () => {
    await mountSheet({ windowHours: UNLIMITED, threshold: 5 })
    await openSelect(CATEGORY_LABEL)
    const options = [
      ...document.querySelectorAll('[data-slot="select-item"]'),
    ].map((node) => (node.textContent ?? '').trim())
    const hit = options.find((text) => text.startsWith('破限(越狱)'))
    assert.ok(hit, `下拉里没有那一类：${JSON.stringify(options)}`)
    assert.ok(
      hit.includes(fill('qy_vcat_threshold_value_unlimited', { count: '5' })),
      `下拉项没有换句：${hit}`
    )
    assert.ok(!hit.includes('-1'), `下拉项漏出了哨兵：${hit}`)
    assert.ok(!hit.includes('小时'), `下拉项不该出现小时数：${hit}`)
  })
})

/* ── 4. 两句新文案自己得站得住 ─────────────────────────────────────────── */

describe('新增的两句文案', () => {
  test('两侧语言都在，且都不含 {{hours}}', async () => {
    // 留着 `{{hours}}` 的话 i18next 会把它原样吐到界面上（没有传值时），
    // 或者更糟 —— 传了 -1 就又回到事故本身。
    for (const key of [
      'qy_vio_field_category_dest_active_unlimited',
      'qy_vio_field_count_weight_math_active_unlimited',
    ]) {
      const zhText = dict[key]
      assert.ok(zhText, `zh.json 里没有 ${key}`)
      assert.ok(!zhText.includes('{{hours}}'), `${key} 的中文里还留着 {{hours}}`)
      assert.ok(!zhText.includes('小时'), `${key} 的中文里还写着小时`)
    }
  })

  test('换句之后与原句确实不同，不是复制粘贴', async () => {
    assert.notEqual(
      dict.qy_vio_field_category_dest_active,
      dict.qy_vio_field_category_dest_active_unlimited
    )
    assert.notEqual(
      dict.qy_vio_field_count_weight_math_active,
      dict.qy_vio_field_count_weight_math_active_unlimited
    )
  })
})

/*
 * ── 变异验证（逐条改坏产品代码再跑本文件，改完即还原；括号里是实测结果）──
 *
 * `rule-form-sheet.tsx`：
 *  M1  「命中记到哪」永远走 `qy_vio_field_category_dest_active`
 *      （即事故本身）                          → 红 2 条，失败消息里印出
 *                                                「该类型 -1 小时内累计 5 次即触发处置」
 *  M2  「几次命中到线」永远走有限窗口那一句     → 红 2 条
 *  M3  下拉项写死 `qy_vcat_threshold_value`     → 红 2 条（下拉那条，外加类型那一格
 *                                                的「不许出现小时」—— 收起态的选中值
 *                                                就渲染在那一格里）
 *  M4  三处都退回有限那一句，并把哨兵折成 24
 *      （读起来完全正常的那种假话）             → 红 5 条，失败消息里印出
 *                                                「该类型 24 小时内累计 5 次即触发处置」
 *                                                —— 一个永不清零的类型被写成一天一清
 *  M5  `qyWindowIsUnlimited` 改成 `hours <= 0`  → 本文件全绿（哨兵仍是 -1），
 *                                                由 `lib/__tests__/violation-window`
 *                                                的取值语义表守住
 */
