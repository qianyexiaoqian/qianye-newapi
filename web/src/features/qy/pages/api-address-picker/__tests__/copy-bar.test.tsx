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
 * 密钥页顶上那条「API 地址」：两个按钮各自把什么写进了剪贴板。
 *
 * # 守什么
 *
 * 1. **两个按钮真的复制出两个不同的东西**。规则本身由
 *    `api-base-url.test.ts` 的用例表守；这里守的是"按钮接对了函数"——
 *    两个按钮都接 `base` 时，页面看起来完全正常，只有粘贴出来才发现
 *    「带 V1 复制」没带 V1。
 *
 * 2. **多条线路时选哪条就复制哪条**。地址簿本来就允许配多条（上限 30），
 *    下拉换了一条而复制出来还是第一条，是这个控件唯一一个"错了却看不出来"
 *    的环节：输入框里显示的地址会跟着变，剪贴板里的却没有。
 *
 * 3. **复制失败要留下能手动选中的文本**。非 HTTPS + 无剪贴板权限时
 *    `copyToClipboard` 会全线失败；只弹一句「复制失败」而不给出那一串，
 *    等于让用户自己去别处找 —— 而带 `/v1` 的那一串本来就不在界面上。
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
  'HTMLSelectElement',
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

/** 剪贴板。`written` 是断言对象；`failNext` 用来演一次写入失败。 */
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
// 失败路径要一路走到底：`copyToClipboard` 在 clipboard API 抛错之后还会试
// 一次 `document.execCommand('copy')`，那一条在 happy-dom 里没有实现。
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
const { api } = await import('@/lib/api')
const { QyApiAddressCopyBar } = await import('../copy-bar')
type AddressOption = { id: number; name: string; remark: string; url: string }

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const SITE = 'https://site.example.com'
localStorage.setItem('status', JSON.stringify({ server_address: SITE }))

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
  queryClient.setQueryData(qyKeys.apiAddresses(), addresses)
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

/**
 * 用真实的 axios adapter 挂起一次「还在路上」或「503」的取数。
 *
 * 不能用 setQueryData 预置：那样 query 一上来就是 success，
 * 加载中与不可用这两态在这个组件里根本走不到。
 */
async function mountWithQuery(mode: 'pending' | 'error') {
  await unmount()
  written.length = 0
  clipboardFails = false
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  // 返回一个 status=503 的**正常响应**,让 axios 自己按 validateStatus 拒掉 ——
  // 手工 throw 一个仿造的 AxiosError 走不通拦截器那条真实路径。
  api.defaults.adapter = async (config) => {
    if (mode === 'pending') {
      // 不能永不 resolve:http-client 对 GET 做在途去重(inFlightGet),
      // 一个挂住的 promise 会把同一条 URL 的后续请求一起钉死。
      await new Promise((resolve) => setTimeout(resolve, 3000))
    }
    return {
      data: {
        success: false,
        code: 'qy_unavailable',
        message: '扩展服务暂时不可用',
      },
      status: 503,
      statusText: 'Service Unavailable',
      headers: {},
      config,
    }
  }
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <QyApiAddressCopyBar />
      </QueryClientProvider>
    )
  })
  if (mode === 'error') {
    // 503 要走完 拦截器 → qyGet → toQyError → react-query 整条链,
    // 20ms 抢不到最后一次 setState。
    for (let i = 0; i < 20; i += 1) {
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 20))
      })
      if (document.body.textContent?.includes(qyEn.qy_aa_bar_unavailable)) break
    }
  } else {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20))
    })
  }
  mounted = { container, root }
}

function buttonByText(text: string): HTMLElement {
  const found = [...document.body.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === text
  )
  assert.ok(found != null, `找不到按钮「${text}」`)
  return found as unknown as HTMLElement
}

function addressInput(): HTMLInputElement {
  const input = document.body.querySelector('input[readonly]')
  assert.ok(input != null, '找不到展示 API 地址的输入框')
  return input as unknown as HTMLInputElement
}

async function click(text: string) {
  const button = buttonByText(text)
  await act(async () => {
    button.click()
  })
}

const COPY = qyEn.qy_aa_bar_copy
const COPY_V1 = qyEn.qy_aa_bar_copy_v1

