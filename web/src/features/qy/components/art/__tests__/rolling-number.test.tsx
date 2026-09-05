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
 * 数字"到账"滚动的三条契约:
 *
 *   1. 首次挂载不动 —— 进页面时大数字"从下面滚上来"是把每一次打开都演成一次到账;
 *   2. 值变了才叠一层 aria-hidden 的旧值滚出去,屏幕上的**真实文字永远是新值**;
 *      动画结束(animationend)那一层撤掉,没有定时器;
 *   3. prefers-reduced-motion 下不叠旧值层,新值直接就位。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ height: 900, width: 1280 })
for (const key of [
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
  'DOMRect',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

/** 用例自己拨这个开关:同一份组件在两档偏好下各走一遍。 */
let reducedMotion = false
Object.defineProperty(globalThis, 'matchMedia', {
  configurable: true,
  value: (queryText: string) => ({
    matches: queryText.includes('prefers-reduced-motion') && reducedMotion,
    media: queryText,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false,
  }),
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QyRollingNumber } = await import('../qy-rolling-number')

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

const render = (value: number) => <b>{value}</b>

async function mount(value: number) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  roots.push({ container, root })
  const update = async (next: number) => {
    await act(async () => {
      root.render(<QyRollingNumber value={next} render={render} />)
    })
  }
  await update(value)
  return { container, update }
}

const rolling = (container: HTMLElement) =>
  container.querySelector('[data-rolling="true"]')

describe('QyRollingNumber', () => {
  test('首次挂载不滚;值变了才叠旧值层,新值是屏幕上唯一可见的文字', async () => {
    reducedMotion = false
    const { container, update } = await mount(1_234)
    assert.equal(rolling(container), null, '首次挂载不该有滚动')
    assert.equal(container.textContent, '1234')

    await update(1_734)
    assert.ok(rolling(container) != null, '值变了应当进入滚动态')
    const ghost = container.querySelector('[aria-hidden="true"]')
    assert.equal(ghost?.textContent, '1234', '旧值只在 aria-hidden 那一层里')
    const visible = [...container.querySelectorAll('b')].filter(
      (node) => node.closest('[aria-hidden="true"]') == null
    )
    assert.deepEqual(
      visible.map((node) => node.textContent),
      ['1734'],
      '读屏与页内搜索拿到的必须只有新值'
    )

    // 动画结束由事件宣告,不是定时器。
    await act(async () => {
      const fresh = visible[0].parentElement
      fresh?.dispatchEvent(new Event('animationend', { bubbles: true }))
    })
    assert.equal(rolling(container), null, 'animationend 之后旧值层要撤掉')
    assert.equal(container.textContent, '1734')
  })

  test('缩减动效下值变了也不叠旧值层,新值直接就位', async () => {
    reducedMotion = true
    const { container, update } = await mount(10)
    await update(20)
    assert.equal(rolling(container), null)
    assert.equal(container.querySelector('[aria-hidden="true"]'), null)
    assert.equal(container.textContent, '20')
  })
})
