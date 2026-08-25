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
 * 额度输入框：**外面改了值，框里就要跟着变**。
 *
 * # 这条为什么值得单独钉
 *
 * 建活动向导本轮新增的每一颗「自动填」按钮走的都是同一条路：算出推荐值 →
 * `onChange(patch)` → 父组件 setState → 新值以 `props.value` 回到这个框。
 * 而这个框的文本原本只在挂载那一次从 `props.value` 取过一回，此后外部再怎么
 * 改都不回灌 —— 表现是按钮按下去像没反应：换算读数变了、红字消失了，唯独框
 * 还是空的，运营的下一个动作是往那个空框里手打一遍。
 *
 * 同一条根因还有第二种形态：提交成功后 `reset({ quota: 0 })` 的表单（划转、
 * 提现）框里留着上一笔的旧数字，而实际要提交的是 0 —— 屏幕上的钱不等于要动
 * 的钱。
 *
 * 所以断言落在 `input.value` 这个**用户真正看见的字节**上，而不是回调参数。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window()
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'HTMLElement',
  'HTMLInputElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const React = await import('react')
const { act, useState } = React
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const { QyAmountInput } = await import('../qy-amount-input')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

after(async () => {
  for (const { container, root } of roots) {
    await act(async () => root.unmount())
    container.remove()
  }
})

/**
 * 挂一个"外面能改值"的宿主：它就是向导里那个 draft，`push` 模拟一次自动填。
 */
async function mountHost(initial: number): Promise<{
  input: HTMLInputElement
  push: (quota: number) => Promise<void>
  current: () => number
}> {
  let push!: (quota: number) => Promise<void>
  let latest = initial

  function Host() {
    const [quota, setQuota] = useState(initial)
    latest = quota
    push = async (next: number) => {
      await act(async () => setQuota(next))
    }
    return React.createElement(QyAmountInput, {
      value: quota,
      onChange: setQuota,
    })
  }

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(React.createElement(Host))
  })
  roots.push({ container, root })
  const input = container.querySelector('input')
  assert.ok(input, '输入框没渲染出来')
  return {
    input: input as unknown as HTMLInputElement,
    push,
    current: () => latest,
  }
}

/** 模拟一次真实键入：happy-dom 下要绕开 React 的 value setter 缓存。 */
async function typeInto(input: HTMLInputElement, text: string) {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(
      domWindow.HTMLInputElement.prototype,
      'value'
    )?.set
    setter?.call(input, text)
    input.dispatchEvent(
      new domWindow.Event('input', { bubbles: true }) as unknown as Event
    )
  })
}

describe('额度输入框与外部值的同步', () => {
  test('自动填：外面写进一个值，框里必须显示它', async () => {
    const host = await mountHost(0)
    assert.equal(host.input.value, '', '初始应当是空的')

    // ⌈50000 / 3⌉ = 16667 额度，正是「奖档单份额度」那颗自动填算出来的数。
    await host.push(16_667)
    assert.equal(host.current(), 16_667, '草稿里必须是这个数')
    assert.equal(
      host.input.value,
      '0.033334',
      '框里必须显示自动填进来的那个数 —— 空框会让运营以为按钮坏了'
    )
  })

  test('清零：提交成功后表单被重置，框里不许留着上一笔的旧数字', async () => {
    const host = await mountHost(25_000_000)
    assert.equal(host.input.value, '50')

    await host.push(0)
    assert.equal(
      host.input.value,
      '',
      '框里留着 50 而实际要提交 0，屏幕上的钱就不等于要动的钱'
    )
  })

  test('键入到一半不许被回灌打断', async () => {
    const host = await mountHost(0)

    // "1." 与 "0.0" 都不是合法数字，但它们是键入过程中必然出现的中间态。
    // 回灌判据看的是"解析回去还等不等于当前值"，所以这两步都不该动文本。
    await typeInto(host.input, '1.')
    assert.equal(host.input.value, '1.', '小数点被吃掉会让人没法输入小数')
    assert.equal(host.current(), 500_000)

    await typeInto(host.input, '1.5')
    assert.equal(host.input.value, '1.5')
    assert.equal(host.current(), 750_000)

    // 用户自己清空：框留空，值归零，不许被反格式化成 "0"。
    await typeInto(host.input, '')
    assert.equal(host.input.value, '')
    assert.equal(host.current(), 0)
  })
})
