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
 * 公告表单**搬家之后仍然接着线**：真 DOM、真 axios（假 adapter）、真文案。
 *
 * # 为什么源码级断言不够
 *
 * 同目录的 `section-placement.test.ts` 已经钉住"表单只有一份、在新家里"。
 * 那一条守的是**位置**，但搬家最典型的坏法是位置对了、线断了：
 *
 *   · 回读接的还是旧 key，于是打开表单永远是空的（运营以为配置丢了）；
 *   · 保存打出去了，但 `enabled` 漏在了请求体外，于是"我明明开了它还是不显示"；
 *   · 保存成功后没有失效缓存，回到这一页看到的还是搬家前那一版。
 *
 * 三种都编译得过、typecheck 全绿、源码 grep 也看不出来 —— 只有真的点一次才知道。
 *
 * 所以这里断言的是：**打开能回读到服务端的值** → **改一次** → **打出去的是
 * 那一条路径与那一份完整请求体** → **保存后表单跟着服务端的新值走**。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, beforeEach, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', '..', '..', '..', '..')

const domWindow = new Window({ height: 900, width: 1280 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLTextAreaElement',
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')

const zhBundle = JSON.parse(
  readFileSync(join(srcDir, 'i18n', 'qy', 'zh.json'), 'utf8')
) as Record<string, string>

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhBundle } },
})

const { api } = await import('@/lib/api')
const { QyRestrictedNoticeCard } = await import('../components/notice-card')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/** 服务端当前存着的那一份。PUT 会就地改写它，随后的 GET 读到的就是新值。 */
let stored = {
  enabled: false,
  title: '旧标题',
  body: '旧正文',
  updated_at: 1,
  updated_by: 7,
  title_max_runes: 120,
  body_max_runes: 4000,
}

let sent: { method: string; url: string; body: unknown }[] = []

api.defaults.adapter = async (config) => {
  const body =
    typeof config.data === 'string'
      ? (JSON.parse(config.data) as Record<string, unknown>)
      : undefined
  sent.push({
    method: String(config.method).toUpperCase(),
    url: String(config.url),
    body,
  })
  if (String(config.method).toUpperCase() === 'PUT' && body != null) {
    // 真服务端就是这么做的：整份覆盖写，然后回读拿到的是新值。
    stored = {
      ...stored,
      enabled: body.enabled === true,
      title: String(body.title ?? ''),
      body: String(body.body ?? ''),
      updated_at: stored.updated_at + 1,
    }
  }
  return {
    data: { success: true, message: '', data: stored },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }
}

const mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

async function unmountAll() {
  for (;;) {
    const entry = mounted.pop()
    if (entry == null) return
    await act(async () => entry.root.unmount())
    entry.container.remove()
  }
}

after(unmountAll)
beforeEach(() => {
  sent = []
})

async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function mountCard() {
  await unmountAll()
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  })
  await act(async () =>
    root.render(
      <QueryClientProvider client={client}>
        <QyRestrictedNoticeCard />
      </QueryClientProvider>
    )
  )
  await settle()
  mounted.push({ container, root })
}

function field(id: string): HTMLInputElement | HTMLTextAreaElement {
  const node = document.getElementById(id)
  assert.ok(node != null, `找不到 #${id}`)
  return node as HTMLInputElement | HTMLTextAreaElement
}

/** 取一句真实文案。键不存在时当场失败，不允许回落成空串（那会让断言恒真）。 */
function copy(key: string): string {
  const value = zhBundle[key]
  assert.ok(value != null && value !== '', `文案键 ${key} 没有登记进 zh.json`)
  return value
}

/**
 * 保存按钮。
 *
 * 按**文案**找而不是按位置找。这里的 `Save` 是**上游**的 i18n 键（不在
 * `i18n/qy/zh.json` 里），而本测试只挂了 qy 那份 bundle，所以 i18next 原样
 * 吐出键名 —— 断言打在 `'Save'` 上是对的：键被改掉时这里当场找不到按钮。
 */
function saveButton(): HTMLButtonElement {
  const buttons = [...document.querySelectorAll('button')]
  const target = buttons.find((node) =>
    (node.textContent ?? '').includes('Save')
  )
  assert.ok(target != null, '找不到保存按钮')
  return target as HTMLButtonElement
}

