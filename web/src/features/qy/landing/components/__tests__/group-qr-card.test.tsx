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
 * 首屏群聊二维码卡：**三种形态各说各的真话，而且外链永远是安全的**。
 *
 * 只配了群号却照样画一张码，扫出来是一串无意义纯文本，比不画更糟；
 * 而说明文字不跟着形态换，就是一句不会有任何东西报错的假话 —— 与信号板那两句
 * caption 同一条纪律。外链那一条是安全回归：这个地址由管理员填、给匿名访客点。
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

const { QyLandingGroupQrCard } = await import('../group-qr-card')

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

async function render(joinUrl: string, number: string): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(<QyLandingGroupQrCard joinUrl={joinUrl} number={number} />)
  })
  mounted.push({ container, root })
  return container
}

const JOIN_URL = 'https://qm.qq.com/q/abc?k=xyz'

/**
 * 二维码按**可访问名**认,不按标签名。卡片里还有别的 <svg>(复制按钮与外链的
 * 图标),用 querySelector('svg') 认会把图标当成二维码。
 */
const hasQrCode = (container: HTMLElement) =>
  [...container.querySelectorAll('svg > title')].some(
    (title) => title.textContent === 'Group join QR code'
  )

describe('首屏群聊二维码卡', () => {
  test('两项都配时给出可扫的码、可复制的群号和可点的外链', async () => {
    const container = await render(JOIN_URL, '1013106587')

    assert.equal(hasQrCode(container), true, '配了链接就必须画出二维码')

    assert.match(container.textContent ?? '', /1013106587/)
    assert.equal(
      container.querySelectorAll('[aria-label="Copy the group number"]').length,
      1,
      '群号必须能一键复制 —— 手机用户没法扫自己屏幕上的码'
    )

    const link = container.querySelectorAll('a')[0]
    assert.ok(link)
    assert.equal(link.getAttribute('href'), JOIN_URL)
    assert.equal(link.getAttribute('target'), '_blank')
    const rel = link.getAttribute('rel') ?? ''
    assert.match(rel, /noopener/)
    assert.match(rel, /noreferrer/)

    assert.match(container.textContent ?? '', /Scan the code or use the button/)
  })

  test('只配链接时不出现群号那一格', async () => {
    const container = await render(JOIN_URL, '')

    assert.equal(hasQrCode(container), true)
    assert.equal(container.querySelectorAll('a').length, 1)
    assert.equal(
      container.querySelectorAll('[aria-label="Copy the group number"]').length,
      0
    )
  })

  test('只配群号时绝不伪造一张码，说明文字也跟着换', async () => {
    const container = await render('', '1013106587')

    assert.equal(
      hasQrCode(container),
      false,
      '拿群号去编一张码，扫出来是一串无意义纯文本，比不画更糟'
    )
    assert.equal(container.querySelectorAll('a').length, 0)
    assert.match(container.textContent ?? '', /1013106587/)
    assert.match(container.textContent ?? '', /Search this group number/)
  })

  test('被以未净化的值调用时不把伪协议放进 href', async () => {
    const container = await render('javascript:alert(1)', '1013106587')

    for (const anchor of container.querySelectorAll('a')) {
      assert.doesNotMatch(anchor.getAttribute('href') ?? '', /^javascript:/i)
    }
    assert.equal(hasQrCode(container), false)
    assert.match(container.textContent ?? '', /1013106587/)
  })

  test('两项都为空时整块不渲染', async () => {
    const container = await render('', '')
    assert.equal(container.textContent, '')
  })
})
