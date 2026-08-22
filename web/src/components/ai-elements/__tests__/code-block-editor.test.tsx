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
// 同步自上游 137d1171f 的 CodeBlockEditor 回归。上游那份用的是
// @testing-library/react + vitest,本仓两样都没有(前端闸门是 node:test,
// React 用例统一手搭 happy-dom + createRoot,见同目录其余用例),
// 所以判据照搬、载体换成本仓的写法。
//
// 判据:父组件每次按键都会重建 onKeyDown 闭包。如果 CodeMirror 的 extensions
// memo 把 onKeyDown 算进依赖,新身份会让 EditorView 被拆掉重建 —— 光标回到
// 文档开头,后续字符堆在最前面,肉眼看上去就是"从右往左打字"。这条测试盯的是
// **同一个 .cm-content 节点在 rerender 后仍然是同一个对象**。
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLButtonElement',
  'HTMLDivElement',
  'SVGElement',
  'Node',
  'Element',
  'Text',
  'Range',
  'Event',
  'CustomEvent',
  'KeyboardEvent',
  'MutationObserver',
  'ResizeObserver',
  'IntersectionObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'DOMRect',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { CodeBlockEditor } = await import('../code-block')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const container = document.createElement('div')
document.body.appendChild(container)
const root = createRoot(container)

after(() => {
  act(() => root.unmount())
})

function editorTree(value: string) {
  // 每次调用都传一个全新的内联 onKeyDown,复刻 PlaygroundMessageEditor:
  // 它在每一次按键驱动的 render 里都会重建这个闭包。
  return (
    <CodeBlockEditor
      ariaLabel='Edit message'
      language='markdown'
      onChange={() => undefined}
      onKeyDown={() => undefined}
      value={value}
    />
  )
}

describe('CodeBlockEditor', () => {
  // 只换 onKeyDown 身份、不动 value:这样失败时是一条干净的
  // strictEqual 断言(约 14s)。同时改 value 的话,重建出来的 EditorView 会在
  // happy-dom 里陷入布局测量空转,60s 都跑不完 —— 那种"红"没法与真挂住区分。
  test('keeps the same editor instance when onKeyDown identity changes on rerender', async () => {
    await act(async () => {
      root.render(editorTree('h'))
    })

    const contentBefore = container.querySelector('.cm-content')
    assert.ok(contentBefore, 'CodeMirror content node must be mounted')

    await act(async () => {
      root.render(editorTree('h'))
    })

    const contentAfter = container.querySelector('.cm-content')
    // EditorView 被拆掉重建时,.cm-content 会换成另一个节点,光标随之回到
    // 文档开头 —— 那正是"打字从右往左"的现象。
    assert.equal(
      contentAfter,
      contentBefore,
      'rerender must not tear down and rebuild the EditorView'
    )
    assert.ok(contentAfter?.textContent?.includes('h'))
  })
})
