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
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { normalizeQyConfig } from '@/features/qy/lib/config-query'
import { qyAnyLotPlayShown, qyLotDrawShown } from '@/features/qy/lib/pages'
import type { QyConfig, QyLotPlays } from '@/features/qy/lib/types'
import { qyEntrySwitches } from '@/features/qy/nav'

/**
 * 「娱乐模块按玩法显示/隐藏」的前端侧契约。
 *
 * 这条链上的每一步失败都是**沉默的**：
 *
 *   · 后端没下发 `plays` 段时若按"全部隐藏"处理，一个从没动过配置的站点在升级
 *     之后整块娱乐功能就消失了，前端没有任何报错（本仓在「缺一段配置 = 整块
 *     不可见」上已经栽过）；
 *   · 四种玩法全关时若侧栏那一行还在，用户点进去看到的是一个只剩「我的参与」
 *     的页面 —— 与"页面坏了"长得一样；
 *   · 反过来，若把「我的参与」也跟着藏掉，已参与的用户就再也找不到自己的票与
 *     奖励，那已经不是显隐问题而是把钱藏起来了。
 */

// __tests__ → lib → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)

const ALL_SHOWN: QyLotPlays = {
  draw_rank: true,
  draw_prob: true,
  draw_ball: true,
  guess: true,
}

/** 一份"扩展开着、娱乐入口也开着"的配置，玩法由用例自己指定。 */
function configWith(plays: QyLotPlays): QyConfig {
  return normalizeQyConfig({
    enabled: true,
    available: true,
    features: { lottery: true },
    lottery: { show_entry: true, proof_public: true, plays },
  })
}

describe('玩法显隐 — 引导端点的归一化', () => {
  test('后端不下发 plays 段时四种玩法全部显示', () => {
    // 期望手写，不从 QY_DISABLED_CONFIG 之类的常量回读。
    const config = normalizeQyConfig({
      enabled: true,
      available: true,
      features: { lottery: true },
      lottery: { show_entry: true, proof_public: true },
    })
    assert.deepEqual(config.lottery.plays, {
      draw_rank: true,
      draw_prob: true,
      draw_ball: true,
      guess: true,
    })
  })

  test('只下发一个键时，其余三个仍按显示处理', () => {
    const config = normalizeQyConfig({
      enabled: true,
      features: { lottery: true },
      lottery: { show_entry: true, plays: { draw_ball: false } },
    })
    assert.deepEqual(config.lottery.plays, {
      draw_rank: true,
      draw_prob: true,
      draw_ball: false,
      guess: true,
    })
  })

  test('后端明确关掉四种时逐个跟随', () => {
    const config = normalizeQyConfig({
      enabled: true,
      features: { lottery: true },
      lottery: {
        show_entry: true,
        plays: {
          draw_rank: false,
          draw_prob: false,
          draw_ball: false,
          guess: true,
        },
      },
    })
    assert.deepEqual(config.lottery.plays, {
      draw_rank: false,
      draw_prob: false,
      draw_ball: false,
      guess: true,
    })
  })

  test('扩展整体关掉时不留任何玩法', () => {
    // 这一条与"缺键按显示"并不矛盾：那讲的是后端在场但没下发这一段，
    // 这讲的是后端明确说了 enabled:false。
    const config = normalizeQyConfig({ enabled: false })
    assert.deepEqual(config.lottery.plays, {
      draw_rank: false,
      draw_prob: false,
      draw_ball: false,
      guess: false,
    })
  })
})

/**
 * 三张大厅标签 × 四个玩法开关的**合并口径**（本轮：「每个入口都可以单独被
 * 隐藏或显示」）。
 *
 * 没有第五个开关：「双色球」「竞猜」各自只压着一种玩法，标签可见性就是那一个
 * 开关本身；「抽奖」底下压着按名次与按公示概率两种，**两种都关掉时**它才消失。
 * 所以这里只有一个派生函数要测（`qyLotDrawShown`），另外两张标签直接读
 * `plays.draw_ball` / `plays.guess`。
 *
 * 关键的一条是第二行：只开双色球时「抽奖」那张标签必须**消失**。改造前它是
 * `rank || prob || ball`，于是只开双色球会留下一张永远空的「抽奖」标签 ——
 * 那正是"两套开关互相打架"的形状，而界面上不会报错。
 */
