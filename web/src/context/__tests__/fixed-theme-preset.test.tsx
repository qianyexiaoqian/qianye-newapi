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
 * 「移除主题设置、主题固定为 Steins Gate」的行为回归。
 *
 * 守三件事，都是改坏了**页面照常渲染、肉眼一时看不出来**的：
 *
 * 1. **`<body data-theme-preset='steins-gate'>` 必须被写上。**
 *    全部 qy 主题 CSS（`styles/qy-sg-*.css`，一千三百多行）都挂在这个属性的
 *    作用域下。provider 里那个 effect 被删掉、或值写错一个字母，页面不会报错、
 *    不会白屏，只会整站退回上游裸样式 —— 正是本仓反复出现的「写了但没接上」。
 *
 * 2. **被移除的四个轴不能再通过陈旧 cookie 生效。**
 *    font / radius / scale / contentLayout 原本各有一份 cookie。只把控件从抽屉里
 *    拿掉而保留读取逻辑，用户浏览器里那份 `theme_font=serif` 仍会被读出来写进
 *    DOM —— 既看不到控件又改不回去，比不删更糟。因此下面先往 document.cookie 里
 *    塞满旧偏好再挂载，断言它们一个都没落到 DOM 上。
 *    断言写成「不等于 cookie 里的那个值」而不是「属性必须不存在」：日后若有人
 *    改成显式把某个轴钉成常量写进 DOM，那是等价实现，不该误报。
 *
 * 3. **常量与样式表对得上。** `FIXED_THEME_PRESET` 改成一个 CSS 里没有的名字，
 *    上面两条仍然全绿（属性照样写、cookie 照样不生效），但站点会变成裸样式。
 *    所以直接去 `qy-sg-tokens.css` 里核对选择器是否存在。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, beforeEach, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')

const { ThemeCustomizationProvider, useThemeCustomization } =
  await import('@/context/theme-customization-provider')
const {
  DEFAULT_THEME_CUSTOMIZATION,
  FIXED_THEME_PRESET,
  THEME_PRESETS,
  THEME_PRESET_ATTRIBUTE,
} = await import('@/lib/theme-customization')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

async function mountProvider(children?: React.ReactNode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  roots.push({ container, root })
  await act(async () => {
    root.render(
      <ThemeCustomizationProvider>{children}</ThemeCustomizationProvider>
    )
  })
  return container
}

after(async () => {
  await act(async () => {
    for (const { root } of roots) root.unmount()
  })
  for (const { container } of roots) container.remove()
})

/** 抹掉上一条用例留下的 DOM 痕迹，避免"其实是上一条写的"这种假绿。 */
beforeEach(() => {
  for (const name of document.body.getAttributeNames()) {
    if (name.startsWith('data-theme-')) document.body.removeAttribute(name)
  }
})

describe('站点主题固定为 Steins Gate', () => {
  test('provider 把预设属性写到 <body> 上', async () => {
    await mountProvider()
    assert.equal(
      document.body.getAttribute(THEME_PRESET_ATTRIBUTE),
      'steins-gate'
    )
  })

  test('陈旧的主题 cookie 一个都不再生效', async () => {
    // 一个"上一版用户把五个轴全拨过一遍"的浏览器。
    for (const cookie of [
      'theme_preset=lake-view',
      'theme_font=serif',
      'theme_radius=xl',
      'theme_scale=xl',
      'theme_content_layout=centered',
    ]) {
      document.cookie = cookie
    }

    await mountProvider()

    assert.equal(
      document.body.getAttribute(THEME_PRESET_ATTRIBUTE),
      'steins-gate',
      'theme_preset cookie 又被读回来了：换预设的入口已经移除，用户将被锁在一个换不回去的主题里'
    )
    for (const [attribute, staleValue] of [
      ['data-theme-font', 'serif'],
      ['data-theme-radius', 'xl'],
      ['data-theme-scale', 'xl'],
      ['data-theme-content-layout', 'centered'],
    ] as const) {
      assert.notEqual(
        document.body.getAttribute(attribute),
        staleValue,
        `${attribute} 仍在跟随陈旧 cookie：控件已经没了，用户改不回去`
      )
    }
  })

  test('useThemeCustomization 返回的是那个固定值', async () => {
    let seen: { preset: string; radius: string } | null = null
    function Probe() {
      const { customization } = useThemeCustomization()
      seen = { preset: customization.preset, radius: customization.radius }
      return null
    }
    await mountProvider(<Probe />)
    // 上游三张图表拿 `${preset}:${radius}` 当圆角重测的刷新键，键必须恒定。
    assert.deepEqual(seen, { preset: 'steins-gate', radius: 'default' })
  })
})

describe('固定预设与样式表的对应关系', () => {
  test('FIXED_THEME_PRESET 是预设表里真实存在的取值', () => {
    assert.equal(DEFAULT_THEME_CUSTOMIZATION.preset, FIXED_THEME_PRESET)
    assert.ok(
      THEME_PRESETS.some((preset) => preset.value === FIXED_THEME_PRESET),
      `${FIXED_THEME_PRESET} 不在 THEME_PRESETS 里`
    )
  })

  test('qy 主题 CSS 的作用域选择器与常量一致', () => {
    const css = readFileSync(
      new URL('../../styles/qy-sg-tokens.css', import.meta.url),
      'utf8'
    )
    assert.ok(
      css.includes(`[${THEME_PRESET_ATTRIBUTE}='${FIXED_THEME_PRESET}']`),
      'qy-sg-tokens.css 里没有与固定预设匹配的选择器：主题 CSS 一条都不会命中，站点会退回上游裸样式'
    )
  })
})
