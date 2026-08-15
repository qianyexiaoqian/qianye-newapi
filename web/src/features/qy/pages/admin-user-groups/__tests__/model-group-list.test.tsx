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
 * 编辑用户分组：模型分组是一个**能增能删的列表**，不是一堵灰墙。
 *
 * # 守什么
 *
 * 项目方带截图报的原话：「这个编辑用户分组，这里分配的模型分组为何不可以
 * 删除/添加模型分组，你改成列表框的模式展现这些分配的模型分组。」
 *
 * 上一版把站上**全部**模型分组平铺成行，每行一个勾选框；而勾选框在"这一档
 * 还没有自己的清单"时全部禁用，于是每一行下面都跟着同一段长文，叫运营先去
 * 上面那段「可用范围的力度」里把开关打开。那个开关是 `qy_group_scopes` 有没有
 * 这一行的直接投影 —— 一个内部状态被做成了运营必须先理解、先操作的前置动作。
 *
 * 现在只剩两块：已分配的一个列表，和一个从尚未分配里挑的入口。"有没有自己的
 * 清单"由列表内容隐式表达。下面每一组各钉住一件在浏览器里看得见、且坏了不会有
 * 任何报错的事：
 *
 *   1. 列表里每一项都有一个真能点的「移除」，且它走的是既有的
 *      `onToggleGranted` 那条链（同一份草稿、同一道影响面闸门）；
 *   2. 添加入口只列**尚未分配**的，点进去同样走那条链；
 *   3. 「移除最后一项」不许静默落地 —— 空清单与没有清单是相反的两件事，
 *      必须二选一；
 *   4. 还没有清单的那一档：列表读的是它此刻真的能用的那些，增删先问一句
 *      「建立独立清单」，**没有任何禁用的勾选框、也没有那三段重复长文**；
 *   5. 「这份清单会拦人」在有清单时说得出口、在没有清单时一个字都不提。
 *
 * ── 影子档（shadow）下线之后这一份保下来的是什么 ──
 *
 * 曾经这一屏还有第二个运营决定：配好的清单是先只观察（shadow），还是现在就
 * 拦人（enforce）。项目方要求整个去掉 —— 编辑用户分组即刻生效，令牌该拦就拦。
 * 于是「两档之间怎么切、切之前要过哪些闸门」这一整族用例覆盖的行为不复存在，
 * 连同它们一起删掉；而下面两件事在只剩一档之后**恒成立**，因此改写保留：
 *
 *   · 有清单 ⇒ 屏幕上说得出「会拦人」，没有清单 ⇒ 一个字都不提（假陈述）；
 *   · 总说明按「有没有自己的清单」二选一，两句不许互串。
 *
 * 「建清单不该让任何人当场 403」这条保证此前挂在 shadow 上（新清单一律先建成
 * 影子）。它没有随 shadow 消失，只是改由**清单先按此刻真的能用的那些原样建立**
 * 来兑现，所以那一条也改写保留，断言换成确认弹窗里的那句承诺。
 *
 * 文案走真实的 `src/i18n/qy/zh.json`：键写错时 i18next 原样吐键名，下面的
 * 中文断言当场变红。
 *
 * 变异实验（十二条，逐条跑过；基线 20 pass / 0 fail）：
 *   · `QyUgRowAction` 的「移除」提前 `return null`          → 9 条红
 *   · 每一列都无条件进 `addable`                            → 1 条红
 *   · 「移除最后一项」的判据从 `<= 1` 改成 `<= 0`           → 5 条红
 *   · 没有清单时按 `granted` 而不是 `model_groups` 画列表   → 4 条红
 *   · 建清单的确认键不再看 `hasUnsavedDraft`                → 1 条红
 *   · 备注框画在每一行上（不看 `origin === 'scope'`）       → 1 条红
 *   · 建清单弹窗改按列轴长度报数                            → 1 条红
 *   · 建清单跳过 `afterApply`，直接落草稿                   → 1 条红
 *   · 状态条不再看 `scoped`，无条件渲染                     → 1 条红
 *   · 状态条上那枚「会拦人」徽章删掉                        → 1 条红
 *   · 总说明的两句对调（`scoped` ↔ `!scoped`）              → 1 条红
 *   · 「更多范围设置」的保存键不再看 `hasUnsavedDraft`      → 1 条红
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
  QyGmScopeRequest,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', '..', '..', '..', '..')

