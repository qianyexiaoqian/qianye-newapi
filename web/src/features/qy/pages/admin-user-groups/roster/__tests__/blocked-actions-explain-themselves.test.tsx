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
 * 「按不动的按钮必须自己解释为什么」。
 *
 * # 守什么
 *
 * 项目方原话：「比如当前数据，我要删除：default 用户分组，我选择了其他分组
 * 仍然无法删除。但是其他分组又可以删除迁移。」—— 他点了、按钮是死的、
 * 屏幕上没有一个字，于是他花时间去猜。
 *
 * 后端 `deletability` / `renamability` 每一条拒绝分支都写了很清楚的理由，
 * 缺的是把它**变成屏幕上的字**。所以下面每一条断言的都是"文本出现在 DOM 里"，
 * **不是**"按钮 disabled"：一个只断言 `disabled` 的测试在这次缺陷下是绿的。
 *
 * 三组分别堵住三种沉默：
 *
 *   1. 弹窗里的服务端拒绝 —— 四条分支各要出现两句话：后端给的原因，
 *      以及前端按 `block_code` 给的"那我该干什么"。少了后半句，运营读完
 *      「以下套餐的升降级分组指向它」还是不知道该去哪一页。
 *   2. 删除键为什么是灰的 —— 四种闸门状态一条都不许沉默。此前只有
 *      `needs_target` 有字，`blocked` / `needs_ack` / 刷新中都只是变灰。
 *   3. 入口级的两条（`default` / 未登记）—— 此前理由塞在 `title` 里，而
 *      **禁用的按钮在多数浏览器上不派发指针事件**，那句话永远不出现。
 *      所以这里额外断言它是**文本节点**，而不是某个属性。
 *
 * 文案走真实的 `src/i18n/qy/zh.json`，不是测试里现编的假资源：键写错时
 * i18next 会原样吐出键名，下面的中文断言当场变红 —— 这一条同时把
 * 「新增分支忘了补文案」钉住。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import type { QyUgrBlockCode, QyUgrImpact } from '../types'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', '..', '..', '..', '..', '..')

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

const { QyUgrDeleteDialog } = await import('../components/delete-dialog')
const { QyUgrActionBlockNote } = await import('../components/action-block-note')
const {
  QY_UGR_BLOCK_NEXT_STEP_KEY,
  QY_UGR_DELETE_GATE_KEY,
  QY_UGR_RENAME_ENTRY_BLOCK_KEY,
  qyUgrDeleteEntry,
} = await import('../lib/gates')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const mounted: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

async function mount(node: React.ReactNode) {
  await unmountAll()
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => root.render(node))
  // 迁移目标那个下拉的浮层定位器在挂载后还会隔着一个宏任务再跑一轮 effect。
  // 不把它冲掉的话那一轮落在 act 之外，React 会打印一串警告 —— 而那串警告会
  // 淹掉真正的失败信息。
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  mounted.push({ container, root })
}

async function unmountAll() {
  for (;;) {
    const entry = mounted.pop()
    if (entry == null) return
    await act(async () => entry.root.unmount())
    entry.container.remove()
  }
}

after(unmountAll)

/** 整屏文本 —— 属性里的 `title` 不算数，这正是本次要区分的东西。 */
function screenText(): string {
  return document.body.textContent ?? ''
}

/**
 * 取一句真实文案。**键不存在时当场失败**，不允许回落成空串。
 *
 * 回落成空串的话 `text.includes('')` 恒真，整条断言变成一句空话 —— 而它
 * 恰恰是"新增分支忘了补文案"这种缺陷唯一的检出口。
 */
function copy(key: string): string {
  const value = zhBundle[key]
  assert.ok(
    value != null && value !== '',
    `文案键 ${key} 没有登记进 src/i18n/qy/zh.json`
  )
  return value
}

function impact(patch: Partial<QyUgrImpact>): QyUgrImpact {
  return {
    blocking: [],
    blocking_plans: [],
    deletable: true,
    empty_group_tokens: 0,
    name: 'qy-canary-ug',
    renamable: true,
    residues: [],
    subscriptions: 0,
    targets: [
      {
        display_name: '',
        enabled: true,
        name: '黄金档',
        usable_groups: 2,
        users: 8,
      },
    ],
    tokens: 0,
    users: 0,
    ...patch,
  }
}

