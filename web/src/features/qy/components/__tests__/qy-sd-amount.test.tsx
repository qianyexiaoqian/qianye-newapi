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
 * 星屑金额展示件的契约：**整数 + 千分位 + 运营配的单位名，绝不走额度换算**。
 *
 * 它与 `qy-amount-text.test.tsx` 是两套口径，而且必须是两套：那一件把
 * 137200 印成 `$0.2744`，这一件把 137200 印成 `137,200 星屑`。抽奖 / 商城上凡是钱
 * 都换成了这一件，任何一处误接回 `QyAmountText`，用户看到的就是一个与他账上
 * 那个整数对不上的美元数。
 *
 * 单位名来自引导端点（运营可改），缺配时回落到 i18n 默认词 —— 两条路都要钉：
 * 只钉前者，把回落删掉之后界面上会出现「1,234 」这样一个没有单位的数。
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: { qy_sd_unit_default: 'Stardust' } } },
})
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { normalizeQyConfig, qyConfigQueryOptions } =
  await import('../../lib/config-query')
const { QySdAmount } = await import('../qy-sd-amount')

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

/** 挂在一份预置了引导端点响应的 QueryClient 下，`name` 就是运营配的货币名。 */
async function render(
  node: React.ReactNode,
  name: string
): Promise<HTMLElement> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(
    qyConfigQueryOptions().queryKey,
    normalizeQyConfig({
      enabled: true,
      available: true,
      features: { stardust: true },
      stardust: { show_entry: true, name },
    })
  )
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>{node}</QueryClientProvider>
    )
  })
  roots.push({ container, root })
  return container
}

describe('QySdAmount 的展示口径', () => {
  test('整数千分位 + 运营配的单位名，不做任何换算', async () => {
    const container = await render(<QySdAmount amount={137_200} />, '星尘')
    assert.equal(container.textContent, '137,200 星尘')
  })

  test('运营没配单位名时回落到 i18n 默认词', async () => {
    const container = await render(<QySdAmount amount={5} />, '')
    assert.equal(container.textContent, '5 Stardust')
  })

  test('取不到的值显示短横，不带单位', async () => {
    const container = await render(<QySdAmount amount={null} />, '星尘')
    assert.equal(container.textContent, '-')
  })

  test('负数带 destructive 着色，正数不带', async () => {
    const negative = await render(<QySdAmount amount={-1500} />, '星尘')
    const positive = await render(<QySdAmount amount={1500} />, '星尘')
    assert.equal(negative.textContent, '-1,500 星尘')
    assert.match(
      negative.querySelector('span')?.className ?? '',
      /text-destructive/
    )
    assert.doesNotMatch(
      positive.querySelector('span')?.className ?? '',
      /text-destructive/
    )
  })

  test('signed 打开时正数带 +，零与负数不带', async () => {
    const plus = await render(<QySdAmount amount={1500} signed />, '星尘')
    const zero = await render(<QySdAmount amount={0} signed />, '星尘')
    const minus = await render(<QySdAmount amount={-1500} signed />, '星尘')
    assert.equal(plus.textContent, '+1,500 星尘')
    assert.equal(zero.textContent, '0 星尘')
    assert.equal(minus.textContent, '-1,500 星尘')
  })
})