const domWindow = new Window({ height: 900, width: 1280 })
const domGlobals = [
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

const zhBundle = JSON.parse(
  readFileSync(join(srcDir, 'i18n', 'qy', 'zh.json'), 'utf8')
) as Record<string, string>
const enBundle = JSON.parse(
  readFileSync(join(srcDir, 'i18n', 'qy', 'en.json'), 'utf8')
) as Record<string, string>

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhBundle } },
})

const { qyGmCellKey } = await import('../../admin-group-matrix/lib/draft')
const { QyUgGroupDetail } = await import('../components/user-group-detail')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/* ── 夹具 ──────────────────────────────────────────────────────────── */

const UG = 'qy-canary-ug'

function modelGroup(name: string): QyGmModelGroup {
  return {
    name,
    base_ratio: '1',
    has_channels: true,
  grantable: true,
    display_name: '',
    note: '',
    user_selectable: true,
    usable_description: '',
  }
}

function cell(name: string, patch: Partial<QyGmCell>): QyGmCell {
  return {
    user_group: UG,
    model_group: name,
    granted: false,
    ratio: null,
    source: 'inherit',
    inherited_from: '1',
    note: '',
    effective_note: name,
    note_pending: false,
    note_source: 'group_name',
    ...patch,
  }
}

function userGroup(patch: Partial<QyGmUserGroup>): QyGmUserGroup {
  return {
    name: UG,
    display_name: '',
    note: '',
    enabled: true,
    sort_order: 0,
    registered: true,
    observed: true,
    topup_ratio: null,
    topup_ratio_effective: '1',
    model_groups: [],
    user_count: 12,
    active_token_count: 3,
    managed: true,
    scope_state: 'set',
    // 影子档下线之后后端只可能下发这一种组合：有范围行 = 清单立即生效
    // （存量的 shadow 行由 migrateShadowScopesToEnforce 在启动时改写）。
    mode: 'enforce',
    scope_enforced: true,
    allow_auto: false,
    scope_note: '',
    self_excluded: false,
    self_inserted: false,
    ...patch,
  }
}

const COLUMNS = ['pool-a', 'pool-b', 'pool-c'].map(modelGroup)

type Recorded = {
  toggles: Array<[string, boolean]>
  scopes: QyGmScopeRequest[]
  /** 范围提交带上来的"回读之后再做"的那个回调。 */
  afterApply: Array<(() => void) | undefined>
}

const mounted: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

async function unmountAll() {
  for (;;) {
    const entry = mounted.pop()
    if (entry == null) return
    await act(async () => entry.root.unmount())
    entry.container.remove()
  }
}

after(unmountAll)

async function mountDetail(options: {
  row: QyGmUserGroup
  cells: QyGmCell[]
  hasUnsavedDraft?: boolean
}): Promise<Recorded> {
  await unmountAll()
  const log: Recorded = { toggles: [], scopes: [], afterApply: [] }

  const serverCells = new Map(
    options.cells.map((item) => [qyGmCellKey(UG, item.model_group), item])
  )
  const data: QyGmMatrixResponse = {
    user_groups: [options.row],
    model_groups: COLUMNS,
    cells: options.cells,
    base_ratio_hash: 'h',
    snapshot: { loaded: true, age_seconds: 1, version: 1 },
    warnings: [],
    shadow_write_denies: [],
  }

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QyUgGroupDetail
        data={data}
        userGroup={options.row}
        serverCells={serverCells}
        draft={new Map()}
        grantedCount={options.row.model_groups.length}
        onToggleGranted={(name, granted) => log.toggles.push([name, granted])}
        onRatioChange={() => {}}
        onNoteChange={() => {}}
        onCopyFrom={() => {}}
        scope={{
          isSaving: false,
          hasUnsavedDraft: options.hasUnsavedDraft ?? false,
          onSubmit: (body, afterApply) => {
            log.scopes.push(body)
            log.afterApply.push(afterApply)
          },
        }}
        scrollList={false}
      />
    )
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  mounted.push({ container, root })
  return log
}