async function type(
  node: HTMLInputElement | HTMLTextAreaElement,
  text: string
) {
  await act(async () => {
    // React 19 + happy-dom：直接改 `.value` 会被 React 的受控值追踪吞掉，
    // 必须走原型上的 setter 再派发 input 事件。
    const proto =
      node instanceof HTMLTextAreaElement
        ? HTMLTextAreaElement.prototype
        : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(node, text)
    node.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('受限账号公告：搬家之后仍然能存能回读', () => {
  test('打开时回读服务端的值，而不是一张空表单', async () => {
    stored = {
      enabled: true,
      title: '解除限制的流程',
      body: '请在工作日发工单。',
      updated_at: 9,
      updated_by: 7,
      title_max_runes: 120,
      body_max_runes: 4000,
    }
    await mountCard()

    assert.equal(field('qy-restricted-notice-title').value, '解除限制的流程')
    assert.equal(field('qy-restricted-notice-body').value, '请在工作日发工单。')
    assert.deepEqual(
      sent.map((item) => `${item.method} ${item.url}`),
      ['GET /api/qy/admin/restricted-notice'],
      '回读走的不是管理端那条路径 —— 表单会永远是空的'
    )
    // 上限跟着内容一起下发，前端不另写一份常量。计数器显示的就是它。
    assert.ok(
      (document.body.textContent ?? '').includes('/ 120'),
      '标题计数器没有用服务端下发的上限'
    )
  })

  test('改一次 → 打出去的是完整的一份，回来之后表单跟着新值走', async () => {
    stored = {
      enabled: true,
      title: '旧标题',
      body: '旧正文',
      updated_at: 1,
      updated_by: 7,
      title_max_runes: 120,
      body_max_runes: 4000,
    }
    await mountCard()
    sent = []

    await type(field('qy-restricted-notice-title'), '  新的申诉指引  ')
    await type(field('qy-restricted-notice-body'), '发工单并附上订单号。')
    await act(async () => saveButton().click())
    await settle()

    const put = sent.find((item) => item.method === 'PUT')
    assert.ok(put != null, '点了保存却一条 PUT 都没打出去')
    assert.equal(put.url, '/api/qy/admin/restricted-notice')
    // `enabled` 必须在请求体里。后端没有"不传即保持原样"的语义：漏传等于
    // 每次保存都把公告关掉，而界面上开关还是开着的 ——「以为改了其实没改」
    // 的镜像版本。
    assert.deepEqual(put.body, {
      enabled: true,
      // 两端空白由前端 trim 掉：后端也 trim，但前端不 trim 的话预览与计数器
      // 会按未 trim 的算，运营会看到一个与线上不同的字数。
      title: '新的申诉指引',
      body: '发工单并附上订单号。',
    })

    // 保存成功后要失效缓存重取，否则这一页显示的还是搬家前那一版。
    const reread = sent.filter(
      (item) =>
        item.method === 'GET' && item.url === '/api/qy/admin/restricted-notice'
    )
    assert.ok(reread.length >= 1, '保存后没有重新回读，界面会停在旧值上')
    assert.equal(field('qy-restricted-notice-title').value, '新的申诉指引')
    assert.equal(stored.title, '新的申诉指引', '服务端那一份没有被改写')
  })

  test('开着却清空正文时，保存按钮按不下去（后端会 400，先在这里挡住）', async () => {
    stored = {
      enabled: true,
      title: '标题',
      body: '正文',
      updated_at: 1,
      updated_by: 7,
      title_max_runes: 120,
      body_max_runes: 4000,
    }
    await mountCard()
    sent = []

    await type(field('qy-restricted-notice-body'), '   ')
    assert.equal(
      saveButton().disabled,
      true,
      '开着公告却没有正文时保存仍然可点 —— 受限用户首屏会出现一块空白卡片'
    )
    assert.ok(
      (document.body.textContent ?? '').includes(
        copy('qy_restricted_notice_incomplete')
      ),
      '禁用了按钮却没说为什么，运营只会以为页面坏了'
    )
    assert.equal(sent.length, 0)
  })
})
