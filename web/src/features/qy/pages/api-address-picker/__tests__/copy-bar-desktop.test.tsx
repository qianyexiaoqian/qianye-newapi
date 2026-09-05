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
 * 复制条的**桌面卡片形态**（≥768px，一条线路一张卡）。
 *
 * # 守什么
 *
 * 1. **每张卡的两个按钮复制的是自己那条线路**。卡片各自拼 base / withV1，
 *    最容易错的形状是所有卡共用了 active 那一条 —— 界面上每张卡写着不同的
 *    地址，点谁复制出来的都是第一条。
 * 2. **展示上限**。表的总量上限是 100，桌面最多铺 QY_AA_BAR_MAX_CARDS 张，
 *    截掉的条数必须写出来 —— 静默截断读起来像"全都在这了"。
 * 3. **复制失败要现出能手动选中的文本**。桌面形态平时没有输入框，失败时
 *    必须补一个，否则带 /v1 的那一串在界面上根本不存在，用户连抄都没得抄。
 *
 * 移动形态（下拉 + 输入框）在 copy-bar.test.tsx 里测，那边窗宽 375。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import qyEn from '@/i18n/qy/en.json'

const domWindow = new Window({ width: 1280, height: 900 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'Node',
  'Element',
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

const written: string[] = []
let clipboardFails = false
Object.defineProperty(domWindow.navigator, 'clipboard', {
  configurable: true,
  value: {
    writeText: async (text: string) => {
      if (clipboardFails) throw new Error('not allowed')
      written.push(text)
    },
  },
})
Object.defineProperty(domWindow.document, 'execCommand', {
  configurable: true,
  value: () => false,
})

const { act, StrictMode } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: qyEn } } })

const { qyKeys } = await import('../../../lib/query-keys')
const { QyApiAddressCopyBar, QY_AA_BAR_MAX_CARDS } = await import('../copy-bar')
type AddressOption = {
  id: number
  name: string
  remark: string
  url: string
  color?: string
}

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

localStorage.setItem(
  'status',
  JSON.stringify({ server_address: 'https://site.example.com' })
)

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

after(unmount)

async function mount(addresses: AddressOption[]) {
  await unmount()
  written.length = 0
  clipboardFails = false
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(qyKeys.apiAddresses('picker'), addresses)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <StrictMode>
        <QueryClientProvider client={queryClient}>
          <QyApiAddressCopyBar />
        </QueryClientProvider>
      </StrictMode>
    )
  })
  mounted = { container, root }
}

const COPY = qyEn.qy_aa_bar_copy
const COPY_V1 = qyEn.qy_aa_bar_copy_v1

/** 第 index 张卡上文案为 text 的按钮（每张卡恰好一对 复制 / 带 V1 复制）。 */
function cardButton(index: number, text: string): HTMLElement {
  const buttons = [...document.body.querySelectorAll('button')].filter(
    (b) => b.textContent?.trim() === text
  )
  assert.ok(
    buttons.length > index,
    `想按第 ${index + 1} 张卡的「${text}」，但只找到 ${buttons.length} 个`
  )
  return buttons[index] as unknown as HTMLElement
}

const LINES: AddressOption[] = [
  {
    id: 7,
    name: 'Primary',
    remark: '',
    url: 'https://primary.example.com',
    color: 'orange',
  },
  { id: 9, name: 'Overseas', remark: 'CDN', url: 'https://cdn.example.com/' },
]

describe('桌面卡片：每张卡复制自己那条线路', () => {
  test('两张卡各自的 复制 / 带 V1 复制', async () => {
    await mount(LINES)

    assert.ok(
      document.body.textContent?.includes('Primary') &&
        document.body.textContent?.includes('Overseas'),
      '两条线路的名字都该在卡片上'
    )
    assert.equal(
      document.body.querySelector('select'),
      null,
      '桌面形态没有下拉 —— 每条线路已经各自成卡'
    )

    await act(async () => {
      cardButton(0, COPY).click()
    })
    await act(async () => {
      cardButton(1, COPY).click()
    })
    await act(async () => {
      cardButton(1, COPY_V1).click()
    })
    assert.deepEqual(
      written,
      [
        'https://primary.example.com',
        'https://cdn.example.com',
        'https://cdn.example.com/v1',
      ],
      '第二张卡复制出来的必须是第二条线路 —— 所有卡共用第一条是这个形态最容易犯的错'
    )
  })

  test('超过上限只铺前几张，截掉的条数写出来', async () => {
    const many: AddressOption[] = Array.from(
      { length: QY_AA_BAR_MAX_CARDS + 2 },
      (_, i) => ({
        id: i + 1,
        name: `Line ${i + 1}`,
        remark: '',
        url: `https://l${i + 1}.example.com`,
      })
    )
    await mount(many)

    const copyButtons = [...document.body.querySelectorAll('button')].filter(
      (b) => b.textContent?.trim() === COPY
    )
    assert.equal(copyButtons.length, QY_AA_BAR_MAX_CARDS)
    assert.ok(
      document.body.textContent?.includes(
        qyEn.qy_aa_bar_more.replace('{{count}}', '2')
      ),
      '静默截断读起来像"全都在这了"—— 截掉几条必须写出来'
    )
  })

  test('复制失败时现出只读输入框，失败那串被整段选中', async () => {
    await mount(LINES)
    clipboardFails = true

    await act(async () => {
      cardButton(0, COPY_V1).click()
    })

    assert.deepEqual(written, [], '这一轮本来就不该写成功')
    const input = document.body.querySelector(
      'input[readonly]'
    ) as unknown as HTMLInputElement | null
    assert.ok(
      input != null,
      '桌面形态平时没有输入框，复制失败必须补一个，否则那串带 /v1 的地址没处抄'
    )
    assert.equal(input.value, 'https://primary.example.com/v1')
    assert.equal(input.selectionStart, 0)
    assert.equal(input.selectionEnd, input.value.length)
  })
})
