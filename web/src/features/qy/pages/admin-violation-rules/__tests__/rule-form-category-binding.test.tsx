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
 * 「规则命中了、计次了，加到哪里去」这句话必须在**规则抽屉里**答得上。
 *
 * # 这里守的是什么
 *
 * 绑定违规类型的那一格上一轮就做出来了，项目方还是回了「怎么没有找到绑定违规
 * 规则类型的选项」。翻线上产物（`/static/js/async/*.js`）确认它渲染了，也确认
 * 它排在第三格 —— 所以问题不是"没做"，是"看不见 / 看不懂"。三条根因：
 *
 *   1. **它不像一个字段**。整张表单八个下拉，只有这一格的 `<SelectTrigger>`
 *      没带 `w-full`；共享组件的默认宽度是 `w-fit`，于是它在一整行的空白里
 *      缩成一枚一百来像素的小药丸，视觉上更像徽标。
 *   2. **点标签没反应**。`<FormControl>` 包的是 `<Select>`（Base UI Root，
 *      不渲染任何 DOM），`id` 落空，`<FormLabel htmlFor>` 指向一个不存在的
 *      元素。
 *   3. **同一个桶在下拉里出现两次**。合成的 `0`「未分类(兜底)」，加上清单里
 *      真实的那一行兜底类型。后端 `resolveRuleCategory` 保存时把 0 改写成兜底
 *      的真实 id，于是存之前显示前者、重开显示后者 —— 同一条规则的同一格
 *      会自己变一次。
 *
 * 以及项目方那句问话本身：不选类型时，规则命中只会记到一个阈值为 0 的桶，
 * **配了也不会封人**。那句话原来只躺在下方小字里，这里把它钉成一条独立的、
 * 按当前选择实时改写的结论行。
 *
 * # 为什么挂真组件
 *
 * 抄一份表单形状再断言，测的是抄本：有人把这一格挪走、换掉、条件渲染掉，抄本
 * 照样绿。这里挂真的 `QyRuleFormSheet`，文案走真实的 `src/i18n/qy/zh.json`，
 * 类型清单用真实的 query key 预置进 QueryClient —— 断言的就是管理员打开抽屉
 * 那一刻屏幕上的字与结构。
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
const { qyViolationRuleToForm, qyViolationRuleToPayload } = await import(
  '../lib/rule-form'
)

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

/**
 * 现网口径的类型清单：六类业务类型阈值全 0，兜底「未分类」也是 0。
 *
 * 刻意不把它做成"一切正常"的样板数据 —— 项目方看到的就是这一份，而这一份
 * 正是"配了规则也不会封人"的那一档。
 */
function categoryList(overrides?: {
  jailbreakActive?: boolean
  fallbackActive?: boolean
}) {
  const jailbreakActive = overrides?.jailbreakActive === true
  const fallbackActive = overrides?.fallbackActive === true
  return {
    fallback_id: 1,
    threshold_semantics: 'any_line',
    items: [
      {
        rule_count: 10,
        threshold_state: jailbreakActive ? 'active' : 'unset',
        category: {
          id: 2,
          key: 'jailbreak',
          name: '破限(越狱)',
          remark: '内部判据，绝不渲染到用户端',
          public_title: '绕过安全策略',
          public_desc: '',
          published: true,
          enabled: jailbreakActive,
          window_hours: 24,
          threshold: jailbreakActive ? 3 : 0,
          sort_order: 10,
          is_fallback: false,
          created_at: 0,
          updated_at: 0,
        },
      },
      {
        // 兜底排最后，与后端 `is_fallback asc, sort_order asc, id asc` 一致。
        rule_count: 4,
        threshold_state: fallbackActive ? 'active' : 'unset',
        category: {
          id: 1,
          key: 'uncategorized',
          name: '未分类',
          remark: '',
          public_title: '',
          public_desc: '',
          published: false,
          enabled: fallbackActive,
          window_hours: 24,
          threshold: fallbackActive ? 9 : 0,
          sort_order: 999,
          is_fallback: true,
          created_at: 0,
          updated_at: 0,
        },
      },
    ],
  }
}

