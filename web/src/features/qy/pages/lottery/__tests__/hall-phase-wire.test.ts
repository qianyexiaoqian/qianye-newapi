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
 * 大厅分区这根线的**两头**：前端发的参数名 / 取值，与后端认的那一套。
 *
 * # 为什么这条测试值得单独存在
 *
 * 上一版的缺陷不在任何一侧内部 —— 两侧各自都是对的、都能编译、单测都绿：
 *
 *   · 前端：`status?: string` 接得住 `'open' | 'done'`；
 *   · 后端：`switch c.Query("phase")` 认 `live` / `ended`，写得也没错。
 *
 * 错的是**它们之间**，而 TypeScript 与 Go 各自的类型系统都看不到对面。
 * 隔壁 `hall-phase-render.test.tsx` 用桩把这条线钉在了前端这一头（桩只按
 * `phase` 分流），但桩本身也是前端写的 —— 桩与产品代码一起漂移时它照样全绿。
 * 所以这里去读**真正的 Go 源码**，把两侧的字面量对起来。
 *
 * 同一个理由，草稿那条闸门也在这里读一次源码：它是一句 `WHERE status <> ?`，
 * 删掉之后前端不会报错、类型不会变、渲染测试只在"后端下发了草稿"的假设下才
 * 覆盖得到 —— 而那个假设正是这句 WHERE 保证不成立的。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

// __tests__ → lottery → pages → qy → features → src → web → 仓库根
const repoRoot = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..',
  '..',
  '..'
)
const goApiUser = readFileSync(
  join(repoRoot, 'qianye', 'modules', 'lottery', 'api_user.go'),
  'utf8'
)
const lotteryDir = join(dirname(fileURLToPath(import.meta.url)), '..')
const tsApi = readFileSync(join(lotteryDir, 'api.ts'), 'utf8')
const hallList = readFileSync(
  join(lotteryDir, 'components', 'lottery-hall-list.tsx'),
  'utf8'
)

/** 前端这一头声明的分区取值。从 `QyLotHallPhase` 那一行里抠出来。 */
function tsPhaseValues(): string[] {
  const line = /export type QyLotHallPhase = ([^\n]+)/.exec(tsApi)
  assert.ok(
    line,
    'api.ts 里找不到 QyLotHallPhase —— 分区取值不再是一个封闭集合'
  )
  return Array.from(line[1].matchAll(/'([a-z_]+)'/g), (m) => m[1]).sort()
}

/** 后端这一头登记的分区键。从 `hallPhases` 那个 map 字面量里抠出来。 */
function goPhaseKeys(): string[] {
  const block = /var hallPhases = map\[string\]hallPhase\{([\s\S]*?)\n\}/.exec(
    goApiUser
  )
  assert.ok(block, 'api_user.go 里找不到 hallPhases')
  return Array.from(
    block[1].matchAll(/^\t"([a-z_]+)": \{/gm),
    (m) => m[1]
  ).sort()
}

describe('大厅分区：前后端说的是同一套词', () => {
  test('取值集合逐字相同', () => {
    // 期望值在这里写死一份，而不是只比 ts === go：两侧被同一次"顺手重命名"
    // 一起改掉时，只比彼此的断言会一起变绿。
    assert.deepEqual(goPhaseKeys(), ['ended', 'live'])
    assert.deepEqual(tsPhaseValues(), ['ended', 'live'])
  })

  test('参数名是 `phase`，而且大厅真的按这个名字发出去', () => {
    assert.ok(
      goApiUser.includes('c.Query("phase")'),
      '后端不再读 `phase` —— 前端发的那个名字会被静默忽略，两张标签又变成同一份列表'
    )
    assert.match(
      hallList,
      /\n\s*phase: scope,/,
      '大厅没有把分段作为 `phase` 发出去'
    )
    assert.ok(
      !/\n\s*status: scope,/.test(hallList),
      '大厅又发起了 `status` —— 后端那个键是「精确状态」，不是分区，正是上一版的缺陷'
    )
  })

  test('未登记的取值会被后端拒绝，而不是静默返回全量', () => {
    // 老代码是一个没有 default 分支的 switch：参数名一漂移就退回全量，
    // 全链路无声。这一条守住"下一次漂移必须炸"。
    assert.ok(
      goApiUser.includes('errBadPhase'),
      'hallQuery 不再对未知分区报错：这正是上一版缺陷能活过一整个版本的原因'
    )
    assert.ok(
      !/switch c\.Query\("phase"\)/.test(goApiUser),
      '分区又回到了没有 default 分支的 switch'
    )
  })
})

describe('草稿不出现在用户端（源码级）', () => {
  test('大厅列表在 SQL 上就把草稿排除掉', () => {
    // 条件本身允许长出别的项（下架、隐藏……），但 `status <> ?` + `StatusDraft`
    // 这一对必须留在同一句 Where 里。
    assert.match(
      goApiUser,
      /Where\("status <> \?[^"]*", StatusDraft/,
      '大厅列表不再排除草稿：用户会看到一份还没有承诺、随时可能被改掉的规则'
    )
  })

  test('活动详情对草稿一律 404', () => {
    // 大厅不列出来是不够的：`act_no` 出现在任何一次分享里，详情页就是第二个入口。
    const guards = goApiUser.match(
      /if act\.Status == StatusDraft \{\s*\n\s*respondErr\(c, errActivityNotFound\)/g
    )
    assert.ok(
      guards != null && guards.length >= 2,
      `详情/资格预检里挡草稿的分支只剩 ${guards?.length ?? 0} 处（应有 2 处）`
    )
  })

  test('前端还有第二道：拿到草稿也不渲染', () => {
    assert.match(
      hallList,
      /activity\.status !== 'draft'/,
      '前端去掉了草稿过滤 —— 后端一次列表重构就能把草稿送到屏幕上'
    )
  })
})
