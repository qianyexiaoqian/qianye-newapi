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
 * QyResponsiveDialog —— 桌面居中窗口 / 手机侧边抽屉，且「头尾固定、只有中间滚」。
 *
 * # 守什么
 *
 * 项目方两轮反馈，合起来是三条规则：
 *
 *   1. 「让他居中显示出一个完整的窗口……对于手机端的原项目从左边伸出这不挺好
 *      的？」→ 宽屏出 `dialog-content`，窄屏出 `sheet-content[data-side=left]`。
 *   2. 「很多内容都想显示不出来不完全」（配图：编辑规则弹窗已经居中，但整体高过
 *      视口，动作 / 扣费 / 作用域被裁在屏幕外）→ 窗口 `flex flex-col` + 高度上限，
 *      正文 `flex-1 min-h-0 overflow-y-auto` 吃掉剩余空间，头尾 `shrink-0`
 *      不参与滚动。三者缺一：少 `max-h` 窗口顶破视口；少 `min-h-0` 正文的最小
 *      高度等于内容高度、`overflow-y-auto` 永不触发，一样顶破；少 `flex-1`
 *      正文不吃剩余空间，滚动区高度失控。所以三条各有一条断言。
 *   3. 口径唯一 → 反向扫 `features/qy`，除了这个外壳，不许有第二处直接开浮层
 *      （`SheetContent` / `DrawerContent` / 上游成品 `components/dialog`）。
 *      上游那个成品壳的正文写死 `max-h-[calc(100vh-14rem)]`（猜死头尾高度），
 *      正是第 2 条被投诉的裁切来源；qy 不改全站共用的它，而是全部改走本外壳。
 *
 * happy-dom 不做布局计算，`offsetHeight` 恒为 0，所以第 2 条只能断言到
 * 「哪个元素带着哪几个布局类」+「头尾在不在滚动区里面」这一层 —— 这里 CSS 本身
 * 就是行为，把类删掉功能就没了，下面的变异实验逐条验证过会变红。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

const qyDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..')

/* ── 1. 口径唯一（纯源码扫描，不需要 DOM）────────────────────────────── */

function collectSources(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      if (entry !== '__tests__' && entry !== 'node_modules') {
        collectSources(full, out)
      }
      continue
    }
    if (/\.tsx?$/.test(entry)) out.push(full)
  }
  return out
}

const SHELL = join(qyDir, 'components', 'qy-responsive-dialog.tsx')
const qySources = collectSources(qyDir).filter((f) => f !== SHELL)

describe('qy 浮层口径唯一', () => {
  test('除了外壳，qy 下没有第二处直接用 SheetContent / DrawerContent', () => {
    const offenders = qySources
      .filter((f) =>
        /\b(SheetContent|DrawerContent)\b/.test(readFileSync(f, 'utf8'))
      )
      .map((f) => relative(qyDir, f))
    assert.deepEqual(
      offenders,
      [],
      `这些文件绕过了 QyResponsiveDialog 自己开浮层，形态会与其余的对不上：${offenders.join(', ')}`
    )
  })

  test('除了外壳，qy 下没有第二处直接用上游成品 Dialog 壳', () => {
    const offenders = qySources
      .filter((f) =>
        /from '@\/components\/(dialog|ui\/dialog|ui\/sheet|ui\/drawer)'/.test(
          readFileSync(f, 'utf8')
        )
      )
      .map((f) => relative(qyDir, f))
    assert.deepEqual(
      offenders,
      [],
      `上游成品壳的正文用 max-h-[calc(100vh-14rem)] 猜死头尾高度，正是"内容显示不全"的来源；` +
        `这些文件必须改走 QyResponsiveDialog：${offenders.join(', ')}`
    )
  })

  test('外壳自己不使用底部抽屉', () => {
    const shell = readFileSync(SHELL, 'utf8')
    assert.ok(!shell.includes("side='bottom'"), '底部抽屉正是被投诉的那种形态')
    assert.ok(
      !shell.includes('ui/drawer'),
      'vaul 的 Drawer 默认从底部伸出，不要在这里引入'
    )
    // 两条分支都必须在源码里：删掉任一条，下面的行为断言会红，这条给出更直接的信息。
    assert.ok(shell.includes("side='left'"), '手机端的侧边抽屉方向丢了')
    assert.ok(
      shell.includes("from '@/components/ui/dialog'"),
      '桌面端应拼上游 Dialog 原子件，不要另造一套 base-ui 封装'
    )
  })
})

/* ── 2. 行为：同一份 props，两种视口渲染出两种浮层 ──────────────────── */