async function mountSheet(
  rule: QyViolationRule | null,
  categories: unknown = categoryList()
) {
  // 上一棵树必须先卸干净：抽屉走 Portal，内容挂在 document.body 上而不是容器里，
  // 留着就会被下一条用例的全局查询捞到。
  for (const entry of roots.splice(0)) {
    entry.root.unmount()
    entry.container.remove()
  }
  await act(async () => {})

  // `refetchOnMount: false` 是必须的，不是调优：类型清单的 queryOptions 带
  // `staleTime: 0`，预置的数据一挂载就会被判为陈旧并触发一次真实 fetch，
  // 而这里没有网络。那次失败会把清单清空，于是这一格回落到占位文案 ——
  // 表现为"单跑绿、合跑红"。
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnMount: false } },
  })
  if (categories != null) {
    queryClient.setQueryData(qyKeys.adminViolationCategories(), categories)
  }
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <QyRuleFormSheet
          open
          onOpenChange={() => {}}
          rule={rule}
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

/**
 * 那一格的**控件本体**。
 *
 * 不能按 `[data-slot="select-trigger"]` 找：`<FormControl>` 走 Base UI 的
 * `useRender`，它把自己的 `data-slot="form-control"` 覆盖上去，于是表单里
 * 每一个被它包住的触发器都不再带 `select-trigger` 这个标记 —— 全屏按那个
 * 选择器搜，搜到的其实是抽屉底部试跑面板里那两个**没**进 `<FormControl>`
 * 的下拉。所以一律从标签的 `for` 反查，那也正是屏幕上点标签会落到的元素。
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

const CATEGORY_LABEL = dict.qy_vio_field_category

const editedRule: QyViolationRule = {
  id: 46,
  name: '破限-伪造开发者/调试模式',
  remark: '',
  category_id: 0,
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

/* ── 1. 两条路径都有这一格，而且在同一个位置 ───────────────────────────── */

describe('绑定违规类型这一格', () => {
  test('新增与编辑两条路径都渲染它，且都排在第三格', async () => {
    await mountSheet(null)
    const created = formFieldLabels()
    assert.equal(
      created.indexOf(CATEGORY_LABEL),
      2,
      `新增路径上「${CATEGORY_LABEL}」不在第三格：${created.join(' / ')}`
    )

    await mountSheet(editedRule)
    const edited = formFieldLabels()
    assert.equal(
      edited.indexOf(CATEGORY_LABEL),
      2,
      `编辑路径上「${CATEGORY_LABEL}」不在第三格：${edited.join(' / ')}`
    )
    // 两条路径必须是同一张表单，而不是"新增少几格"。
    assert.deepEqual(created, edited)
  })

  test('点标签能落到一个真实存在的控件上', async () => {
    // 这一格原来把 `<FormControl>` 包在 `<Select>`（不渲染 DOM 的 Root）外面，
    // `id` 整个落空 —— 标签指向一个不存在的元素，点四个字没有任何反应。
    // 这是"看不见"的一部分：一个点不动的标签读起来像说明文字，不像字段名。
    await mountSheet(null)
    const control = fieldControl(CATEGORY_LABEL)
    assert.ok(
      control.querySelector('[data-slot="select-value"]'),
      '标签指向的不是那个下拉'
    )
  })

  test('它的下拉宽度与表单里其它下拉一致', async () => {
    // 断言的是**跨字段的一致性**，不是某一个写死的 class：整张表单上其余七个
    // 下拉都是整行宽，只有这一格用了共享组件的默认 `w-fit`，于是它缩成一枚
    // 小药丸。留一条"某一格与其它格不一样"的裂缝，正是它被扫视跳过的原因。
    await mountSheet(null)
    const form = document.querySelector('#qy-violation-rule-form')
    assert.ok(form)
    const narrow: string[] = []
    for (const item of form.querySelectorAll('[data-slot="form-item"]')) {
      const label = (
        item.querySelector('[data-slot="form-label"]')?.textContent ?? ''
      ).trim()
      if (label === '') continue
      const control = fieldControl(label)
      if (control.querySelector('[data-slot="select-value"]') == null) continue
      if (!(control.getAttribute('class') ?? '').includes('w-full')) {
        narrow.push(label)
      }
    }
    assert.deepEqual(narrow, [], '这些下拉没有跟随表单宽度')
  })
})

/* ── 2. 「加到哪里」这句话，按当前选择实时给答案 ───────────────────────── */

