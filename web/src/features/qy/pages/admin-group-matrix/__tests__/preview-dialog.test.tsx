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
 * 影响面报告是**一层可开可关的浮层**，且块 A 与块 B 永不合并。
 *
 * # 守什么
 *
 * 项目方原话：「这个预览面改成第二个弹窗显示吧，都在一个页面显得太乱了。」
 * 编辑用户分组那个弹窗此前把整份报告就地摊开在差异条与模型分组清单之间，
 * 于是运营真正要动的那张表被挤到一屏之外。改法是让报告只以这一个浮层出现，
 * 两个入口（整页矩阵 / 编辑弹窗）共用它。
 *
 * 「只以浮层出现」这件事一半由类型系统兜着（正文不再导出，没人能把它内联进
 * 第三处），另一半是下面第 1 组：`open` 为 false 时整屏一个字都没有。
 * 内联回去的表现不是报错，是"页面又乱了"—— 没有任何测试会因此变红。
 *
 * 第 2 组守的是"点了预览之后到底在干什么"。窗是在**发请求之前**开的，那句
 * 「正在统计影响面…」是这次点击唯一的回声；它若和统计块同屏出现，运营就会
 * 对着一份还没算完、或者上一轮留下的报告做决定。
 *
 * 第 3 组守的是这一屏唯一的设计要求（见 `preview-dialog.tsx` 的说明）：
 * 本次新造出来的断链与本来就已经断的**分开合计**。本站块 B 有几百条历史欠账，
 * 块 A 可能只有几条 —— 合并之后运营看到一个巨大的数字，判断「反正一直都这样」，
 * 然后照按不误。
 *
 * 文案走真实的 `src/i18n/qy/zh.json`：键写错时 i18next 原样吐键名，中文断言
 * 当场变红。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import type { QyGmImpactPair, QyGmPreviewResponse } from '../types'

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

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhBundle } },
})

const { QyGmPreviewDialog } = await import('../components/preview-dialog')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/* ── 夹具 ──────────────────────────────────────────────────────────── */

function pair(modelGroup: string): QyGmImpactPair {
  return {
    user_group: 'qy-canary-ug',
    model_group: modelGroup,
    user_count: 2,
    token_count: 5,
    token_enabled_count: 5,
    token_active_30d: 1,
    samples: [],
    samples_truncated: false,
  }
}

/** 块 A 一条、块 B 三条 —— 合并之后是 4，而 4 不是任何人要负责的数字。 */
const PREVIEW: QyGmPreviewResponse = {
  draft_hash: 'd',
  impact_hash: 'i',
  base_ratio_hash: 'h',
  preview_incomplete: false,
  approximate_user_group: false,
  log_days: 7,
  newly_broken: [pair('pool-a')],
  already_broken: [pair('pool-b'), pair('pool-c'), pair('pool-d')],
  newly_allowed: [],
  self_excluded: [],
  case_near_miss: [],
  orphan_group_names: [],
  empty_group_tokens: 0,
  auto_groups_shrink: 0,
  total_newly_broken_tokens: 5,
  total_already_broken_tokens: 15,
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

async function mountDialog(options: {
  open: boolean
  preview: QyGmPreviewResponse | null
  isLoading: boolean
}): Promise<{ openChanges: boolean[] }> {
  await unmountAll()
  const log = { openChanges: [] as boolean[] }

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QyGmPreviewDialog
        open={options.open}
        onOpenChange={(open) => log.openChanges.push(open)}
        preview={options.preview}
        isLoading={options.isLoading}
      />
    )
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  mounted.push({ container, root })
  return log
}

/* ── 查询 ──────────────────────────────────────────────────────────── */

function copy(key: string): string {
  const value = zhBundle[key]
  assert.ok(
    value != null && value !== '',
    `文案键 ${key} 没有登记进 src/i18n/qy/zh.json`
  )
  return value
}

function screenText(): string {
  return document.body.textContent ?? ''
}

