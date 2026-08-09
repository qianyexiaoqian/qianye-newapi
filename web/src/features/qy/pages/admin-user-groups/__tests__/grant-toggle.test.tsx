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
 * 编辑用户分组的弹窗里，每一行都必须有一个**真的能点的可选性控件**。
 *
 * # 守什么
 *
 * 项目方带截图报的原话：「编辑用户分组」弹窗上写着「已设定范围：只有下面勾选的
 * 模型分组可用」，顶部计数条写着「放开 0 · 撤销 0」，每一行有倍率框与备注框 ——
 * 但一行里没有任何可以勾选的东西。
 *
 * 复核下来接线是**通的**：`user-group-dialog` → `user-group-detail` →
 * `matrix-cell`，`onToggleGranted` 四层参数一路传到底。坏的是渲染那一端：那个
 * 控件当时是一个贴在格子最右端、12px、`text-muted-foreground/60` 的裸图标按钮，
 * 而且画的是**动作**（未放开画 ＋、已放开画 ✕）不是**状态** —— 于是"已放开"的
 * 行上显示一个 ✕，扫一眼读到的恰好与事实相反。控件在，没人认得出它是控件。
 *
 * 所以这条测试**不断言回调存在**：回调一直都在，那正是这个缺陷能活下来的原因。
 * 它断言的是三件在浏览器里看得见的事：
 *
 *   1. 每一行渲染出一个 `role=checkbox` 的控件，且 `aria-checked` 与该格此刻的
 *      放开状态一致（状态可读）；
 *   2. 点它真的会带着**目标状态**调用 `onToggleGranted`（可切换，且不是 toggle
 *      语义 —— 参数是"改成这个状态"，两者在整列批量那条路径上会分叉）；
 *   3. 未设定范围的那一档，勾选框全部禁用，并且屏幕上有一句**可见的**话说明
 *      为什么（禁用而不解释，正是项目方在删除按钮上投诉的同一个形状）。
 *
 * 变异实验：把 `<Checkbox …>` 从 `matrix-cell.tsx` 里删掉 → 1/2/3 全红；
 * 把它换回一个 `aria-label` 描述动作的裸按钮 → 1 红。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import type {
  QyGmCell,
  QyGmMatrixResponse,
  QyGmModelGroup,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'

const domWindow = new Window({ width: 1280, height: 900 })
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
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const

for (const key of domGlobals) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
/*
  近乎空的资源 ⇒ i18next 把键名原样渲染出来。下面的断言因此可以直接对键名比对，
  既不依赖文案措辞，又能在键被改错时变红。

  唯一登记的三条是「未设范围」那句指路：它带一个 `{{where}}` 插值，而**插值只在
  键真的有文案时才发生**（回落成键名时 i18next 原样吐键、不做插值）。这三条的
  值里仍然带着键名，所以既有的"键名出现在屏幕上"那两条断言照旧成立。
*/
await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'en',
  resources: {
    en: {
      translation: {
        qy_group_scope_edit: 'SCOPE_BUTTON',
        qy_ug_grant_needs_scope: 'qy_ug_grant_needs_scope[where={{where}}]',
        qy_ug_scope_section: 'SCOPE_SECTION',
      },
    },
  },
})

const i18nDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..',
  'i18n',
  'qy'
)

const { qyGmCellKey } = await import('../../admin-group-matrix/lib/draft')
const { QyUgGroupDetail } = await import('../components/user-group-detail')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/* ── 夹具 ──────────────────────────────────────────────────────────── */

function modelGroup(name: string): QyGmModelGroup {
  return {
    name,
    base_ratio: '1',
    has_channels: true,
    display_name: '',
    note: '',
    user_selectable: true,
    usable_description: '',
  }
}

function cell(modelGroupName: string, granted: boolean): QyGmCell {
  return {
    user_group: 'qy-canary-ug',
    model_group: modelGroupName,
    granted,
    ratio: null,
    source: 'inherit',
    inherited_from: '1',
    note: '',
    effective_note: modelGroupName,
    note_pending: false,
    note_source: 'group_name',
  }
}

function userGroup(scopeState: QyGmUserGroup['scope_state']): QyGmUserGroup {
  return {
    name: 'qy-canary-ug',
    display_name: '',
    note: '',
    enabled: true,
    sort_order: 0,
    registered: true,
    observed: true,
    topup_ratio: null,
    topup_ratio_effective: '1',
    model_groups: ['pool-a'],
    user_count: 12,
    active_token_count: 3,
    managed: scopeState !== 'unset',
    scope_state: scopeState,
    mode: 'enforce',
    scope_enforced: true,
    allow_auto: false,
    scope_note: '',
    self_excluded: false,
    self_inserted: false,
  }
}

