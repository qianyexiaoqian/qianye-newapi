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
 * 「填入 CC Switch」这条路走一遍：选线路 → 配置窗口 → 导出的配置里就是那条线路。
 *
 * # 守什么
 *
 * 1. **选中的那条线路真的落进了配置**。这是整件事的目的，也是唯一一个错了却
 *    毫无信号的环节：窗口弹了、线路选了、配置窗口也出来了，只有那串
 *    `ccswitch://` 里的 `endpoint` 悄悄还是站点地址。用户要把它导进客户端、
 *    跑一段时间之后才可能发现。所以这里从点菜单一路点到「打开 CC Switch」，
 *    断言的是最终交给 `window.open` 的那串 URL。
 *
 * 2. **只有一条线路时不弹窗**。与「复制链接信息」同一个口径（见
 *    `useQyApiAddressPicker` 的说明）：一个只有一个选项的窗口不提供任何信息，
 *    只会训练用户闭眼点下一步。两个入口共用一份判定，所以这条也在这里守 ——
 *    哪天有人给 CC Switch 单独加一份"总是弹"的逻辑，这条会红。
 *
 * 3. **一条都没配时不是一堵墙**。合成出站点自身那一条，直接进配置窗口，
 *    也就是这个功能上线之前的行为。
 *
 * 4. **`homepage` 不跟着线路走**。加速线路只保证转发 API，未必伺服 Web 界面；
 *    把它填进 homepage 会给用户一个点开是白页的链接。
 *
 * 组合关系（真实的那个菜单项到底调了谁）由 `cc-switch-wiring.test.ts` 用 AST 守；
 * 这里的 Harness 只是把那份接线原样搭出来，好让一次点击能一路走到 `window.open`。
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

/** CC Switch 是靠 `window.open('ccswitch://…')` 把配置交给客户端的。 */
const opened: string[] = []
Object.defineProperty(domWindow, 'open', {
  configurable: true,
  value: (url?: string) => {
    opened.push(String(url))
    return null
  },
})

const { act, useState } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: qyEn } } })

const { api } = await import('@/lib/api')
const { qyKeys } = await import('../../../lib/query-keys')
const { useQyApiAddressPicker } = await import('..')
const { CCSwitchDialog } = await import(
  '@/features/keys/components/dialogs/cc-switch-dialog'
)
const { buildCCSwitchURL } = await import('@/features/keys/lib/cc-switch-url')
type AddressOption = { id: number; name: string; remark: string; url: string }

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/** 配置窗口的模型下拉走 `/api/user/models`，用真 axios 实例 + 假 adapter 喂。 */
api.defaults.adapter = async (config) => ({
  data: { success: true, data: ['gpt-5', 'claude-4'] },
  status: 200,
  statusText: 'OK',
  headers: {},
  config,
})

const SITE = 'https://site.example.com'
localStorage.setItem('status', JSON.stringify({ server_address: SITE }))

const LINES: AddressOption[] = [
  { id: 7, name: 'Primary', remark: '', url: 'https://primary.example.com' },
  { id: 9, name: 'Overseas', remark: 'CDN', url: 'https://cdn.example.com' },
]

/**
 * 把密钥行里那份接线原样搭出来：菜单项 → `pick(realKey)` → 选完线路把地址与
 * 密钥一起交给配置窗口。真实组件的接线由 AST 守卫钉住，这里只负责能点。
 */
function Harness() {
  const [apiAddress, setApiAddress] = useState('')
  const [tokenKey, setTokenKey] = useState('')
  const [open, setOpen] = useState(false)
  const picker = useQyApiAddressPicker({
    description: 'pick a line',
    confirmLabel: 'Next',
    onPick: (realKey, url) => {
      setTokenKey(realKey)
      setApiAddress(url)
      setOpen(true)
    },
  })
  return (
    <>
      <button
        type='button'
        data-testid='cc-switch-entry'
        onClick={() => picker.pick('sk-real-key')}
      >
        CC Switch
      </button>
      {picker.dialog}
      <CCSwitchDialog
        open={open}
        onOpenChange={setOpen}
        tokenKey={tokenKey}
        apiAddress={apiAddress}
      />
    </>
  )
}

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

/** 挂一次 Harness，地址清单直接写进 react-query 缓存（`pick()` 读的就是它）。 */
async function mount(addresses: AddressOption[] | null) {
  await unmount()
  opened.length = 0
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  if (addresses != null) {
    queryClient.setQueryData(qyKeys.apiAddresses(), addresses)
  }
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <Harness />
      </QueryClientProvider>
    )
  })
  mounted = { container, root }
}

async function click(element: Element | null, what: string) {
  assert.ok(element != null, `找不到${what}`)
  await act(async () => {
    ;(element as HTMLElement).click()
  })
}

function buttonByText(text: string): Element | null {
  return (
    [...document.body.querySelectorAll('button')].find(
      (b) => b.textContent?.trim() === text
    ) ?? null
  )
}

