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
 * 按天下钻:主表的语义不许被"顺手加个天维度"换掉。
 *
 * # 为什么是下钻,不是给主表加一列
 *
 * 主表一行 = 一个人在整个区间里的消费,行数不随天数膨胀 —— 后端那个
 * 20000 行的上界因此读作"20000 个人"。把天加进主表的 GROUP BY 之后,
 * 31 天区间下同一份数据会变成人数 × 天数,上界的含义、排序键、导出的 CSV
 * 会一起换成另一张表,而运营打开这一页的第一个问题始终是"谁在花钱"。
 *
 * # 这条测试守什么
 *
 *   ① 主表那条请求**不带**任何天维度参数(带了就是上面那次换表);
 *   ② 下钻走自己那条路由,并且**必须带 user_id** —— 后端缺 user_id 会 400,
 *      而 400 在界面上表现为"点开一片空白",没人会去想是少了个参数;
 *   ③ 界面上真的有一个能点的入口(接口做完了、按钮没接,等于没做)。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import en from '../../../../../i18n/qy/en.json'
import zh from '../../../../../i18n/qy/zh.json'

const API = readFileSync(new URL('../api.ts', import.meta.url), 'utf8')
const INDEX = readFileSync(new URL('../index.tsx', import.meta.url), 'utf8')

describe('按天下钻', () => {
  test('走自己那条路由,不是往主表那条上塞参数', () => {
    assert.ok(
      API.includes("'/admin/commission/daily-consume/by-day'"),
      '下钻必须是独立路由:塞进主表那条会让 20000 行的上界从"多少人"变成"多少格"'
    )
    assert.ok(
      API.includes('qyAdminDailyConsumeByDayQuery'),
      '缺少下钻的 query 工厂'
    )
  })

  test('下钻请求必须带 user_id', () => {
    const fn = API.slice(
      API.indexOf('export function qyAdminDailyConsumeByDayQuery')
    )
    assert.ok(
      fn.includes('user_id: params.user_id'),
      '不带 user_id 的下钻在后端是 400,在界面上是一片空白 —— 而且它一旦被放行' +
        '就退化成一条全站按天的聚合,那正是会把主库拖住的形状'
    )
  })

  test('主表那条请求不带任何天维度参数', () => {
    const fn = API.slice(
      API.indexOf('function dailyConsumeQuery'),
      API.indexOf('export function qyAdminDailyConsumeQuery')
    )
    for (const key of ['group_by', 'by_day', 'granularity']) {
      assert.ok(
        !fn.includes(key),
        `主表请求带上了 ${key} —— 主表一行必须仍然是"一个人",不是"一个人的一天"`
      )
    }
  })

  test('界面上有能点开的入口,且下钻面板真的被渲染', () => {
    assert.ok(INDEX.includes('setOpenUserId'), '缺少"点开哪一行"的状态')
    assert.ok(
      INDEX.includes('<ByDayPanel'),
      '面板没被渲染 = 接口做完了但界面点不到'
    )
    assert.ok(
      INDEX.includes('qyAdminDailyConsumeByDayQuery'),
      '面板没发下钻请求'
    )
  })

  test('索引未就绪要在面板上说出来', () => {
    assert.ok(
      INDEX.includes('qy_dc_by_day_index_missing'),
      '下钻自己那条覆盖索引不在时会从百毫秒掉到数秒(备份库实测 163ms → 6523ms),' +
        '不说出来运营只会觉得"今天有点卡"'
    )
    for (const [name, dict] of [
      ['zh', zh],
      ['en', en],
    ] as const) {
      assert.ok(
        (dict as Record<string, string>).qy_dc_by_day_index_missing,
        `${name} 缺 qy_dc_by_day_index_missing`
      )
    }
  })
})
