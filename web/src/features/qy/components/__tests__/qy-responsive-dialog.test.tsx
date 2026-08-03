/*
 * QyResponsiveDialog —— 桌面居中窗口 / 手机侧边抽屉（需求 1）。
 *
 * # 守什么
 *
 * 项目方原话：「当前的弹窗你的方案是从底部伸出来，缺失了很多信息，你需要让他
 * 居中显示出一个完整的窗口，展示完整的内容，对于手机端的原项目从左边伸出这不
 * 挺好的？」两条规则，两组断言：
 *
 *   1. **行为**：真的挂进 DOM 渲染两遍 —— 宽屏出 `dialog-content`（上游居中
 *      弹窗的 slot），窄屏出 `sheet-content[data-side=left]`。只断言"组件里
 *      写了 side='left'" 是不够的：本仓反复栽在"写对了但那条分支从没被走到"。
 *   2. **口径唯一**：反向扫 `features/qy` 下的源码，除了这个外壳本身，不许有
 *      第二处直接用 `SheetContent` / `DrawerContent`。否则今天统一、明天又
 *      分叉成两种形态，正是这次被投诉的起因。
 *
 * 顺带钉死"底部抽屉"不许回来：`side='bottom'` 与 vaul 的 Drawer 一律不出现。
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
      shell.includes("from '@/components/dialog'"),
      '桌面端应复用上游居中 Dialog，不要另造一套'
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
        <p data-testid='body'>正文</p>
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
    // 正文与底部操作区都要在，"缺失了很多信息"正是被投诉的点。
    assert.ok(document.body.querySelector('[data-testid="body"]'))
    assert.ok(document.body.querySelector('[data-testid="save"]'))
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
    assert.ok(document.body.querySelector('[data-testid="body"]'))
    assert.ok(document.body.querySelector('[data-testid="save"]'))
  })
})