/**
 * 选主模型。下拉是自绘的 input + ul：聚焦展开，再在那一项上按下鼠标
 * （选项挂的是 `onMouseDown`，`click()` 不会触发它）。
 * 第一个「Select or enter model name」就是必填的 Primary Model。
 */
async function chooseModel(model: string) {
  const input = document.body.querySelector(
    'input[placeholder="Select or enter model name"]'
  )
  assert.ok(input != null, '找不到模型输入框')
  await act(async () => {
    ;(input as HTMLInputElement).focus()
  })
  const option = [...document.body.querySelectorAll('li')].find(
    (li) => li.textContent?.trim() === model
  )
  assert.ok(option != null, `下拉里没有模型 ${model}`)
  await act(async () => {
    option.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
  })
}

function pickerIsOpen(): boolean {
  return document.body.querySelector('#qy-aa-7') != null
}

function exportedParams(): URLSearchParams {
  assert.equal(opened.length, 1, `应当只导出一次，实际 ${opened.length} 次`)
  const url = opened[0]
  assert.ok(
    url.startsWith('ccswitch://v1/import?'),
    `导出的不是 CC Switch 导入链接：${url}`
  )
  return new URLSearchParams(url.slice('ccswitch://v1/import?'.length))
}

describe('填入 CC Switch 之前先选线路', () => {
  test('选中的线路就是配置里的接口地址', async () => {
    await mount(LINES)

    await click(
      document.body.querySelector('[data-testid="cc-switch-entry"]'),
      'CC Switch 菜单项'
    )
    assert.ok(
      pickerIsOpen(),
      '有两条线路可选，点「CC Switch」必须先弹出线路选择窗口'
    )
    assert.equal(opened.length, 0, '线路还没选，配置窗口就已经把东西导出去了')

    // 默认选中第一条；这里改选第二条，正是"用户想换一条线路"的那个动作。
    await click(document.body.querySelector('#qy-aa-9'), '第二条线路')
    await click(buttonByText('Next'), '确认按钮')

    assert.ok(!pickerIsOpen(), '选完之后线路窗口应当关掉')
    await chooseModel('claude-4')
    await click(buttonByText('Open CC Switch'), '打开 CC Switch')

    const params = exportedParams()
    assert.equal(
      params.get('endpoint'),
      'https://cdn.example.com',
      '配置里的接口地址不是用户选的那一条'
    )
    assert.equal(params.get('apiKey'), 'sk-real-key')
    assert.equal(params.get('model'), 'claude-4')
    assert.equal(
      params.get('homepage'),
      SITE,
      'homepage 是站点主页，不该跟着 API 线路走'
    )
  })

  test('只有一条线路时不弹窗，直接进配置窗口', async () => {
    await mount([LINES[0]])

    await click(
      document.body.querySelector('[data-testid="cc-switch-entry"]'),
      'CC Switch 菜单项'
    )
    assert.ok(
      !pickerIsOpen(),
      '只有一个选项的选择窗口不提供任何信息，只会训练用户闭眼点下一步'
    )

    await chooseModel('gpt-5')
    await click(buttonByText('Open CC Switch'), '打开 CC Switch')
    assert.equal(exportedParams().get('endpoint'), LINES[0].url)
  })

  test('一条都没配时回落到站点地址，而不是一个空列表', async () => {
    await mount([])

    await click(
      document.body.querySelector('[data-testid="cc-switch-entry"]'),
      'CC Switch 菜单项'
    )
    assert.ok(!pickerIsOpen(), '空列表会把一个本来能用的功能变成一堵墙')

    await chooseModel('gpt-5')
    await click(buttonByText('Open CC Switch'), '打开 CC Switch')
    assert.equal(exportedParams().get('endpoint'), SITE)
  })
})

describe('CC Switch 导入链接的拼装', () => {
  const cases = [
    {
      name: 'claude：端点就是选中的线路，homepage 仍是站点',
      app: 'claude',
      apiAddress: 'https://cdn.example.com',
      endpoint: 'https://cdn.example.com',
    },
    {
      name: 'codex：/v1 后缀拼在选中的线路上',
      app: 'codex',
      apiAddress: 'https://cdn.example.com',
      endpoint: 'https://cdn.example.com/v1',
    },
    {
      name: '没经过线路选择时回落到站点地址',
      app: 'claude',
      apiAddress: '',
      endpoint: SITE,
    },
    {
      name: '回落到站点地址时 codex 的 /v1 照拼',
      app: 'codex',
      apiAddress: '',
      endpoint: `${SITE}/v1`,
    },
  ]

  for (const item of cases) {
    test(item.name, () => {
      const url = buildCCSwitchURL(
        item.app,
        'My Claude',
        { model: 'claude-4' },
        'sk-real-key',
        item.apiAddress
      )
      const params = new URLSearchParams(
        url.slice('ccswitch://v1/import?'.length)
      )
      assert.equal(params.get('endpoint'), item.endpoint)
      assert.equal(params.get('homepage'), SITE)
      assert.equal(params.get('apiKey'), 'sk-real-key')
      assert.equal(params.get('enabled'), 'true')
    })
  }
})