describe('玩法显隐 — 三张标签的可见性', () => {
  const cases: {
    name: string
    plays: QyLotPlays
    draw: boolean
    any: boolean
  }[] = [
    { name: '四个都开', plays: ALL_SHOWN, draw: true, any: true },
    {
      name: '只开双色球：抽奖标签消失（它已经是自己的标签了），整组入口保留',
      plays: { ...ALL_SHOWN, draw_rank: false, draw_prob: false, guess: false },
      draw: false,
      any: true,
    },
    {
      name: '只开按名次：抽奖标签还在',
      plays: { ...ALL_SHOWN, draw_prob: false, draw_ball: false, guess: false },
      draw: true,
      any: true,
    },
    {
      name: '只开按公示概率：抽奖标签还在',
      plays: { ...ALL_SHOWN, draw_rank: false, draw_ball: false, guess: false },
      draw: true,
      any: true,
    },
    {
      name: '抽奖底下两种都关、双色球与竞猜还开：抽奖标签消失，整组入口保留',
      plays: {
        draw_rank: false,
        draw_prob: false,
        draw_ball: true,
        guess: true,
      },
      draw: false,
      any: true,
    },
    {
      name: '抽奖三种全关、竞猜还开：抽奖标签消失，整组入口保留',
      plays: {
        draw_rank: false,
        draw_prob: false,
        draw_ball: false,
        guess: true,
      },
      draw: false,
      any: true,
    },
    {
      name: '只开抽奖：整组入口保留',
      plays: { ...ALL_SHOWN, draw_ball: false, guess: false },
      draw: true,
      any: true,
    },
    {
      name: '只开竞猜：抽奖标签消失，整组入口保留',
      plays: {
        draw_rank: false,
        draw_prob: false,
        draw_ball: false,
        guess: true,
      },
      draw: false,
      any: true,
    },
    {
      name: '四个全关',
      plays: {
        draw_rank: false,
        draw_prob: false,
        draw_ball: false,
        guess: false,
      },
      draw: false,
      any: false,
    },
  ]

  for (const tc of cases) {
    test(tc.name, () => {
      assert.equal(qyLotDrawShown(tc.plays), tc.draw)
      assert.equal(qyAnyLotPlayShown(tc.plays), tc.any)
    })
  }

  /**
   * 「整组入口」= 三张标签里还有一张在。
   *
   * 期望值在这里独立算一遍（三张标签各自的可见性取或），而不是再调一次
   * `qyAnyLotPlayShown` —— 后者等于断言它等于它自己。守的是"抽奖那张标签
   * 的判据换了之后，整组入口跟着算错"：把 `draw_ball` 从 `qyLotDrawShown`
   * 里摘出来时，若忘了在 `qyAnyLotPlayShown` 里补上，只开双色球的站点会
   * 整行导航消失，而双色球明明是开着的。
   */
  test('整组入口 = 三张标签的并集', () => {
    for (const tc of cases) {
      const anyTabShown =
        qyLotDrawShown(tc.plays) || tc.plays.draw_ball || tc.plays.guess
      assert.equal(
        qyAnyLotPlayShown(tc.plays),
        anyTabShown,
        `${tc.name}：整组入口与三张标签的并集不一致`
      )
    }
  })
})

describe('玩法显隐 — 侧栏那一行', () => {
  test('还有玩法开着时入口保留', () => {
    assert.deepEqual(qyEntrySwitches(configWith(ALL_SHOWN)), { lottery: true })
  })

  test('只剩双色球时入口仍然保留', () => {
    const plays: QyLotPlays = {
      draw_rank: false,
      draw_prob: false,
      draw_ball: true,
      guess: false,
    }
    assert.deepEqual(qyEntrySwitches(configWith(plays)), { lottery: true })
  })

  test('只剩竞猜时入口仍然保留', () => {
    const plays: QyLotPlays = {
      draw_rank: false,
      draw_prob: false,
      draw_ball: false,
      guess: true,
    }
    assert.deepEqual(qyEntrySwitches(configWith(plays)), { lottery: true })
  })

  test('四种玩法全关时整行入口消失', () => {
    const plays: QyLotPlays = {
      draw_rank: false,
      draw_prob: false,
      draw_ball: false,
      guess: false,
    }
    assert.deepEqual(qyEntrySwitches(configWith(plays)), { lottery: false })
  })

  test('站点级展示开关关掉时，玩法全开也不出现入口', () => {
    const config = normalizeQyConfig({
      enabled: true,
      features: { lottery: true },
      lottery: { show_entry: false, plays: ALL_SHOWN },
    })
    assert.deepEqual(qyEntrySwitches(config), { lottery: false })
  })
})

/**
 * 「我的参与」这张标签**不许**挂任何玩法开关。
 *
 * 它是已参与用户查票、看结果、领文本奖的唯一入口。三张大厅标签可以按玩法消失，
 * 这一张不行 —— 藏掉它等于把已经收了钱的活动连同用户的凭据一起藏起来，
 * 而界面上不会有任何一处报错。
 *
 * 用源码断言而不是渲染：渲染这一页要路由 + react-query + zustand 三套 provider，
 * 而要守的东西就是那一行 `bodies` 里有没有跟着写条件。
 */
describe('玩法显隐 — 我的参与永不隐藏', () => {
  const hub = readFileSync(
    join(srcDir, 'features', 'qy', 'pages', 'lottery', 'hub.tsx'),
    'utf8'
  )

  test('三张大厅标签各自跟着自己的玩法开关', () => {
    assert.match(hub, /'\/qy\/lottery':\s*drawShown\s*\?/)
    assert.match(hub, /'\/qy\/lottery-guess':\s*plays\.guess\s*\?/)
    assert.match(hub, /'\/qy\/lottery-ball':\s*plays\.draw_ball\s*\?/)
  })

  test('我的参与那一行是无条件的', () => {
    const line = hub
      .split('\n')
      .find((row) => row.includes("'/qy/lottery-records':"))
    assert.ok(line != null, 'hub.tsx 里已经没有「我的参与」这张标签了')
    assert.ok(
      !line.includes('?') && !line.includes('&&'),
      `「我的参与」被挂上了条件：${line.trim()} —— 已参与的用户必须还能查票与领奖`
    )
  })
})