async function mountDeleteDialog(
  current: QyUgrImpact | null,
  extra?: {
    target?: string
    ack?: boolean
    isRefreshing?: boolean
    isLoading?: boolean
  }
) {
  await mount(
    <QyUgrDeleteDialog
      open
      onOpenChange={() => {}}
      impact={current}
      isLoading={extra?.isLoading ?? false}
      isRefreshing={extra?.isRefreshing ?? false}
      isDeleting={false}
      target={extra?.target ?? ''}
      onTargetChange={() => {}}
      ack={extra?.ack ?? false}
      onAckChange={() => {}}
      onConfirm={() => {}}
    />
  )
}

/* ── 1. 服务端的每一条拒绝分支，屏幕上都有两句话 ────────────────────── */

describe('删不了的时候，弹窗要说清原因与下一步', () => {
  const branches: Array<{ code: QyUgrBlockCode; reason: string }> = [
    {
      code: 'upstream_default',
      reason:
        'default 是 users.group 这一列的数据库默认值 —— 删掉它之后，' +
        '任何一次没有显式指定分组的建号都会落进一个不存在的分组。这一档不可删除',
    },
    {
      code: 'blocking_plans',
      reason:
        '以下套餐的升级/降级分组指向它：月卡、年卡。请先在套餐里改掉这两处引用',
    },
    {
      code: 'blocking_residues',
      reason:
        '以下配置指向它且必须由人来决定新值：抽奖活动的参与名单（qy_lottery_rounds，2 行）',
    },
    {
      code: 'no_migration_target',
      reason:
        '这一档还有用户，但站上没有第二个已登记的用户分组可以迁 —— 请先新建一个目标分组',
    },
  ]

  for (const branch of branches) {
    test(`${branch.code}：后端原话与指路文案都要落到 DOM 上`, async () => {
      await mountDeleteDialog(
        impact({
          block_code: branch.code,
          block_reason: branch.reason,
          deletable: false,
        })
      )
      const text = screenText()

      // 这一条是本次缺陷的正体：理由必须**出现在界面上**，而不是只让按钮变灰。
      assert.ok(
        text.includes(branch.reason),
        `后端给的拒绝理由没有出现在屏幕上，运营只会看到一个按不动的按钮：${branch.code}`
      )

      const nextStep = zhBundle[QY_UGR_BLOCK_NEXT_STEP_KEY[branch.code]]
      assert.ok(nextStep != null, `${branch.code} 的指路文案没有登记进 zh.json`)
      assert.ok(
        text.includes(nextStep),
        `${branch.code} 只说了"为什么不行"、没说"那我该干什么" —— ` +
          `项目方为此花掉的时间正是这次报上来的缺陷`
      )

      // 灰按钮旁边也要有一句，否则运营的视线落在按钮上时仍然什么都读不到。
      assert.ok(
        text.includes(copy('qy_ugr_gate_blocked')),
        '删除键旁边缺少"为什么按不动"的一句话'
      )
    })
  }

  /*
    上面四条断言的前提是「这个弹窗在生产里真的打得开」。

    `upstream_default` 那一条曾经不成立：表行上的删除按钮对 default 是
    `disabled`，而这个弹窗是全站唯一渲染 `impact.block_reason` 的地方 ——
    于是那一条断言直接 mount 弹窗、手喂 `block_code`，绕过了入口闸门，对应不到
    任何一个可达的生产状态。运营真正读到的是前端另写的一份中文副本，两份文本
    之间没有任何跨语言的一致性检查。

    所以这里把入口闸门本身钉住：**四条分支的弹窗都要打得开**。
  */
  test('四条分支的弹窗在生产里都打得开 —— 否则上面的断言是自说自话', () => {
    for (const row of [
      // default：deletability 的第一条分支。
      { name: 'default', registered: true, user_count: 12 },
      // 其余三条分支都发生在普通的已登记分组上（套餐引用 / block 残留 /
      // 没有迁移目标），入口一律不拦。
      { name: 'qy-canary-ug', registered: true, user_count: 3 },
      // 未登记但有人挂着：后端 `adminDeleteUserGroup` 明说这一类要能删。
      { name: '历史遗留档', registered: false, user_count: 37 },
    ]) {
      assert.equal(
        qyUgrDeleteEntry(row).enabled,
        true,
        `${row.name} 的删除入口被关掉了 —— 后端为它写的拒绝理由与替代做法` +
          '因此永远到不了屏幕，而屏幕上那一份是谁也没在维护的前端副本'
      )
    }
  })

  test('改名被同一道闸门挡住时，在删除弹窗里一并说出来', async () => {
    // 不说的话运营的下一步几乎必然是「删不掉就改个名」—— 同一次事故换个入口，
    // 而他要等到提交之后才知道这条路也走不通。
    const reason =
      '以下配置指向它且改不动（它们是冻结值），必须由人来先处理：' +
      '抽奖活动的参与名单（qy_lottery_rounds，2 行）'
    await mountDeleteDialog(
      impact({
        block_code: 'blocking_residues',
        block_reason: '删除同理',
        deletable: false,
        rename_block_code: 'blocking_residues',
        rename_block_reason: reason,
        renamable: false,
      })
    )
    assert.ok(screenText().includes(reason))
  })
})