describe('命中之后加到哪里', () => {
  test('没选类型时写明落到兜底桶、而且不会触发任何处置', async () => {
    await mountSheet(null)
    const text = fieldItem(CATEGORY_LABEL).textContent ?? ''
    const expected = dict.qy_vio_field_category_dest_fallback_idle.replace(
      '{{name}}',
      '未分类'
    )
    assert.ok(
      text.includes(expected),
      `没选类型时缺少兜底结论：${text.slice(0, 400)}`
    )
    // 这句话必须是显眼的一档，不能和普通说明文字混在一起 —— 否则等于把
    // "配了也不会封人"再藏一次。
    const callout = [...fieldItem(CATEGORY_LABEL).querySelectorAll('p')].find(
      (node) => (node.textContent ?? '').includes(expected)
    )
    assert.ok(
      (callout?.getAttribute('class') ?? '').includes('text-warning'),
      '兜底结论没有用告警样式'
    )
  })

  test('选中的类型配了阈值时，写出具体的次数与窗口', async () => {
    await mountSheet(
      { ...editedRule, category_id: 2 },
      categoryList({ jailbreakActive: true })
    )
    const text = fieldItem(CATEGORY_LABEL).textContent ?? ''
    const expected = dict.qy_vio_field_category_dest_active
      .replace('{{name}}', '破限(越狱)')
      .replace('{{count}}', '3')
      .replace('{{hours}}', '24')
    assert.ok(text.includes(expected), `缺少生效结论：${text.slice(0, 400)}`)
    assert.ok(
      !text.includes(dict.qy_vio_field_category_dest_idle.slice(0, 12)),
      '生效档不该同时挂着"没设阈值"的说法'
    )
  })

  test('选中的类型没配阈值时，说明只累计不处置', async () => {
    await mountSheet({ ...editedRule, category_id: 2 })
    const text = fieldItem(CATEGORY_LABEL).textContent ?? ''
    const expected = dict.qy_vio_field_category_dest_idle.replace(
      '{{name}}',
      '破限(越狱)'
    )
    assert.ok(text.includes(expected), `缺少未配阈值结论：${text.slice(0, 400)}`)
  })

  test('类型清单读不出来时不漏裸主键，并说明保存不受影响', async () => {
    // 清单拉取失败是常态（权限、网络、后端抖动）。这一档原来会让 Base UI
    // 查不到译名而把取值原样渲染成一个数字。
    await mountSheet({ ...editedRule, category_id: 7 }, null)
    const item = fieldItem(CATEGORY_LABEL)
    const value = item
      .querySelector('[data-slot="select-value"]')
      ?.textContent?.trim()
    assert.equal(value, dict.qy_vio_field_category_unset)
    assert.ok(
      (item.textContent ?? '').includes(dict.qy_vio_field_category_dest_unknown)
    )
  })

  test('指向已归档类型时写出「已归档 #id」，而不是一个裸数字', async () => {
    await mountSheet({ ...editedRule, category_id: 77 })
    const value = fieldItem(CATEGORY_LABEL)
      .querySelector('[data-slot="select-value"]')
      ?.textContent?.trim()
    assert.equal(
      value,
      dict.qy_vio_field_category_missing.replace('{{id}}', '77')
    )
  })
})

/* ── 3. 下拉里不能有两个"未分类" ───────────────────────────────────────── */

