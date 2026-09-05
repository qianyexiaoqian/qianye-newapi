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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { QY_DISABLED_CONFIG, normalizeQyConfig } from '../config-query'

/**
 * 引导端点里星屑 / 商城两段的归一化（design-15 §8.1）。
 *
 * 缺键方向与玩法开关同一条理由：一个没动过配置的站点升级后不该静默少掉一整块，
 * 所以 `show_entry` 缺键按**显示**；但只在扩展开着时成立，`enabled:false` 的
 * 响应里把它们标成显示会得到一份自相矛盾的快照。
 */
describe('引导端点：stardust / mall 段的归一化', () => {
  test('后端不下发这两段时按显示处理、货币名留空', () => {
    const config = normalizeQyConfig({
      enabled: true,
      available: true,
      features: { stardust: true, mall: true },
    })
    assert.deepEqual(config.stardust, { show_entry: true, name: '' })
    assert.deepEqual(config.mall, { show_entry: true })
    assert.equal(config.features.stardust, true)
    assert.equal(config.features.mall, true)
  })

  test('运营配的货币名原样透传（去空白），show_entry 跟着后端', () => {
    const config = normalizeQyConfig({
      enabled: true,
      stardust: { show_entry: false, name: ' 星尘 ' },
      mall: { show_entry: false },
    })
    assert.deepEqual(config.stardust, { show_entry: false, name: '星尘' })
    assert.deepEqual(config.mall, { show_entry: false })
  })

  test('扩展整体关掉时两段与玩法一起零痕迹', () => {
    const config = normalizeQyConfig({ enabled: false })
    assert.deepEqual(config.stardust, { show_entry: false, name: '' })
    assert.deepEqual(config.mall, { show_entry: false })
    assert.equal(config.features.stardust, false)
    assert.equal(config.features.mall, false)
    assert.equal(config.lottery.plays.wheel, false)
  })

  test('转盘开关缺键按显示，明确关掉时跟随', () => {
    const shown = normalizeQyConfig({ enabled: true, lottery: { plays: {} } })
    assert.equal(shown.lottery.plays.wheel, true)
    const hidden = normalizeQyConfig({
      enabled: true,
      lottery: { plays: { wheel: false } },
    })
    assert.equal(hidden.lottery.plays.wheel, false)
  })

  test('全关配置里 features 与两段都是关的', () => {
    assert.equal(QY_DISABLED_CONFIG.features.stardust, false)
    assert.equal(QY_DISABLED_CONFIG.features.mall, false)
    assert.equal(QY_DISABLED_CONFIG.stardust.show_entry, false)
    assert.equal(QY_DISABLED_CONFIG.mall.show_entry, false)
    assert.equal(QY_DISABLED_CONFIG.lottery.plays.wheel, false)
  })
})
