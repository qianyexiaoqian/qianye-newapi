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

import { QY_COMMISSION_FIELDS, QY_MAX_QUOTA } from '../fields'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = join(here, '..', '..', '..', '..', '..', '..', '..', '..')

/**
 * 佣金页的额度上界是**抄**过来的一个数,而不是后端下发的。
 *
 * 划转与抽奖两页从接口拿 bounds,佣金页没有那条通道 —— 于是 `QY_MAX_QUOTA`
 * 是全站唯一一处把后端 `common.MaxQuota` 硬抄进前端的地方。抄下来的数会漂:
 * 本轮把 MaxQuota 从 2^31-1 抬到 2^43 时,这里若不同步,界面会在一个早就
 * 合法的数字上标红,而运营找不到任何配置能放开它。
 *
 * 所以这条测试直接去读 Go 源文件,把两边钉在一起。它读的是常量声明本身,
 * 不是某个中间产物 —— 后端一改,这里当场红。
 */
describe('QY_MAX_QUOTA 与后端 common.MaxQuota 同步', () => {
  test('数值逐字一致', () => {
    const go = readFileSync(join(repoRoot, 'common', 'quota_math.go'), 'utf8')
    const match = go.match(/MaxQuota\s*=\s*1\s*<<\s*(\d+)/)
    assert.ok(
      match,
      'common/quota_math.go 里找不到 `MaxQuota = 1 << N` 形态的声明 —— ' +
        '后端改了写法就必须同步改这条断言,而不是把它删掉'
    )
    const backend = 2 ** Number(match![1])
    assert.equal(
      QY_MAX_QUOTA,
      backend,
      `前端 QY_MAX_QUOTA=${QY_MAX_QUOTA} 与后端 common.MaxQuota=${backend} 不一致`
    )
  })

  test('落在 JS 的精确整数区间内', () => {
    // 额度是以 JSON 数字下发的,越过 MAX_SAFE_INTEGER 之后前端读到的就不再是
    // 后端写下的那个数 —— 那会让"界面说 OK、后端拒绝"变成一类无法解释的现象。
    assert.ok(Number.isSafeInteger(QY_MAX_QUOTA))
  })

  test('每一个 quota 类字段的 max 都取自这个常量', () => {
    const quotaFields = Object.values(QY_COMMISSION_FIELDS).filter(
      (meta) => meta.unit === 'quota'
    )
    assert.ok(
      quotaFields.length > 0,
      '佣金页必须至少有一个额度类字段,否则这条断言是空转'
    )
    for (const meta of quotaFields) {
      assert.equal(meta.max, QY_MAX_QUOTA)
    }
  })
})
