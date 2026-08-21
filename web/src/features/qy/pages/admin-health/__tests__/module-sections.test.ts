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
import { describe, test } from 'node:test'

/**
 * 「扩展健康 → 模块配置段」这张表的展示契约。
 *
 * 这张表存在的**唯一**理由是「界面上写的状态可以被相信」——「配置里根本没有
 * 这一段」与「显式写了 enabled: false」在后端是同一个字节，它是它们唯一的区别。
 * 因此它自己说错话的代价比它不存在还大：排障的人会照着它去改一个不存在的开关。
 *
 * 这里钉的是两条已经踩过的：
 *
 *  1. 一个模块可能占多行（violation 有总开关 + 两个二级开关），行 key 必须带上
 *     开关名，否则同一个模块的几行共用一个 React key。
 *  2. 修复片段必须连「往哪儿粘」一起说：missing_key 的前提就是那一段已经在文件
 *     里了，把片段当成新的顶层段追加会产生重复的顶层 YAML 键 —— 配置从此解析
 *     失败、整台网关起不来。一条「不阻断启动」的告警，其修复指引把网关关停了。
 */
const page = readFileSync(new URL('../index.tsx', import.meta.url), 'utf-8')

describe('模块配置段表', () => {
  test('行 key 带上开关名 —— 一个模块会占多行', () => {
    assert.match(
      page,
      /getRowKey=\{\(row\) => `\$\{row\.module\}\.\$\{row\.key\}`\}/,
      'violation 的三行（enabled / precheck_enabled / post_charge_enabled）' +
        '会共用同一个 React key'
    )
  })

  test('修复片段分状态给出粘贴位置', () => {
    assert.ok(
      page.includes("row.state === 'missing_key'"),
      '两种 missing 状态用了同一句提示 —— 而它们给的片段不是同一种东西'
    )
    assert.ok(
      page.includes('qy_cfg_health_module_fix_key') &&
        page.includes('qy_cfg_health_module_fix_section'),
      '缺少「往哪儿粘」的说明：只丢一个 YAML 片段给运维，' +
        '照着追加就是重复顶层键 → 配置解析失败 → 网关起不来'
    )
  })

  test('顶部告警列的是开关路径而不是模块名', () => {
    // 只报模块名的话，运维补上 violation.enabled 就以为完事了，而真正决定
    // 「抓不抓」的是段内那两个二级开关，它们各自还是关着的。
    assert.match(
      page,
      /\.map\(\(m\) => `\$\{m\.section\}\.\$\{m\.key\}`\)/,
      '告警里列的是模块名，看不出到底是哪一个开关没写'
    )
  })
})

describe('透支总览卡', () => {
  // 后端 qianye/overdraft/overdraft.go 的 Report.TotalOwed 已经是取过反的正数
  // （代码与注释都写死 `= -SUM(quota)`，恒 >= 0），前端**不能**再取一次反。
  // 取反之后界面上写的是「合计欠额 −$3,813.8」，而紧挨着的「欠得最深」
  // （deepest.quota，库里本来就是负的）是「−$3,803.5」——两个同符号同量级的数
  // 并排，会被直接读成同一个数。实测库里有 20 个负余额账号，这张卡是出数的。
  test('合计欠额原样渲染，不再取一次反', () => {
    assert.doesNotMatch(
      page,
      /quota=\{-overdraftQuery\.data\.total_owed\}/,
      'total_owed 下发的就是正数，再取反会在界面上写出一个负的欠款额'
    )
    assert.match(
      page,
      /quota=\{overdraftQuery\.data\.total_owed\}/,
      '合计欠额必须原样渲染'
    )
  })

  test('方向靠标签与颜色表达，不借负数的红色语义', () => {
    assert.match(
      page,
      /overdraftQuery\.data\.total_owed > 0\s*\?\s*'text-warning'/,
      '去掉取反之后这张卡不再是「负数红」，需要显式给告警色，' +
        '否则一个纯正数看上去与正常收入无异'
    )
  })
})