/* ── 查询与点击 ────────────────────────────────────────────────────── */

/** 整屏文本 —— 属性里的 `title` 不算数。 */
function screenText(): string {
  return document.body.textContent ?? ''
}

function copy(key: string): string {
  const value = zhBundle[key]
  assert.ok(
    value != null && value !== '',
    `文案键 ${key} 没有登记进 src/i18n/qy/zh.json`
  )
  return value
}

/**
 * 取一句真实文案并把插值填上 —— 断言一律对**完整的 aria-label** 比对。
 *
 * 不用"键值里第一个 `{{` 之前那一段"当前缀匹配：两条真实文案当场把它打穿 ——
 * `qy_ugl_add_one_label` 与 `qy_ugl_remove_label` 都以「把 」开头（移除键会被
 * 数成添加键），而以插值开头的键前缀是空串（`startsWith('')` 恒真，一屏所有
 * 输入框都被数进来）。
 */
function fill(key: string, vars: Record<string, string>): string {
  let text = copy(key)
  for (const [name, value] of Object.entries(vars)) {
    text = text.replaceAll(`{{${name}}}`, value)
  }
  return text
}

function buttons(): HTMLButtonElement[] {
  return [...document.body.querySelectorAll('button')] as HTMLButtonElement[]
}

/** 按可见文字找一个按钮。**找不到就当场失败**，不返回 undefined。 */
function button(text: string): HTMLButtonElement {
  const found = buttons().filter((node) =>
    (node.textContent ?? '').includes(text)
  )
  assert.equal(
    found.length,
    1,
    `按钮「${text}」应当恰好有一个，实际 ${found.length} 个`
  )
  return found[0]
}

function buttonByLabel(label: string): HTMLButtonElement {
  const found = buttons().filter(
    (node) => node.getAttribute('aria-label') === label
  )
  assert.equal(found.length, 1, `按钮 aria-label=「${label}」应当恰好有一个`)
  return found[0]
}

