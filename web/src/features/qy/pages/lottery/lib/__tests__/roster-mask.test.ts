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
import { createHash } from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { describe, test } from 'node:test'

import type { QyLotProofEntry } from '../../types'
import { qyLotMaskRef } from '../display'
import { qyLotChainNext, qyLotRosterHash } from '../verify'

/**
 * 公开名单的展示层打码 —— **以及"打完码之后哈希链还能被第三方复算"这条硬约束**。
 *
 * ## 这里要证的到底是什么
 *
 * 打码如果哪天"顺手"渗进了证据链的原像，后果不是报错，而是**整套公正性协议
 * 静默作废**：名单哈希与链尾都会变成另一串值，任何拿着已公布的 `roster_hash`
 * 去复算的人都会得到不匹配，而平台这边看起来一切正常。
 *
 * 所以下面三条一起证明它没有：
 *
 *   1. 打码函数确实改变了值（否则"打了码"是一句空话）；
 *   2. 用**原值**算出来的 `roster_hash` / `chain_hash` 等于一份在本文件里
 *      独立重写的 sha256 期望值（不是从被测实现回读的）；
 *   3. 用**打了码的值**算出来的那两个哈希与之不同 —— 也就是说渗漏一旦发生，
 *      验证会当场红，而不是悄悄通过。
 *
 * 第四条是结构性的：`lib/verify.ts` 不许引用打码函数。上面三条只能证明"此刻
 * 没渗漏"，这一条挡的是下一个人顺手把它接进去。
 */

const SEP = '\u001F'

/** 本文件自己的一份 sha256，按协议文档拼原像 —— 不调被测实现的任何一行。 */
function digest(parts: string[]): string {
  return createHash('sha256').update(parts.join(SEP), 'utf8').digest('hex')
}

const ACT_NO = 'LA-20260825-01'
const COMMIT = 'a'.repeat(64)

const ENTRIES: QyLotProofEntry[] = [
  {
    seq: 1,
    entry_no: 'LE20260824-69dbcf45d7d51875',
    user_ref: '3f2a1c9e40b3d61885a70cbb14e2d907',
    opt_no: 0,
    amount: 500_000,
    status: 'confirmed',
    prev_hash: '',
    chain_hash: '',
    order_no: 'FO-1',
    pick: '03,09,12,17,22,31|05',
  },
  {
    seq: 2,
    entry_no: 'LE20260824-7f2a1c9e40b3d618',
    user_ref: '90c11de4477b2a6630fe58c1d2043aa5',
    opt_no: 0,
    amount: 500_000,
    status: 'confirmed',
    prev_hash: '',
    chain_hash: '',
    order_no: 'FO-2',
    pick: '01,02,04,05,06,07|01',
  },
]

/** 名单在展示层被打码之后的那一份（**绝不应该**出现在任何原像里）。 */
const MASKED: QyLotProofEntry[] = ENTRIES.map((entry) => ({
  ...entry,
  entry_no: qyLotMaskRef(entry.entry_no),
  user_ref: qyLotMaskRef(entry.user_ref),
}))

describe('qyLotMaskRef', () => {
  test('保留首 6 位与末 4 位，中间遮住', () => {
    assert.equal(
      qyLotMaskRef('3f2a1c9e40b3d61885a70cbb14e2d907'),
      '3f2a1c…d907'
    )
    assert.equal(qyLotMaskRef('LE20260824-69dbcf45d7d51875'), 'LE2026…1875')
  })

  test('用户仍然认得出自己那一行，旁人却归并不了两行', () => {
    // 报名回执上印的是完整单号，首 6 位 + 末 4 位足够本人确认"这一行是我的"。
    const mine = qyLotMaskRef(ENTRIES[0]!.entry_no)
    assert.ok(ENTRIES[0]!.entry_no.startsWith(mine.slice(0, 6)))
    assert.ok(ENTRIES[0]!.entry_no.endsWith(mine.slice(-4)))
    // 同一场里的两个不同标识打码之后仍然不同 —— 打到全都一样就等于把名单
    // 变成了一堆无法核对的省略号。
    assert.notEqual(
      qyLotMaskRef(ENTRIES[0]!.user_ref),
      qyLotMaskRef(ENTRIES[1]!.user_ref)
    )
  })

  test('短到不够打码的串原样返回', () => {
    // 截断一个 8 字符的串只会让它既不可读也不安全。
    assert.equal(qyLotMaskRef('LE-1'), 'LE-1')
    assert.equal(qyLotMaskRef('abcdefghijkl'), 'abcdefghijkl')
    assert.equal(qyLotMaskRef(''), '')
  })
})

describe('打码之后证据链仍可复算', () => {
  test('名单哈希用的是原值，且等于独立算出的期望', async () => {
    const lines = ENTRIES.map((entry) =>
      [
        entry.entry_no,
        entry.user_ref,
        String(entry.opt_no),
        String(entry.amount),
        entry.pick ?? '',
      ].join(SEP)
    )
    const expected = digest([
      'qylot-roster-v2',
      ACT_NO,
      COMMIT,
      String(ENTRIES.length),
      lines.join('\n'),
    ])

    const actual = await qyLotRosterHash(ACT_NO, COMMIT, ENTRIES, 'lot-v2')
    assert.equal(
      actual,
      expected,
      '名单哈希与协议文档对不上 —— 第三方拿公示的 roster_hash 复算会失败'
    )

    // 打了码的那一份算出来必然是另一个值：渗漏一旦发生，验证会当场红，
    // 而不是悄悄通过。
    const leaked = await qyLotRosterHash(ACT_NO, COMMIT, MASKED, 'lot-v2')
    assert.notEqual(leaked, expected)
  })

  test('链哈希同样用原值', async () => {
    const entry = ENTRIES[0]!
    const expected = digest([
      'qylot-chain-v2',
      '',
      ACT_NO,
      String(entry.seq),
      entry.entry_no,
      entry.user_ref,
      String(entry.opt_no),
      String(entry.amount),
      entry.pick ?? '',
    ])

    assert.equal(
      await qyLotChainNext('', ACT_NO, entry, 'lot-v2'),
      expected,
      '链哈希与协议文档对不上'
    )
    assert.notEqual(
      await qyLotChainNext('', ACT_NO, MASKED[0]!, 'lot-v2'),
      expected
    )
  })

  test('verify.ts 不许引用打码函数', () => {
    /*
      上面两条只能证明"此刻没渗漏"。这一条挡的是下一个人：把 `qyLotMaskRef`
      接进复算路径不会有任何编译期信号，而它的后果是整套公正性协议静默作废。
      验证模块从头到尾只该看见接口下发的原值。
    */
    const source = fs.readFileSync(
      path.join(import.meta.dirname, '..', 'verify.ts'),
      'utf8'
    )
    assert.ok(
      !source.includes('qyLotMaskRef'),
      'verify.ts 引用了打码函数 —— 展示层的东西渗进了证据链原像'
    )
    assert.ok(
      !source.includes('mask'),
      'verify.ts 里出现了 mask 字样，请确认没有把展示层的处理接进复算'
    )
  })
})
