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
 * 首屏信号板：**永远有行，且行里写的是真话**。
 *
 * 两条都只在真实渲染下才暴露：
 *
 *   1. 目录为空（新站、未开放游客预览、请求失败）时不能留一块空板。上一版
 *      落地页在这个位置放的是写死的 curl 演示，永远不空；换成读真实目录之后，
 *      "空目录"就成了一个必须显式兜底的状态。
 *   2. 兜底与实数据的**说明文字必须跟着换**。同一句「以上取自本站模型目录」
 *      挂在三行兜底族名下面就是假话，而这种假话不会有任何东西报错。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ width: 1280, height: 900 })
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
const qyEn = (await import('@/i18n/qy/en.json')).default
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: qyEn } } })

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { QyLandingSignalBoard } = await import('../signal-board')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

after(async () => {
  for (const { container, root } of mounted) {
    await act(async () => root.unmount())
    container.remove()
  }
})

/**
 * 预置 `['pricing']` 的缓存再挂载 —— 与定价页共用同一个 queryKey，
 * 因此这里连网络边界都不必 mock：组件拿到的就是缓存里这份。
 *
 * 传 `null` 表示"目录取不到"（未开放游客预览 / 请求失败）：把查询关掉，
 * 组件看到的 `data` 就是 undefined，与那两种情况下拿到的完全一样，
 * 而且这一轮不会有任何异步状态更新落在断言之后。
 */
async function render(models: string[] | null): Promise<HTMLElement> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, enabled: models !== null } },
  })
  if (models) {
    queryClient.setQueryData(['pricing'], {
      success: true,
      data: models.map((model_name, id) => ({ id, model_name })),
      vendors: [],
      group_ratio: {},
      usable_group: {},
      supported_endpoint: {},
      auto_groups: [],
    })
  }
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <QyLandingSignalBoard />
      </QueryClientProvider>
    )
  })
  mounted.push({ container, root })
  return container
}

const rowNames = (container: HTMLElement) =>
  [...container.querySelectorAll('li')].map(
    (li) => li.children[1]?.textContent ?? ''
  )

describe('首屏信号板', () => {
  test('目录有数据时列出真实模型名，主推族排在前面', async () => {
    const container = await render([
      'deepseek-v3',
      'gpt-5.1',
      'claude-sonnet-4-5',
    ])
    assert.deepEqual(rowNames(container), [
      'claude-sonnet-4-5',
      'gpt-5.1',
      'deepseek-v3',
    ])
    assert.match(
      container.textContent ?? '',
      /Read live from this site's model catalog/
    )
  })

  test('目录取不到时落到三支主推族，板子不空', async () => {
    const container = await render(null)
    assert.deepEqual(rowNames(container), ['Claude', 'Gemini', 'GPT'])
  })

  test('目录为空数组时同样走兜底，且说明文字换成兜底那句', async () => {
    const container = await render([])
    assert.deepEqual(rowNames(container), ['Claude', 'Gemini', 'GPT'])
    assert.match(
      container.textContent ?? '',
      /Three headline families at official fidelity/
    )
    assert.doesNotMatch(container.textContent ?? '', /Read live from this site/)
  })

  test('行数封顶，不会把首屏撑出一屏之外', async () => {
    const many = Array.from({ length: 20 }, (_, i) => `model-${i}`)
    const container = await render(many)
    assert.equal(rowNames(container).length, 7)
  })
})