const domWindow = new Window({ width: 1280, height: 900 })
const domGlobals = [
  'window',
  'document',
  'navigator',
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
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const { QyResponsiveDialog } = await import('../qy-responsive-dialog')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

/** 把视口调到指定宽度：`useIsMobile` 读的是 `innerWidth` + `matchMedia`。 */
async function setViewport(width: number) {
  await act(async () => {
    domWindow.happyDOM.setViewport({ width })
  })
}

/**
 * 每次挂载前先卸载上一个：视口一改，还挂着的那个会在 act 之外更新，
 * React 会打印 act 警告 —— 那条警告本身就说明测试与组件的更新时序对不上。
 *
 * 正文刻意塞一段很长的内容：被投诉的场景就是"字段多到一屏放不下"。
 */
async function mountDialog() {
  await unmountAll()
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QyResponsiveDialog
        open
        onOpenChange={() => {}}
        title='标题'
        description='说明'
        footer={
          <button type='button' data-testid='save'>
            保存
          </button>
        }
      >
        <div data-testid='body'>
          {Array.from({ length: 40 }, (_, i) => (
            <p key={i}>字段 {i}</p>
          ))}
        </div>
      </QyResponsiveDialog>
    )
  })
  roots.push({ container, root })
}

async function unmountAll() {
  for (;;) {
    const mounted = roots.pop()
    if (mounted == null) return
    await act(async () => mounted.root.unmount())
    mounted.container.remove()
  }
}

after(unmountAll)

function requireEl(selector: string): Element {
  const el = document.body.querySelector(selector)
  assert.ok(el != null, `找不到 ${selector}`)
  return el
}

function classes(el: Element): Set<string> {
  return new Set((el.getAttribute('class') ?? '').split(/\s+/).filter(Boolean))
}

/** 断言"这一段布局规则"在某个浮层壳上成立：头尾固定、正文吃剩余空间且自己滚。 */
function assertThreePartLayout(shellSelector: string) {
  const shell = requireEl(shellSelector)
  const shellClasses = classes(shell)
  assert.ok(
    shellClasses.has('flex') && shellClasses.has('flex-col'),
    `${shellSelector} 必须是纵向 flex 容器，否则 flex-1 / shrink-0 全部失效`
  )
  assert.ok(
    shellClasses.has('overflow-hidden'),
    `${shellSelector} 自己不能滚：滚动条只应该长在正文上`
  )

  const body = requireEl('[data-slot="qy-dialog-body"]')
  const bodyClasses = classes(body)
  for (const token of ['flex-1', 'min-h-0', 'overflow-y-auto']) {
    assert.ok(
      bodyClasses.has(token),
      `正文缺少 ${token}：内容一多就会把窗口顶出视口而不是自己滚`
    )
  }

  // 头尾必须是壳的直接子节点，且都不在滚动区里面 —— 否则用户滚到底才发现
  // "保存"还在更下面，正是这次被投诉的那种"找不到按钮"。
  for (const [name, selector] of [
    ['头部', `${shellSelector} > [data-slot$="-header"]`],
    ['底部', `${shellSelector} > [data-slot$="-footer"]`],
  ] as const) {
    const el = document.body.querySelector(selector)
    assert.ok(el != null, `${name}必须是 ${shellSelector} 的直接子节点`)
    assert.ok(
      classes(el).has('shrink-0'),
      `${name}缺少 shrink-0：内容一多它会被压扁甚至压没`
    )
    assert.ok(!body.contains(el), `${name}不该被卷进正文的滚动区`)
  }

  assert.ok(document.body.querySelector('[data-testid="body"]'))
  assert.ok(document.body.querySelector('[data-testid="save"]'))
}

describe('QyResponsiveDialog 的两种形态', () => {
  test('宽屏：居中的完整窗口，不是贴边抽屉', async () => {
    await setViewport(1280)
    await mountDialog()
    assert.equal(
      document.body.querySelectorAll('[data-slot="dialog-content"]').length,
      1,
      '桌面端应渲染上游的居中 Dialog'
    )
    assert.equal(
      document.body.querySelectorAll('[data-slot="sheet-content"]').length,
      0,
      '桌面端不该出现侧边抽屉'
    )
    // 居中：base-ui 的 popup 靠 top/left 50% + translate 定位。
    const content = requireEl('[data-slot="dialog-content"]')
    const contentClasses = classes(content)
    for (const token of [
      'fixed',
      'top-1/2',
      'left-1/2',
      '-translate-x-1/2',
      '-translate-y-1/2',
    ]) {
      assert.ok(contentClasses.has(token), `居中定位缺少 ${token}`)
    }
    assert.ok(
      [...contentClasses].some((c) => c.startsWith('max-h-')),
      '窗口必须有高度上限，否则内容一多就整体超出视口（截图里被裁掉的正是这种）'
    )
  })

  test('宽屏：头尾固定、只有正文滚', async () => {
    await setViewport(1280)
    await mountDialog()
    assertThreePartLayout('[data-slot="dialog-content"]')
  })

  test('窄屏：从侧边伸出，且方向是左（与上游移动端侧栏一致，不是底部）', async () => {
    await setViewport(390)
    await mountDialog()
    const sheet = document.body.querySelector('[data-slot="sheet-content"]')
    assert.ok(sheet, '手机端应渲染侧边抽屉')
    assert.equal(sheet?.getAttribute('data-side'), 'left')
    assert.equal(
      document.body.querySelectorAll('[data-slot="dialog-content"]').length,
      0,
      '手机端不该同时渲染居中弹窗'
    )
  })

  test('窄屏：抽屉里同样是头尾固定、只有正文滚', async () => {
    await setViewport(390)
    await mountDialog()
    assertThreePartLayout('[data-slot="sheet-content"]')
  })
})
