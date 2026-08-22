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
 * 同步自上游 137d1171f 的 PlaygroundMessageEditor 回归。
 *
 * ── 为什么这份必须补上 ──
 *
 * 上游那个提交把这个组件的取消交互整段换掉了：window.confirm(同步阻塞)换成
 * ConfirmDialog、新增 beforeunload 守卫、Escape 在弹窗打开时不再触发取消。
 * 本仓把 .tsx **逐字节取入**，却没有取随行的这份测试，也没有把它记进
 * upstream-provenance.md 的待办 —— 于是一段刚被整体替换的交互逻辑在本仓
 * 零覆盖。（同一提交里另一份 response-fade-render.test.tsx 倒是被如实记下了，
 * 沉默是不对称的。）
 *
 * ── 载体为什么换 ──
 *
 * 上游那份用 @testing-library/react + vitest，本仓两样都没装。本仓的 48 个
 * React 用例统一手搭 happy-dom + createRoot（见 code-block-editor.test.tsx，
 * 同一提交里另一份被改写落地的先例）。所以**判据逐条照搬、载体换成本仓的写法**。
 *
 * 上游 5 条判据，这里 5 条：
 *   ① 没有未保存改动时，取消立刻生效，不弹任何东西；
 *   ② 有未保存改动 → 弹确认 → 点「Leave」才真的退出；
 *   ③ 点「Stay」则留在编辑器里，onCancelEdit 一次都不许被调用；
 *   ④ 有未保存改动时 beforeunload 必须被 preventDefault（关标签页要拦一下）；
 *   ⑤ 改回原文之后不再拦 —— 这一条同样要紧：一个永远拦着的守卫会让用户
 *      每次关页面都吃一个莫名其妙的弹窗，然后学会无脑点确认。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ width: 1280, height: 800 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLButtonElement',
  'HTMLDivElement',
  'HTMLInputElement',
  'SVGElement',
  'Node',
  'Element',
  'Text',
  'Range',
  'Event',
  'CustomEvent',
  'KeyboardEvent',
  'MouseEvent',
  'PointerEvent',
  'MutationObserver',
  'ResizeObserver',
  'IntersectionObserver',
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
const upstreamEn = (await import('@/i18n/locales/en.json')).default

await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: upstreamEn } } })

const { PlaygroundMessageEditor } = await import('../playground-message-editor')
type Message = import('../../../types').Message

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const LEAVE_PROMPT = 'You have unsaved changes. Are you sure you want to leave?'

const userMessage: Message = {
  key: 'msg-1',
  from: 'user',
  versions: [{ id: 'v1', content: 'original' }],
} as Message

let mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
} | null = null

async function unmount() {
  if (mounted == null) return
  const current = mounted
  mounted = null
  await act(async () => current.root.unmount())
  current.container.remove()
}

after(async () => {
  await unmount()
  domWindow.close()
})

async function mountEditor(options: {
  editText: string
  onCancelEdit?: (open: boolean) => void
}) {
  await unmount()
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <PlaygroundMessageEditor
        editText={options.editText}
        message={userMessage}
        onCancelEdit={options.onCancelEdit}
        onEditTextChange={() => undefined}
        originalText='original'
      />
    )
  })
  mounted = { container, root }
  return {
    container,
    async rerender(editText: string) {
      await act(async () => {
        root.render(
          <PlaygroundMessageEditor
            editText={editText}
            message={userMessage}
            onEditTextChange={() => undefined}
            originalText='original'
          />
        )
      })
    },
  }
}

/*
 * 按无障碍名找按钮，与上游 `getByRole('button', { name })` 同一口径。
 *
 * 编辑器上那三个是**图标按钮**（名字只在 aria-label 上，textContent 是空的），
 * 而弹窗里的 Stay / Leave 是文字按钮 —— 两者都要能找到，所以两处都看。
 * 只看 textContent 会找不到取消按钮；只看 aria-label 会找不到弹窗按钮。
 *
 * 弹窗渲染在 portal 里，所以从 document 找而不是从 container 找。
 */
function buttonByText(label: string): HTMLButtonElement | undefined {
  return [...document.querySelectorAll<HTMLButtonElement>('button')].find(
    (button) =>
      button.getAttribute('aria-label') === label ||
      button.textContent?.trim() === label
  )
}

function leavePromptShown(): boolean {
  return (document.body.textContent ?? '').includes(LEAVE_PROMPT)
}

describe('PlaygroundMessageEditor 的离开确认', () => {
  test('没有未保存改动时，取消立刻生效且不弹任何东西', async () => {
    const calls: boolean[] = []
    const { container } = await mountEditor({
      editText: 'original',
      onCancelEdit: (open) => calls.push(open),
    })
    assert.ok(container.textContent != null)

    const cancel = buttonByText('Cancel')
    assert.ok(cancel, '找不到取消按钮')
    await act(async () => cancel.click())

    assert.deepEqual(calls, [false], '没改过就取消，应当直接退出编辑')
    assert.equal(leavePromptShown(), false, '没有改动就不该拦人')
  })

  test('有未保存改动时先弹确认，点 Leave 才真的退出', async () => {
    const calls: boolean[] = []
    await mountEditor({
      editText: 'changed',
      onCancelEdit: (open) => calls.push(open),
    })

    const cancel = buttonByText('Cancel')
    assert.ok(cancel)
    await act(async () => cancel.click())
    assert.deepEqual(calls, [], '弹窗还没确认，绝不能已经退出编辑')
    assert.equal(leavePromptShown(), true, '有未保存改动必须先问一句')

    const leave = buttonByText('Leave')
    assert.ok(leave, '确认框里没有「Leave」')
    await act(async () => leave.click())
    assert.deepEqual(calls, [false], '确认之后才退出')
  })

  test('点 Stay 留在编辑器里，onCancelEdit 一次都不许被调用', async () => {
    const calls: boolean[] = []
    await mountEditor({
      editText: 'changed',
      onCancelEdit: (open) => calls.push(open),
    })

    const cancel = buttonByText('Cancel')
    assert.ok(cancel)
    await act(async () => cancel.click())

    const stay = buttonByText('Stay')
    assert.ok(stay, '确认框里没有「Stay」')
    await act(async () => stay.click())

    assert.deepEqual(calls, [], '选了留下却把编辑关掉，就是直接丢稿')
    assert.equal(leavePromptShown(), false, '选了留下，弹窗要收起来')
  })
})

describe('PlaygroundMessageEditor 的 beforeunload 守卫', () => {
  test('有未保存改动时拦住关闭页面', async () => {
    await mountEditor({ editText: 'changed' })

    const event = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(event)

    assert.equal(
      event.defaultPrevented,
      true,
      '未保存的草稿在关标签页时必须拦一下'
    )
  })

  test('改回原文之后不再拦', async () => {
    const view = await mountEditor({ editText: 'changed' })
    await view.rerender('original')

    const event = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(event)

    assert.equal(
      event.defaultPrevented,
      false,
      '永远拦着的守卫会让用户每次关页面都吃一个莫名其妙的弹窗，' +
        '然后学会无脑点确认 —— 那时真正该拦的那一次也拦不住了'
    )
  })
})
