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
 * 裁决 4「仅 Steins Gate 主题下固定 UI」的行为回归。
 *
 * 钉两件事,都是"改坏了肉眼看不出来"的:
 *
 * 1. **轴锁只对 steins-gate 生效**。把 AXIS_LOCKED_PRESETS 写宽一点
 *    (比如漏写成"所有非 default 预设"),别的十个预设的调色板会一起塌掉,
 *    而评审通常只会去看 steins-gate 那一屏。
 *
 * 2. **font 轴是【就地掐断】而不是【藏起来】**。这一条是整个裁决里唯一
 *    在匿名落地页也必须成立的部分:落地页没有调色板可点,一个陈旧的
 *    `theme_font=serif` cookie 会让游戏那套等宽标签排成衬线。
 *    resolveThemeFont 是 provider 写 `data-theme-font` 属性的唯一来源,
 *    所以断在这里就等于断在 DOM 上。
 *    把 resolveThemeFont 里的 `|| isThemeAxisLocked(preset)` 删掉,
 *    下面第二组用例必须变红。
 *
 * 抽屉本身的条件渲染(四个轴的控件在锁定时不入 DOM)在浏览器里实测,
 * 这里不重复渲染 Base UI 的 Sheet —— 那需要把 sidebar / layout / direction
 * 三个 provider 与一个 portal 都搭起来,测的却是 React 会不会执行 `&&`,
 * 属于"证明代码跑过"而不是"保护契约"。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  AXIS_LOCKED_PRESETS,
  isThemeAxisLocked,
  resolveThemeFont,
  THEME_PRESETS,
  type ThemeFont,
  type ThemePreset,
} from '@/lib/theme-customization'

describe('主题轴锁的作用范围', () => {
  test('只有 steins-gate 被锁', () => {
    assert.deepEqual([...AXIS_LOCKED_PRESETS], ['steins-gate'])
  })

  for (const preset of THEME_PRESETS) {
    const expected = preset.value === 'steins-gate'
    test(`${preset.value} ${expected ? '锁定' : '不锁定'}`, () => {
      assert.equal(isThemeAxisLocked(preset.value), expected)
    })
  }
})

describe('font 轴在锁定预设下就地失效', () => {
  const fonts: ThemeFont[] = ['default', 'sans', 'serif']

  for (const font of fonts) {
    test(`steins-gate + font=${font} → sans`, () => {
      // steins-gate 未登记在 PRESET_DEFAULT_FONT 里,签名字体是 sans。
      // 三种偏好必须解析成同一个值,否则 cookie 还能穿透。
      assert.equal(resolveThemeFont(font, 'steins-gate'), 'sans')
    })
  }

  test('未锁定的预设仍然尊重用户选择', () => {
    // 反向用例:漏掉它的话,"把所有预设都锁死"也能让上面几条通过。
    assert.equal(resolveThemeFont('serif', 'default'), 'serif')
    assert.equal(resolveThemeFont('sans', 'anthropic'), 'sans')
    // anthropic 的签名字体是 serif,`default` 仍按预设解析
    assert.equal(resolveThemeFont('default', 'anthropic'), 'serif')
    assert.equal(
      resolveThemeFont('default', 'rose-garden' as ThemePreset),
      'sans'
    )
  })
})