/** 一档已设范围的分组：`pool-a` 已放开、`pool-b` 未放开。 */
const MODEL_GROUPS = [modelGroup('pool-a'), modelGroup('pool-b')]
const SERVER_CELLS = new Map<string, QyGmCell>([
  [qyGmCellKey('qy-canary-ug', 'pool-a'), cell('pool-a', true)],
  [qyGmCellKey('qy-canary-ug', 'pool-b'), cell('pool-b', false)],
])

function matrix(row: QyGmUserGroup): QyGmMatrixResponse {
  return {
    user_groups: [row],
    model_groups: MODEL_GROUPS,
    cells: [...SERVER_CELLS.values()],
    base_ratio_hash: 'h',
    snapshot: { loaded: true, age_seconds: 1, version: 1 },
    warnings: [],
    shadow_write_denies: [],
  }
}

/* ── 挂载 ──────────────────────────────────────────────────────────── */

const roots: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

async function mountDetail(
  row: QyGmUserGroup,
  onToggleGranted: (modelGroup: string, granted: boolean) => void,
  /**
   * 外壳判据。`undefined` = 编辑弹窗那个外壳（范围表单内嵌在同屏上一节），
   * 传函数 = `/qy` 的独立页（范围入口是详情面板右上角的一枚按钮）。
   */
  onEditScope?: () => void
) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QyUgGroupDetail
        data={matrix(row)}
        userGroup={row}
        serverCells={SERVER_CELLS}
        draft={new Map()}
        grantedCount={1}
        onToggleGranted={onToggleGranted}
        onRatioChange={() => {}}
        onNoteChange={() => {}}
        onEditScope={onEditScope}
        onCopyFrom={() => {}}
        scrollList={false}
      />
    )
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

function checkboxes(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll('[role="checkbox"]')] as HTMLElement[]
}

/* ── 断言 ──────────────────────────────────────────────────────────── */

describe('已设定范围的一档：每一行都有可切换的放开/撤销控件', () => {
  test('每个模型分组渲染一个勾选框，选中态与服务端状态一致', async () => {
    const container = await mountDetail(userGroup('set'), () => {})
    const boxes = checkboxes(container)

    assert.equal(
      boxes.length,
      MODEL_GROUPS.length,
      '一行一个可选性控件。为 0 时正是项目方报的那个缺陷：' +
        '回调接线全通、屏幕上没有任何可勾的东西'
    )
    // 顺序跟着 model_groups 走：pool-a 已放开、pool-b 未放开。
    assert.deepEqual(
      boxes.map((box) => box.getAttribute('aria-checked')),
      ['true', 'false'],
      '勾选框必须显示**状态**。此前那个图标按钮画的是动作（已放开的行上是一个 ✕），' +
        '扫一眼读到的与事实相反'
    )
    for (const box of boxes) {
      assert.notEqual(
        box.getAttribute('aria-disabled'),
        'true',
        '已设定范围的一档，两个方向都必须可点'
      )
    }
  })

  test('点未放开的那一行 → 带 true 调用；点已放开的 → 带 false', async () => {
    const calls: Array<[string, boolean]> = []
    const container = await mountDetail(userGroup('set'), (name, granted) =>
      calls.push([name, granted])
    )
    const boxes = checkboxes(container)

    await act(async () => boxes[1].click())
    await act(async () => boxes[0].click())

    /*
      参数是「改成这个状态」而不是「切一下」。两者在这里看不出差别，但整列批量
      （`columnBulk` 的 select_all / clear）写的是绝对值 —— 语义一旦漂成 toggle，
      同一个草稿里两条路径会互相抵消，而计数条照样显示"放开 N"。
    */
    assert.deepEqual(
      calls,
      [
        ['pool-b', true],
        ['pool-a', false],
      ],
      '点击必须把目标状态送进 onToggleGranted'
    )
  })

  test('未放开的行：备注框禁用并说明原因，倍率框仍可编辑', async () => {
    const container = await mountDetail(userGroup('set'), () => {})
    const inputs = [
      ...container.querySelectorAll('input'),
    ] as HTMLInputElement[]

    const noteInputs = inputs.filter((input) =>
      (input.getAttribute('aria-label') ?? '').includes('qy_ug_cell_note_label')
    )
    assert.equal(noteInputs.length, 2, '两行各有一个按格备注框')
    assert.equal(
      noteInputs[0].disabled,
      false,
      '已放开的行，按格备注写得进 qy_group_grants'
    )
    assert.equal(
      noteInputs[1].disabled,
      true,
      '未放开的行没有 grant 行，后端只 UPDATE 不 INSERT —— 写不进去就不该让人敲'
    )

    /*
      倍率框**刻意不随可选性禁用**，这不是漏改。`GroupGroupRatio` 与可选清单是
      两份独立的数据，而未放开的格子在下面几种情形里 100% 在扣钱：该档未设范围、
      设了范围但处于影子模式、对角线格，以及"范围里没有它、却因为套餐而可达"
      （`reachable_via` 含 plan）。跟着可选性一起禁用，运营就改不了那些真的在
      扣钱的价，唯一的出路变成先去改访问控制 —— 而那是另一回事。
    */
    const ratioInputs = inputs.filter((input) =>
      (input.getAttribute('aria-label') ?? '').includes(
        'qy_group_matrix_cell_ratio_label'
      )
    )
    assert.equal(ratioInputs.length, 2, '两行各有一个倍率框')
    for (const input of ratioInputs) {
      assert.equal(input.disabled, false, '倍率与可选性是两份独立的数据')
    }
  })
})