describe('API 地址的两个复制按钮', () => {
  test('「复制」给基址，「带 V1 复制」给带 /v1 的那一条', async () => {
    await mount([
      {
        id: 7,
        name: 'Primary',
        remark: '',
        url: 'https://primary.example.com',
      },
    ])

    assert.equal(addressInput().value, 'https://primary.example.com')

    await click(COPY)
    await click(COPY_V1)
    assert.deepEqual(written, [
      'https://primary.example.com',
      'https://primary.example.com/v1',
    ])
  })

  test('线路本来就配到了 /v1，「带 V1 复制」不会再拼一层', async () => {
    await mount([
      {
        id: 7,
        name: 'Primary',
        remark: '',
        url: 'https://primary.example.com/v1',
      },
    ])

    await click(COPY)
    await click(COPY_V1)
    assert.deepEqual(
      written,
      ['https://primary.example.com/v1', 'https://primary.example.com/v1'],
      '拼出了 /v1/v1 —— 用户粘进客户端之后全线 404'
    )
  })

  test('一条都没配时用本站地址，而不是一个空的输入框', async () => {
    await mount([])

    assert.equal(addressInput().value, SITE)
    await click(COPY_V1)
    assert.deepEqual(written, [`${SITE}/v1`])
  })

  test('多条线路：换一条，两个按钮跟着换', async () => {
    await mount([
      {
        id: 7,
        name: 'Primary',
        remark: '',
        url: 'https://primary.example.com',
      },
      {
        id: 9,
        name: 'Overseas',
        remark: 'CDN',
        url: 'https://cdn.example.com/',
      },
    ])

    const select = document.body.querySelector('select')
    assert.ok(select != null, '有两条线路时必须给出可切换的下拉')
    assert.deepEqual(
      [...select.querySelectorAll('option')].map((o) => o.textContent),
      ['Primary', 'Overseas']
    )
    assert.equal(addressInput().value, 'https://primary.example.com')

    // 受控 <select>：React 在节点实例上装了一个 value 追踪器，直接
    // `select.value = …` 会被它认成"没变过"而吞掉 change。走原型上的 setter
    // 绕开实例追踪器，这是这类受控输入在测试里唯一可靠的改法。
    const setValue = Object.getOwnPropertyDescriptor(
      HTMLSelectElement.prototype,
      'value'
    )?.set
    assert.ok(setValue != null, '拿不到 HTMLSelectElement 的 value setter')
    await act(async () => {
      setValue.call(select, '9')
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })

    assert.equal(addressInput().value, 'https://cdn.example.com')
    await click(COPY)
    await click(COPY_V1)
    assert.deepEqual(
      written,
      ['https://cdn.example.com', 'https://cdn.example.com/v1'],
      '下拉换了线路，复制出来的却还是第一条'
    )
  })

  test('只有一条线路时不出下拉', async () => {
    await mount([
      {
        id: 7,
        name: 'Primary',
        remark: '',
        url: 'https://primary.example.com',
      },
    ])
    assert.equal(
      document.body.querySelector('select'),
      null,
      '一个只有一个选项的下拉不提供任何信息'
    )
  })

  test('复制失败时，失败的那一串留在输入框里并被整段选中', async () => {
    await mount([
      {
        id: 7,
        name: 'Primary',
        remark: '',
        url: 'https://primary.example.com',
      },
    ])
    clipboardFails = true

    await click(COPY_V1)

    assert.deepEqual(written, [], '这一轮本来就不该写成功')
    const input = addressInput()
    assert.equal(
      input.value,
      'https://primary.example.com/v1',
      '复制失败之后界面上根本没有那一串带 /v1 的地址，用户连手抄都没得抄'
    )
    assert.equal(input.selectionStart, 0)
    assert.equal(input.selectionEnd, input.value.length)
  })
})

/*
 * 加载中 / 接口不可用 / 一条都没配 —— 三种完全不同的状态原先渲染得一模一样。
 *
 * qyResolveAddressOptions 对 `undefined`（在途或出错）与 `[]`（真的没配）
 * 一视同仁，都合成站点自身那一条。于是清单还没落地时这一条已经把**站点地址
 * 当成结论摆出来了**：输入框有值、没有线路下拉、两个按钮都可点，而用户没有
 * 任何办法看出这是个占位值。运营配了备用域/加速线路恰恰是想让用户拿到那一条。
 *
 * 503 那一支更糟：retry:false + staleTime 5min，它不是竞态而是**稳态** ——
 * 扩展库一挂，所有用户的复制条都无声退回主域，页面上没有一处会红。
 * 同目录的选择窗早就把这两态说出来了（`query.isLoading` 转圈、`query.isError`
 * 出红字），同一个 query、同一个功能，两处口径不能相反。
 */
describe('清单还没落地 / 读不到时', () => {
  test('接口 503：仍然给站点地址，但必须说明这不是完整清单', async () => {
    await mountWithQuery('error')

    assert.equal(
      addressInput().value,
      SITE,
      '读不到时回落到站点地址是对的 —— 它是一个能用的网关基址'
    )
    assert.ok(
      document.body.textContent?.includes(qyEn.qy_aa_bar_unavailable),
      '静默退回主域是「错在运行时、界面上不变红」的那一类'
    )
    assert.equal(
      (buttonByText(COPY) as unknown as HTMLButtonElement).disabled,
      false,
      '说明白之后仍然让用户复制得到站点地址,不要把功能整个锁死'
    )
  })

  test('加载中：说出来，并且此刻不许复制', async () => {
    await mountWithQuery('pending')

    assert.ok(
      document.body.textContent?.includes(qyEn.qy_aa_bar_loading),
      '加载中一个字都不说，用户会把占位的站点地址当成结论'
    )
    assert.equal(
      (buttonByText(COPY) as unknown as HTMLButtonElement).disabled,
      true,
      '清单还在路上就允许复制，交出去的是站点主域而不是运营配的线路'
    )
    assert.equal(
      (buttonByText(COPY_V1) as unknown as HTMLButtonElement).disabled,
      true
    )
  })

  test('运营一条都没配：不该出现任何加载中或不可用的提示', async () => {
    await mount([])

    assert.equal(addressInput().value, SITE)
    assert.ok(!document.body.textContent?.includes(qyEn.qy_aa_bar_loading))
    assert.ok(!document.body.textContent?.includes(qyEn.qy_aa_bar_unavailable))
    assert.equal(
      (buttonByText(COPY) as unknown as HTMLButtonElement).disabled,
      false
    )
  })
})