async function click(node: HTMLElement) {
  await act(async () => node.click())
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

/** 屏幕上所有按钮的 aria-label。 */
function buttonLabels(): string[] {
  return buttons()
    .map((node) => node.getAttribute('aria-label'))
    .filter((text): text is string => text != null)
}

/** 按**完整** aria-label 找输入框。缺席时返回 `null`（重复时当场失败）。 */
function inputByLabel(text: string): HTMLInputElement | null {
  const found = [...document.body.querySelectorAll('input')].filter(
    (node) => node.getAttribute('aria-label') === text
  ) as HTMLInputElement[]
  assert.ok(found.length <= 1, `输入框「${text}」重复了 ${found.length} 次`)
  return found[0] ?? null
}

const removeLabel = (modelGroup: string) =>
  fill('qy_ugl_remove_label', { modelGroup })
const addLabel = (modelGroup: string) =>
  fill('qy_ugl_add_one_label', { modelGroup })
const ratioLabel = (modelGroup: string) =>
  fill('qy_group_matrix_cell_ratio_label', { userGroup: UG, modelGroup })
const noteLabel = (modelGroup: string) =>
  fill('qy_ug_cell_note_label', { modelGroup })

/* ── 1 / 2. 已有自己的清单：能删、能加 ─────────────────────────────── */

describe('已有自己的清单：列表能增能删，且走既有的 onToggleGranted 那条链', () => {
  const cells = [
    cell('pool-a', { granted: true }),
    cell('pool-b', { granted: true }),
  ]
  const row = userGroup({ model_groups: ['pool-a', 'pool-b'] })

  test('每一项一行，行尾的「移除」带 false 调用', async () => {
    const log = await mountDetail({ row, cells })

    const labels = buttonLabels()
    assert.deepEqual(
      COLUMNS.map((column) => labels.includes(removeLabel(column.name))),
      [true, true, false],
      '清单里那两项各有一个移除键，不在清单里的那一项没有'
    )

    await click(buttonByLabel(removeLabel('pool-b')))
    assert.deepEqual(
      log.toggles,
      [['pool-b', false]],
      '移除必须落进同一份草稿：那条链上挂着"含撤销的改动，看过影响面之前不许保存"的闸门'
    )
  })

  test('添加入口只列尚未分配的，点它带 true 调用', async () => {
    const log = await mountDetail({ row, cells })
    await click(button(copy('qy_ugl_add')))

    const labels = buttonLabels()
    assert.deepEqual(
      COLUMNS.map((column) => labels.includes(addLabel(column.name))),
      [false, false, true],
      '已经在清单里的不该再出现在"可添加"里 —— 那会让运营以为自己漏配了'
    )

    await click(buttonByLabel(addLabel('pool-c')))
    assert.deepEqual(log.toggles, [['pool-c', true]])
  })

  test('倍率框每一行都在（它在这些行上 100% 在扣钱）', async () => {
    await mountDetail({ row, cells })
    for (const name of ['pool-a', 'pool-b']) {
      const input = inputByLabel(ratioLabel(name))
      assert.ok(input != null, `${name} 这一行少了倍率框`)
      assert.equal(input.disabled, false)
    }
  })
})

/* ── 3. 清空清单 = 二选一 ──────────────────────────────────────────── */

describe('移除最后一项：空清单与没有清单是相反的两件事，必须二选一', () => {
  const cells = [cell('pool-a', { granted: true })]
  const row = userGroup({ model_groups: ['pool-a'] })

  test('不静默落地，先弹出两个出口', async () => {
    const log = await mountDetail({ row, cells })
    await click(buttonByLabel(removeLabel('pool-a')))

    assert.deepEqual(
      log.toggles,
      [],
      '直接撤销掉最后一项，这一档会静默变成"一个都不给用"，而运营想做的多半是反过来'
    )
    const text = screenText()
    assert.ok(
      text.includes(copy('qy_ugl_clear_fallback')),
      '缺"回落全局清单"这个出口'
    )
    assert.ok(
      text.includes(copy('qy_ugl_clear_isolate')),
      '缺"一个都不给用"这个出口'
    )
  })

  test('选「回落全局清单」→ 提交 managed:false（删掉 scope 行）', async () => {
    const log = await mountDetail({ row, cells })
    await click(buttonByLabel(removeLabel('pool-a')))
    await click(button(copy('qy_ugl_clear_fallback_action')))

    assert.equal(log.scopes.length, 1)
    assert.equal(log.scopes[0].managed, false)
    assert.deepEqual(
      log.toggles,
      [],
      '回落是删 scope 行，不是逐个撤销 —— 多发的撤销动作会被后端当成对一份不存在的清单的写入'
    )
  })

  test('选「一个都不给用」→ 保留 scope 行，走草稿撤销', async () => {
    const log = await mountDetail({ row, cells })
    await click(buttonByLabel(removeLabel('pool-a')))
    await click(button(copy('qy_ugl_clear_isolate_action')))

    assert.deepEqual(log.toggles, [['pool-a', false]])
    assert.deepEqual(log.scopes, [], '隔离档要保留 scope 行，不能顺手删掉')
  })

  /*
    ── 「回落全局清单」也会丢草稿，而它此前既没有闸门也没有一句提示 ──

    这一支与「建立独立清单」走的是同一次范围写入，成功之后同样会回读服务端
    状态、清空整张矩阵的未保存草稿。而到达这个弹窗时几乎必然有草稿：把清单
    删到只剩一项这个动作本身就产生了若干条未保存的撤销。不闸的话，运营刚改的
    那个倍率静默消失，屏幕上只剩一句绿色的「范围已保存」—— 而同一个弹窗正在
    向他保证「已配好的倍率原样留着」。
  */
  test('有未保存草稿时，「回落全局清单」按不动并说明为什么', async () => {
    const log = await mountDetail({ row, cells, hasUnsavedDraft: true })
    await click(buttonByLabel(removeLabel('pool-a')))

    assert.equal(
      button(copy('qy_ugl_clear_fallback_action')).disabled,
      true,
      '回落是一次范围写入，它会回读并清空草稿 —— 静默丢弃是这条链路最坏的失败方式'
    )
    assert.ok(
      screenText().includes(copy('qy_ugl_scope_write_discards_draft')),
      '按不动就必须有字：一个灰着且不解释的按钮正是这次被投诉的那一幕'
    )
    await click(button(copy('qy_ugl_clear_fallback_action')))
    assert.deepEqual(log.scopes, [], '闸住了就不许提交')
  })

  test('「一个都不给用」不受草稿影响 —— 它一个字节都不写服务端', async () => {
    // 两支一起闸掉会让运营在有草稿时连隔离都做不了，而那一支本来是安全的。
    const log = await mountDetail({ row, cells, hasUnsavedDraft: true })
    await click(buttonByLabel(removeLabel('pool-a')))
    await click(button(copy('qy_ugl_clear_isolate_action')))
    assert.deepEqual(log.toggles, [['pool-a', false]])
  })
})

/* ── 4. 还没有清单的那一档 ─────────────────────────────────────────── */

describe('还没有自己的清单：列表 = 此刻真的能用的那些，增删先问一句', () => {
  // 残留的 grant 行（撤销接管时刻意保留的那批）不许冒充清单。
  const cells = [cell('pool-c', { granted: true })]
  const row = userGroup({
    managed: false,
    scope_state: 'unset',
    scope_enforced: false,
    model_groups: ['pool-a', 'pool-b'],
  })

  test('列出的是 model_groups，残留 grant 行落进"可添加"', async () => {
    await mountDetail({ row, cells })
    const labels = buttonLabels()
    assert.deepEqual(
      COLUMNS.map((column) => labels.includes(removeLabel(column.name))),
      [true, true, false],
      'pool-c 只有一条残留的 grant 行，它不在上游实际可用名单里 —— 不许冒充清单'
    )
    assert.ok(
      labels.includes(addLabel('pool-c')) === false,
      '添加面板还没展开，这时不该有任何"加入清单"键'
    )
  })

  test('屏幕上没有任何禁用的勾选框（上一版整屏就是这个）', async () => {
    await mountDetail({ row, cells })
    const boxes = [...document.body.querySelectorAll('[role="checkbox"]')]
    assert.equal(
      boxes.length,
      0,
      '项目方原话「为何不可以删除/添加模型分组」—— 那一屏的控件全是灰的勾选框'
    )
  })

  test('每一行下面重复的那三段长文，一段都不许再出现', async () => {
    await mountDetail({ row, cells })
    const text = screenText()
    for (const key of [
      'qy_ug_grant_needs_scope',
      'qy_ug_cell_note_needs_scope',
      'qy_ug_cell_note_needs_grant',
    ]) {
      assert.ok(!text.includes(key), `${key} 又被贴回界面上了`)
      assert.equal(zhBundle[key], undefined, `${key} 还留在 zh.json 里`)
      assert.equal(enBundle[key], undefined, `${key} 还留在 en.json 里`)
    }
  })

  test('倍率框仍然可编辑 —— 这一档的每一格此刻都在真的扣钱', async () => {
    await mountDetail({ row, cells })
    for (const name of ['pool-a', 'pool-b']) {
      const input = inputByLabel(ratioLabel(name))
      assert.ok(input != null, `${name} 这一行少了倍率框`)
      assert.equal(input.disabled, false)
    }
  })

  test('按格备注框一个都不画（后端两道闸门都不满足，写了也存不下）', async () => {
    await mountDetail({ row, cells })
    for (const name of ['pool-a', 'pool-b']) {
      assert.equal(inputByLabel(noteLabel(name)), null)
    }
  })

  /*
    ── 「这一下点击本身不该让任何人当场 403」 ──

    这条保证此前挂在 shadow 上：新清单一律先建成影子，配错了也拦不住人。
    影子档下线之后它由**清单的初始内容**兑现 —— 先按这一档此刻真的能用的那些
    原样建立，再叠这一次增删。所以确认弹窗里那句「按此刻真的能用的那 N 个原样
    建立」不是一句客套，它是这条保证在屏幕上唯一的落款，而 N 必须取
    `model_groups`（后端算好的"此刻真的能用的那些"），不是草稿里勾了几个。
  */
  test('移除先问「建立独立清单」，说清按现状原样建立，回读之后才落这次改动', async () => {
    const log = await mountDetail({ row, cells })
    await click(buttonByLabel(removeLabel('pool-a')))
    assert.deepEqual(log.toggles, [], '建清单之前落草稿，后端会拒绝这次写入')
    assert.ok(
      screenText().includes(
        fill('qy_ugl_create_carries', {
          count: String(row.model_groups.length),
        })
      ),
      '新清单按"此刻真的能用的那 2 个"原样建立 —— 报错了数就等于向运营承诺了一件它不会做的事'
    )

    await click(button(copy('qy_ugl_create_confirm')))
    assert.equal(log.scopes.length, 1)
    assert.equal(log.scopes[0].managed, true)

    assert.deepEqual(
      log.toggles,
      [],
      '范围保存会回读并清空本地草稿：先落草稿等于这次增删被静默丢掉'
    )
    log.afterApply[0]?.()
    assert.deepEqual(
      log.toggles,
      [['pool-a', false]],
      '回读之后必须把这次增删补上，否则运营看到的列表与他刚点的那一下不一致'
    )
  })

  test('手上有未保存的格子改动时，建清单的确认键按不动并说明原因', async () => {
    const log = await mountDetail({ row, cells, hasUnsavedDraft: true })
    await click(buttonByLabel(removeLabel('pool-a')))
    assert.ok(screenText().includes(copy('qy_ugl_create_discards_draft')))
    assert.equal(button(copy('qy_ugl_create_confirm')).disabled, true)
    assert.deepEqual(log.scopes, [])
  })
})

/* ── 5. 有清单 = 会拦人，且范围写入照样受草稿闸门约束 ───────────────── */

describe('有自己的清单：屏上直说"会拦人"，范围写入受草稿闸门约束', () => {
  const cells = [cell('pool-a', { granted: true })]

  /*
    影子档下线之后只剩一档：有范围行 = 清单立即生效。于是「这份清单会拦人」
    这句话在有清单时**恒成立** —— 但恒成立不等于可以不说：运营刚配完的这份
    清单会让一批令牌当场 403，屏幕上必须有一处直说这件事。

    另一半同样重要，而且方向相反：还跟着全局清单走的那一档不许出现这句话。
    给一个不存在的清单标「会拦人」既是噪声也是假陈述，运营会据此认为收紧
    已经落地然后走人。
  */
  test('有清单就直说"会拦人"，还没有清单的那一档一个字都不提', async () => {
    await mountDetail({ row: userGroup({ model_groups: ['pool-a'] }), cells })
    assert.ok(
      screenText().includes(copy('qy_ugl_mode_enforce_chip')),
      '配完一份会让人 403 的清单，屏幕上不能一个字都没提醒过他'
    )

    await mountDetail({
      row: userGroup({
        managed: false,
        scope_state: 'unset',
        scope_enforced: false,
        model_groups: ['pool-a'],
      }),
      cells,
    })
    assert.ok(
      !screenText().includes(copy('qy_ugl_mode_enforce_chip')),
      '这一档还跟着全局「用户可选分组」走，说它"会拦人"是一句假陈述'
    )
  })

  test('保存「更多范围设置」会丢草稿 —— 这里闸住', async () => {
    // 保存 allow_auto / 范围备注同样是一次范围写入：它回读服务端状态、清空
    // 整张矩阵的草稿。它不是止血动作，所以与建清单、回落全局取同一个口径。
    const log = await mountDetail({
      row: userGroup({ model_groups: ['pool-a'] }),
      cells,
      hasUnsavedDraft: true,
    })
    await click(button(copy('qy_ugl_advanced')))
    assert.equal(button(copy('qy_group_scope_apply')).disabled, true)
    assert.ok(screenText().includes(copy('qy_ugl_scope_write_discards_draft')))
    await click(button(copy('qy_group_scope_apply')))
    assert.deepEqual(log.scopes, [])
  })
})

/* ── 6. 总说明必须按「有没有自己的清单」分叉 ─────────────────────────── */

describe('总说明：按「有没有自己的清单」二选一，两句不许互串', () => {
  const cells = [cell('pool-a', { granted: true })]

  /*
    这两句在屏幕上是**先读到的那一句**，语气都很肯定，而它们说的是相反的
    两件事：一句是「只有下面列出的模型分组可用，全局清单对它整体不再生效」，
    另一句是「这一档还跟着全局清单走」。串了任意一个方向都没有报错：
    往还没有清单的那一档说前者，运营认为收紧已经落地然后走人；往已经有清单的
    那一档说后者，他会以为自己刚配的清单一个字节都不算数，再去别处重配一遍。
  */
  test('有清单读到"只有下面列出的可用"，没有清单读到"跟着全局清单走"', async () => {
    await mountDetail({ row: userGroup({ model_groups: ['pool-a'] }), cells })
    const scopedText = screenText()
    assert.ok(
      scopedText.includes(copy('qy_ugl_own_note')),
      '有清单必须读到"只有下面列出的可用"'
    )
    assert.ok(
      !scopedText.includes(copy('qy_ugl_fallback_note')),
      '"还跟着全局清单走"在已经有清单的那一档上是一句假陈述'
    )

    await mountDetail({
      row: userGroup({
        managed: false,
        scope_state: 'unset',
        scope_enforced: false,
        model_groups: ['pool-a'],
      }),
      cells,
    })
    const fallbackText = screenText()
    assert.ok(
      fallbackText.includes(copy('qy_ugl_fallback_note')),
      '没有清单必须读到"跟着全局清单走"'
    )
    assert.ok(
      !fallbackText.includes(copy('qy_ugl_own_note')),
      '"只有下面列出的可用"在这一档上是一句假陈述 —— 它比不说更糟'
    )
  })

  test('清单空着这件事整屏只说一次', async () => {
    // 上一版同屏同时出现：页眉徽章的 title、正文里的整段告警、列表空态提示，
    // 而前两处逐字相同。读的人要先花时间确认这几段是不是在说不同的事。
    await mountDetail({
      row: userGroup({ model_groups: [], scope_state: 'empty' }),
      cells: [],
    })
    const text = screenText()
    const empty = copy('qy_ugl_empty_scope')
    assert.equal(
      text.split(empty).length - 1,
      1,
      '「清单空着」在正文里只许出现一次'
    )
    assert.ok(
      !text.includes(
        copy('qy_group_scope_state_empty_hint').replace('{{userGroup}}', UG)
      ),
      '矩阵页那一段同义长文不许再在这一屏上重复一遍'
    )
  })

  test('没有清单时，页眉徽章的 title 不再指向这一屏不存在的控件', async () => {
    // 默认那一句以「先给这个用户分组设定范围」收尾，指的是矩阵页上那个按钮；
    // 这一屏里范围行由列表内容隐式表达，那个控件根本不渲染。指路指向一个不存在
    // 的控件比不给指路更糟 —— 运营会去找、找不到，然后认为页面坏了。
    await mountDetail({
      row: userGroup({
        managed: false,
        scope_state: 'unset',
        scope_enforced: false,
        model_groups: ['pool-a'],
      }),
      cells: [],
    })
    const titles = new Set(
      [...document.body.querySelectorAll('[title]')].map((node) =>
        node.getAttribute('title')
      )
    )
    assert.ok(
      !titles.has(copy('qy_group_scope_unset_hint')),
      '「先给这个用户分组设定范围」在这一屏上指向一个已经不存在的控件'
    )
    assert.ok(
      titles.has(copy('qy_ugl_fallback_note')),
      '覆盖成这一屏正文里已经写着的那一句'
    )
  })
})