describe('兜底桶只出现一次', () => {
  test('展开的选项里不存在与兜底类型重复的合成项', async () => {
    await mountSheet(null)
    await openSelect(CATEGORY_LABEL)
    // 下拉走 Portal，展开后挂在 document 上而不是那一格里。
    const options = [
      ...document.querySelectorAll('[data-slot="select-item"]'),
    ].map((node) => (node.textContent ?? '').trim())
    // 清单里给了两类（破限 + 兜底），下拉就只能有两项。多出来的那一项正是
    // 老写法里的合成 `0`，它与兜底那一行在保存后指向同一个桶。
    assert.equal(options.length, 2, `下拉项数不对：${JSON.stringify(options)}`)
    assert.equal(
      options.filter((text) => text.startsWith('未分类')).length,
      1,
      `「未分类」在下拉里出现了不止一次：${JSON.stringify(options)}`
    )
  })

  test('换一个类型，结论行跟着改写', async () => {
    // 这一条是"表单状态真的跟着走"的那一半：上面三条按不同的初始值各挂一次，
    // 挂出来的都是首屏；这里是在**同一棵树上**换选择。
    await mountSheet(null, categoryList({ jailbreakActive: true }))
    assert.ok(
      (fieldItem(CATEGORY_LABEL).textContent ?? '').includes(
        dict.qy_vio_field_category_dest_fallback_idle.replace(
          '{{name}}',
          '未分类'
        )
      )
    )

    await openSelect(CATEGORY_LABEL)
    const option = [
      ...document.querySelectorAll('[data-slot="select-item"]'),
    ].find((node) => (node.textContent ?? '').startsWith('破限(越狱)'))
    assert.ok(option, '下拉里没有「破限(越狱)」')
    await act(async () => {
      for (const type of [
        'pointerdown',
        'mousedown',
        'pointerup',
        'mouseup',
        'click',
      ]) {
        option.dispatchEvent(
          new domWindow.Event(type, { bubbles: true }) as unknown as Event
        )
      }
    })
    await act(async () => {})

    const text = fieldItem(CATEGORY_LABEL).textContent ?? ''
    assert.ok(
      text.includes(
        dict.qy_vio_field_category_dest_active
          .replace('{{name}}', '破限(越狱)')
          .replace('{{count}}', '3')
          .replace('{{hours}}', '24')
      ),
      `换了类型之后结论行没有跟着改：${text.slice(0, 400)}`
    )
    assert.equal(
      fieldControl(CATEGORY_LABEL)
        .querySelector('[data-slot="select-value"]')
        ?.textContent?.trim()
        .startsWith('破限(越狱)'),
      true
    )
  })

  test('没选类型时收起态显示的就是兜底那一行，与保存后回读一致', async () => {
    // 后端 `resolveRuleCategory` 把 0 改写成兜底类型的真实 id，所以"存之前"
    // 与"存之后重开"必须是同一句话。
    await mountSheet(null)
    const before = fieldItem(CATEGORY_LABEL)
      .querySelector('[data-slot="select-value"]')
      ?.textContent?.trim()

    await mountSheet({ ...editedRule, category_id: 1 })
    const after = fieldItem(CATEGORY_LABEL)
      .querySelector('[data-slot="select-value"]')
      ?.textContent?.trim()

    assert.equal(before, after)
    assert.ok(before?.startsWith('未分类'), `收起态写的是：${before}`)
  })
})

/* ── 4. 表单 ↔ 载荷的往返 ─────────────────────────────────────────────── */

describe('类型归属的往返', () => {
  test('表单值原样进载荷，服务端回读原样进表单', async () => {
    for (const categoryId of [0, 2, 77]) {
      const form = qyViolationRuleToForm({
        ...editedRule,
        category_id: categoryId,
      })
      assert.equal(form.category_id, categoryId)
      assert.equal(qyViolationRuleToPayload(form).category_id, categoryId)
    }
  })

  test('后端缺字段的老规则回落到 0，而不是 NaN', async () => {
    const legacy = { ...editedRule } as Record<string, unknown>
    delete legacy.category_id
    const form = qyViolationRuleToForm(legacy as unknown as QyViolationRule)
    assert.equal(form.category_id, 0)
  })
})

/*
 * ── 变异验证（逐条改坏产品代码再跑本文件，改完即还原；括号里是实测结果）──
 *
 * `rule-form-sheet.tsx`：
 *  M1  整块删掉违规类型那一格                → 13 条里红 10 条
 *  M2  把那一格挪到 `pattern` 之后            → 「都排在第三格」红
 *      位置断言不是装饰：项目方找不到它，往下挪一屏就等于再丢一次。
 *  M3  `<SelectTrigger className='w-full'>` 改回 `<SelectTrigger>`
 *                                             → 「宽度与其它下拉一致」红
 *  M4  `<FormControl>` 挪回包在 `<Select>` 外面 → 红 4 条（含「点标签…」）
 *  M5  结论行的告警样式换成 `text-muted-foreground`
 *                                             → 「没选类型时…」红，文本断言仍绿
 *                                                （两条各守各的：写没写、显不显眼）
 *  M6  结论行三档合并成恒定输出                → 红 4 条
 *  M7  恢复合成的 `<SelectItem value='0'>`     → 「兜底桶只出现一次」红
 *  M8  `effectiveId` 改回 `field.value`        → 红 4 条（含存前/存后一致那条）
 *  M9  去掉清单为空时的占位 `<SelectItem>`     → 「读不出来时不漏裸主键」红
 *
 * `lib/rule-form.ts`：
 *  M10 `rule.category_id ?? 0` 去掉兜底        → 「老规则回落到 0」红
 */