/* ── 2. 删除键为什么是灰的：四种状态一条都不许沉默 ──────────────────── */

describe('删除键旁边永远有一句话', () => {
  /*
    影响面还在路上的那一条**曾经画不出来**：那一段闸门文案原本是
    `impact == null ? <Loading/> : (…)` 三元里 `else` 分支的子节点，而
    `qyUgrDeleteBlock` 只在 `impact == null` 时返回 `'loading'` —— 两个条件
    互斥，于是穷尽 `Record` 里的那一条成了没有人读得到的死文案。

    穷尽 `Record` 挡得住"漏写文案"，挡不住"写了但那一支不可达"。所以这一条
    断言的是**渲染结果**：把弹窗挂在 `impact == null` 上，那句话必须在屏幕上。
  */
  test('影响面还在路上 —— 这一条曾经是画不出来的死文案', async () => {
    await mountDeleteDialog(null, { isLoading: true })
    assert.ok(
      screenText().includes(copy('qy_ugr_gate_loading')),
      '删除键在加载期间是禁用的，而屏幕上没有一个字说明为什么'
    )
  })

  test('还有人而没选迁移目标', async () => {
    await mountDeleteDialog(impact({ users: 3 }))
    assert.ok(screenText().includes(copy('qy_ugr_gate_needs_target')))
  })

  test('目标一个模型分组都用不了、还没勾强制覆盖', async () => {
    await mountDeleteDialog(
      impact({
        diff: {
          changes: [],
          from: 'qy-canary-ug',
          loses_everything: true,
          to: '黄金档',
          unchanged: 0,
        },
        users: 700,
      }),
      { target: '黄金档' }
    )
    assert.ok(screenText().includes(copy('qy_ugr_gate_needs_ack')))
  })

  test('正在按新目标重算差异 —— 这一份是上一个目标的', async () => {
    await mountDeleteDialog(impact({ users: 3 }), {
      isRefreshing: true,
      target: '黄金档',
    })
    assert.ok(screenText().includes(copy('qy_ugr_gate_refreshing')))
  })

  test('一切就绪时不留残句 —— 常驻的红字会让真正的闸门失去信号', async () => {
    await mountDeleteDialog(impact({ users: 3 }), { target: '黄金档' })
    const text = screenText()
    for (const key of [
      'qy_ugr_gate_blocked',
      'qy_ugr_gate_needs_ack',
      'qy_ugr_gate_needs_target',
      'qy_ugr_gate_refreshing',
    ]) {
      assert.ok(!text.includes(copy(key)), `没有任何闸门时不该显示 ${key}`)
    }
  })
})

/* ── 3. 入口级的两条：必须是文本节点，不是 title ────────────────────── */

describe('入口有话要说时，那句话是屏幕上的字而不是 tooltip', () => {
  const noteKeys = [
    'qy_ug_delete_default_note',
    'qy_ug_delete_not_addressable',
    ...Object.values(QY_UGR_RENAME_ENTRY_BLOCK_KEY),
  ]

  for (const noteKey of noteKeys) {
    test(noteKey, async () => {
      await mount(<QyUgrActionBlockNote noteKey={noteKey} />)
      assert.ok(
        screenText().includes(copy(noteKey)),
        '禁用的按钮在多数浏览器上不派发指针事件，理由必须是常驻文本'
      )
      const note = document.body.querySelector(
        '[data-slot="qy-ugr-block-note"]'
      )
      assert.ok(note != null, '缺少常驻说明这个节点本身')
      assert.equal(
        note.getAttribute('title'),
        null,
        '理由不该退回 title：那正是这次运营点了没反应、也读不到解释的原因'
      )
    })
  }

  /*
    入口上那几句必须**短**。

    它们落在用户分组表最右一列（`max-w-64`）与编辑弹窗右下角，而每一次打开这张
    表都必然有 `default` 那一行。整段渲染时那一格会被撑到七八行高，而这句话对
    运营是"读一次就够"的常识 —— 完整的原因与替代做法在弹窗里，由后端原话给。

    上限取得很宽（一两句话的量级），只挡住"有人又把后端那一整段抄回来"：
    被换掉的那两条旧文案是 120+ 中文字 / 350+ 英文字符。
  */
  test('入口那几句保持短 —— 完整理由属于后端，不在表格里', () => {
    const tooLong = noteKeys.filter(
      (key) => zhBundle[key].length > 60 || enBundle[key].length > 170
    )
    assert.deepEqual(
      tooLong,
      [],
      `以下入口文案长得像一整段说明：${tooLong.join('、')} —— ` +
        '它会把表格行撑高，而且是后端 reason 的一份无人维护的副本'
    )
  })

  test('入口没话说的时候什么都不画', async () => {
    await mount(<QyUgrActionBlockNote noteKey={null} />)
    assert.equal(
      document.body.querySelector('[data-slot="qy-ugr-block-note"]'),
      null
    )
  })
})