/** 一块统计的标题行文本（标题 + 徽章里的计数）。找不到就当场失败。 */
function blockHeading(key: string): string {
  const title = copy(key)
  const found = [...document.body.querySelectorAll('h3')].filter((node) =>
    (node.textContent ?? '').includes(title)
  )
  assert.equal(found.length, 1, `统计块「${title}」应当恰好有一块`)
  return found[0].textContent ?? ''
}

/* ── 1. 关着的时候是一层浮层，不是内联的一段 ───────────────────────── */

describe('报告只在浮层里出现', () => {
  test('open 为 false 时屏幕上一个字都没有', async () => {
    await mountDialog({ open: false, preview: PREVIEW, isLoading: false })

    assert.equal(
      screenText(),
      '',
      '报告一旦内联进编辑那一屏，运营要改的那张表就被挤到一屏之外'
    )
  })

  test('open 为 true 时标题与说明都在', async () => {
    await mountDialog({ open: true, preview: PREVIEW, isLoading: false })

    const text = screenText()
    assert.ok(text.includes(copy('qy_group_matrix_preview_title')))
    assert.ok(text.includes(copy('qy_group_matrix_preview_desc')))
  })

  test('底部「关闭」把 open 交还给调用方', async () => {
    const log = await mountDialog({
      open: true,
      preview: PREVIEW,
      isLoading: false,
    })

    const close = [...document.body.querySelectorAll('button')].filter(
      (node) => (node.textContent ?? '').trim() === copy('qy_common_close')
    )
    assert.equal(close.length, 1, '底部应当恰好有一个「关闭」')
    await act(async () => close[0].click())

    assert.deepEqual(
      log.openChanges,
      [false],
      '关不掉的报告 = 运营回不到正在编辑的那一屏'
    )
  })
})

/* ── 2. 还在算的时候不许摆出统计块 ─────────────────────────────────── */

describe('统计还没回来时只说一句「正在统计」', () => {
  test('第一次预览：只有那句「正在统计影响面…」', async () => {
    await mountDialog({ open: true, preview: null, isLoading: true })

    const text = screenText()
    assert.ok(text.includes(copy('qy_group_matrix_preview_loading')))
    assert.equal(
      text.includes(copy('qy_group_matrix_preview_newly_broken')),
      false,
      '半张报告会让运营对着还没算完的数字做决定'
    )
  })

  test('改了草稿再预览一次：上一轮的结果不许留在屏幕上', async () => {
    await mountDialog({ open: true, preview: PREVIEW, isLoading: true })

    const text = screenText()
    assert.ok(text.includes(copy('qy_group_matrix_preview_loading')))
    assert.equal(
      text.includes(copy('qy_group_matrix_preview_newly_broken')),
      false,
      '过期报告与当前草稿对不上，画出来就是一份假陈述'
    )
  })
})

/* ── 3. 块 A 与块 B 分开合计，永不合并 ─────────────────────────────── */

describe('本次新造的断链与历史欠账分开报数', () => {
  test('两块各自带自己的计数', async () => {
    await mountDialog({ open: true, preview: PREVIEW, isLoading: false })

    const newly = blockHeading('qy_group_matrix_preview_newly_broken')
    const already = blockHeading('qy_group_matrix_preview_already_broken')

    assert.ok(newly.endsWith('1'), `块 A 应当报 1，实际「${newly}」`)
    assert.ok(already.endsWith('3'), `块 B 应当报 3，实际「${already}」`)
  })

  test('计数为 0 的块也留在屏幕上', async () => {
    await mountDialog({ open: true, preview: PREVIEW, isLoading: false })

    const allowed = blockHeading('qy_group_matrix_preview_newly_allowed')
    assert.ok(allowed.endsWith('0'), `实际「${allowed}」`)
    assert.ok(
      screenText().includes(copy('qy_group_matrix_preview_block_empty')),
      '空块直接消失会让人以为那一项根本没算'
    )
  })
})