describe('未设定范围的一档：不许勾，且必须说出为什么', () => {
  test('勾选框全部禁用', async () => {
    const container = await mountDetail(userGroup('unset'), () => {})
    const boxes = checkboxes(container)

    assert.equal(boxes.length, MODEL_GROUPS.length, '控件仍然在原位')
    for (const box of boxes) {
      // Base UI 把勾选框渲染成 `<span role=checkbox aria-disabled>`（原生 input
      // 是旁边那个隐藏镜像），所以判据只能是 `aria-disabled` —— 断言 `disabled`
      // 属性会永远为假，这条守卫就成了摆设。
      assert.equal(
        box.getAttribute('aria-disabled'),
        'true',
        '没有权威清单时写 grant/revoke 会被后端拒绝，而运营从界面上看不出保存为什么失败'
      )
    }
  })

  test('屏幕上有一句可见的指路，不是只挂在 title 上', async () => {
    const container = await mountDetail(userGroup('unset'), () => {})
    assert.ok(
      (container.textContent ?? '').includes('qy_ug_grant_needs_scope'),
      '一排点不动的灰勾选框 + 屏幕上一个字都没有 = 项目方在删除按钮上投诉的同一个形状'
    )
  })

  test('已设定范围时不出那句指路', async () => {
    const container = await mountDetail(userGroup('set'), () => {})
    assert.ok(
      !(container.textContent ?? '').includes('qy_ug_grant_needs_scope'),
      '对已设范围的一档说"先去设范围"是一句假陈述'
    )
  })
})

/*
  ── 指路必须指向**当前这个外壳上真的存在**的控件 ────────────────────────

  这个组件有两个外壳，而它们的范围入口不是同一个东西：

    编辑弹窗    范围表单内嵌在同屏上一节，标题就是 `qy_ug_scope_section`
                （判据：不传 `onEditScope`，那枚按钮因此不渲染）
    /qy 独立页  详情面板右上角一枚按钮，文案 `qy_group_scope_edit`

  写死其中一个，另一个外壳上的运营就会照着指路去找一段根本不存在的东西 ——
  与项目方投诉的「屏幕上没有一个字解释」是同一类失败，只是换成了指错路。
  而只断言"这句话出现了"的测试对这种坏法是全绿的。
*/
describe('未设定范围的指路，指的是本外壳上真的有的那个控件', () => {
  test('独立页（有「设定模型分组范围」按钮）→ 指按钮', async () => {
    const container = await mountDetail(
      userGroup('unset'),
      () => {},
      () => {}
    )
    assert.ok(
      (container.textContent ?? '').includes('where=SCOPE_BUTTON'),
      '这一页上没有任何叫「可用范围的力度」的段落 —— 那是编辑弹窗里的标题'
    )
  })

  test('编辑弹窗（范围表单就在上一节）→ 指那一节的标题', async () => {
    const container = await mountDetail(userGroup('unset'), () => {})
    assert.ok(
      (container.textContent ?? '').includes('where=SCOPE_SECTION'),
      '弹窗里没有那枚按钮（`onEditScope` 缺省时它根本不渲染）'
    )
  })

  test('两份语言包里那句话都还留着 {{where}} 这个插值', () => {
    // 少了它就是又把某一个外壳的称呼写死了，而上面两条断言在真实语言包下
    // 一条都不会红（它们跑在测试自己的资源上）。
    for (const lang of ['zh', 'en']) {
      const bundle = JSON.parse(
        readFileSync(join(i18nDir, `${lang}.json`), 'utf8')
      ) as Record<string, string>
      assert.ok(
        bundle.qy_ug_grant_needs_scope.includes('{{where}}'),
        `${lang}.json 里的 qy_ug_grant_needs_scope 把某一个外壳的称呼写死了`
      )
    }
  })
})
