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

For commercial licensing, please contact support@quantumnous.com
*/
/*
 * 卡片背景图**真的渲染出来**的三种形态。
 *
 * 纯逻辑用例（`lib/__tests__/cover.test.ts`）覆盖的是"来源 → src"那一次翻译。
 * 翻译对了但组件没接上、或者没配封面时渲染成一个空白块 —— 这两种失效纯逻辑
 * 用例一条都看不见，而它们正是项目方打开大厅时会看到的东西：
 *
 *  1. **有封面**：出一个 `<img>`，`src` 指向站内匿名端点；外链要挂
 *     `referrerPolicy="no-referrer"`（不把访客的来源地址送给第三方主机）。
 *  2. **没配封面**：出兜底图案，且**不能**出任何 `<img>` —— 一个 `src=""`
 *     的 img 在多数浏览器里会立刻请求当前页面并画出破图图标，
 *     而"不要出现破图或空白块"正是这条需求的原话。
 *  3. **配了但加载失败**：`onError` 之后同样退回兜底图案。外链是管理员填的
 *     第三方地址，图床跑路 / 防盗链 / https 页面里的 http 图被拦，
 *     每一种都会在某天发生。
 *
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

const lotteryDir = join(dirname(fileURLToPath(import.meta.url)), '..')

const domWindow = new Window({ height: 900, width: 1280 })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
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
  'matchMedia',
  'DOMRect',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zh as Record<string, string> } },
})

const { QyLotCover } = await import('../components/lottery-cover')

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

async function mount(
  activity: Parameters<typeof QyLotCover>[0]['activity']
): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  roots.push({ container, root })
  await act(async () => {
    root.render(<QyLotCover activity={activity} />)
  })
  return container
}

describe('活动卡片的背景图', () => {
  test('有上传封面时渲染站内地址', async () => {
    const container = await mount({ cover_ref: 'abc123', kind: 'draw' })
    const img = container.querySelector('img')
    assert.ok(img != null, '有封面时必须真的出一个 <img>')
    assert.equal(img.getAttribute('src'), '/api/qy/lottery/covers/abc123')
    // 站内引用不挂 no-referrer：那条请求本来就发给自己。
    assert.equal(img.getAttribute('referrerpolicy'), null)
    assert.equal(img.getAttribute('alt'), zh.qy_lot_cover_alt)
  })

  test('外链封面必须挂 no-referrer', async () => {
    const container = await mount({
      cover_url: 'https://cdn.test/a.png',
      kind: 'guess',
    })
    const img = container.querySelector('img')
    assert.ok(img != null)
    assert.equal(img.getAttribute('src'), 'https://cdn.test/a.png')
    assert.equal(
      img.getAttribute('referrerpolicy'),
      'no-referrer',
      '不挂它，每一位打开大厅的访客都会把本站地址送给管理员随手填的那台主机'
    )
  })

  test('没配封面时出兜底图案，且一个 img 都不出', async () => {
    const container = await mount({ kind: 'draw' })
    assert.equal(
      container.querySelector('img'),
      null,
      'src="" 的 img 会立刻请求当前页面并画出破图 —— 那正是这条需求要避免的'
    )
    assert.ok(
      container.querySelector('svg') != null,
      '兜底必须画点什么：一块纯色空白与"图还在加载"长得一模一样，用户会一直等'
    )
  })

  test('封面加载失败时退回兜底图案', async () => {
    const container = await mount({
      cover_url: 'https://cdn.test/gone.png',
      kind: 'draw',
    })
    const img = container.querySelector('img')
    assert.ok(img != null)
    await act(async () => {
      img.dispatchEvent(new Event('error', { bubbles: false }))
    })
    assert.equal(
      container.querySelector('img'),
      null,
      '外链会在某天 404 / 被防盗链拦掉，没有这条退路，大厅第一屏就是一排破图'
    )
    assert.ok(container.querySelector('svg') != null)
  })
})

describe('封面在大厅与详情页都要出现', () => {
  // 详情页整页渲染要拖上 react-query 与路由，成本远高于它能守住的东西；
  // 而这里真正会失效的只有一件事：**详情页压根没有接上这个组件**
  // （封面那一路交付时它就是没接，理由是当时另一位在大改同一个文件）。
  // 所以直接读源码，把"接上了"本身钉住。
  const detail = readFileSync(join(lotteryDir, 'detail.tsx'), 'utf8')
  const card = readFileSync(
    join(lotteryDir, 'components', 'lottery-activity-card.tsx'),
    'utf8'
  )

  test('详情页渲染头图，且用的是 hero 形状', () => {
    assert.match(
      detail,
      /<QyLotCover[^>]*variant='hero'/,
      '大厅卡片上有图、点进去却什么都没有，用户会以为自己点错了活动'
    )
    assert.match(detail, /activity=\{activity\}/)
  })

  test('大厅卡片也仍然渲染封面', () => {
    assert.match(card, /<QyLotCover/)
  })
})

/*
 * ── 变异验证（手工执行并已回滚）──
 *
 *   QyLotCover 在 src == null 时也渲染 <img src={src ?? ''}>
 *       → "没配封面时出兜底图案" 红
 *   QyLotCover 去掉 onError → setFailedSrc
 *       → "封面加载失败时退回兜底图案" 红
 *   QyLotCover 把 referrerPolicy 恒设为 undefined
 *       → "外链封面必须挂 no-referrer" 红
 *   QyLotCover 兜底分支只渲染一个空 div（不画图标）
 *       → "兜底必须画点什么" 红
 *   detail.tsx 去掉 <QyLotCover …>
 *       → "详情页渲染头图，且用的是 hero 形状" 红
 *   detail.tsx 的 variant 改成 'banner'
 *       → 同一条红
 */