/* ── 3.5 查表得到的文案键，两份语言包里都要有 ──────────────────────── */

describe('查表型文案键在 zh / en 里都存在', () => {
  /*
    `i18n-key-coverage` 那条守卫只扫 `t('qy_…')` 这种**字面量**，而下面这三张表
    的值都是先查表再 `t(表[状态])` 取出来的 —— 它们整体落在那条守卫的盲区里。
    漏一个的表现是界面上直接出现裸键 `qy_ugr_next_blocking_plans`，而它只在
    "删不掉"那一刻才看得见。
  */
  const tables: Array<[string, Record<string, string>]> = [
    ['QY_UGR_BLOCK_NEXT_STEP_KEY', QY_UGR_BLOCK_NEXT_STEP_KEY],
    ['QY_UGR_DELETE_GATE_KEY', QY_UGR_DELETE_GATE_KEY],
    ['QY_UGR_RENAME_ENTRY_BLOCK_KEY', QY_UGR_RENAME_ENTRY_BLOCK_KEY],
  ]

  for (const [label, table] of tables) {
    test(label, () => {
      const missing: string[] = []
      for (const key of Object.values(table)) {
        if (!(key in zhBundle)) missing.push(`${key} (zh)`)
        if (!(key in enBundle)) missing.push(`${key} (en)`)
      }
      assert.deepEqual(missing, [], missing.join('、'))
    })
  }
})

/* ── 4. 两个入口真的接上了这个组件（防"写了没接"）──────────────────── */

describe('两个被禁用的入口都接上了常驻说明', () => {
  const entries = [
    join(
      srcDir,
      'features',
      'system-settings',
      'groups',
      'user-groups-section.tsx'
    ),
    join(
      srcDir,
      'features/qy/pages/admin-user-groups/components/user-group-dialog.tsx'
    ),
  ]

  for (const file of entries) {
    test(`${file.split(/[\\/]/).pop()} 渲染 QyUgrActionBlockNote`, () => {
      const source = readFileSync(file, 'utf8')
      assert.ok(
        source.includes('<QyUgrActionBlockNote'),
        '闸门算出来了却没有人渲染它 —— 按钮照样只是变灰'
      )
      assert.ok(
        !/title=\{[^}]*needs_registry/.test(source),
        '理由又退回 title 了：禁用的按钮不派发 hover，这句话永远不会出现'
      )
    })
  }

  /*
    改名入口只接了后端 `renamability()` 两条拒绝分支里的一条。

    另一条是 block 残留（抽奖名单在发布时进了 commit_hash）：那一档的改名按钮
    亮着，运营输完新名字、提交，吃一个 400。唯一的预告写在**删除弹窗**里，
    而他此刻做的是改名。形状与项目方报的缺陷一致，只是换了个入口。

    修法是让编辑弹窗自己拉一次影响面，并把后端原话印在按钮旁边。这里扫源码，
    因为这条接线的失败方式恰恰是"算出来了没人渲染"。
  */
  test('编辑弹窗把后端的改名拒绝理由印出来，而不是等提交后的 400', () => {
    const source = readFileSync(entries[1], 'utf8')
    assert.ok(
      source.includes('qyUgrImpactQuery'),
      '改名入口不拉影响面的话，block 残留那一条永远只能在提交之后才知道'
    )
    assert.ok(
      source.includes('impact.rename_block_reason'),
      '后端原话没有落到屏幕上 —— 运营读到的会是一句前端自己编的话，或者什么都没有'
    )
    assert.ok(
      source.includes('QY_UGR_BLOCK_NEXT_STEP_KEY[impact.rename_block_code]'),
      '只说"为什么不行"、不说"那我该干什么"，正是项目方为此花掉时间的那一段'
    )
  })
})
