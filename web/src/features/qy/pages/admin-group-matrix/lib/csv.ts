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
import type { QyGmOrphanRow } from '../types'

/**
 * 孤儿清单的 CSV 导出。
 *
 * 本轮**只给只读清单 + 导出**，不给批量修复入口：批量改写数百行
 * `tokens.group` 不可逆，而「用户当初选这个分组是有原因的」这件事系统不知道。
 * 导出的用途是让运营主动通知用户自己改，而不是替他改。
 *
 * 在前端拼而不是让后端出一个 `/export` 端点：这份数据已经在页面上了，
 * 再开一个端点等于给同一份事实两个来源，而两者迟早会在字段口径上漂开
 * （「启用」到底算不算近 30 天活跃，两边各判一次就是两个数）。
 */

/**
 * 一个 CSV 字段。
 *
 * 前导 `=+-@` 会被 Excel 当成公式执行（CSV 注入）。这份表里的令牌名与用户名
 * 都是用户自由输入的，所以必须前缀一个单引号中和掉 —— 运营用 Excel 打开一份
 * 从后台导出的表是完全正常的操作，不该因此执行别人写的东西。
 */
function csvField(value: number | string): string {
  const text = String(value)
  const guarded = /^[=+\-@\t\r]/.test(text) ? `'${text}` : text
  return `"${guarded.replaceAll('"', '""')}"`
}

/** CSV 表头。顺序即列序，与 {@link qyGmOrphansCsv} 的行构造一一对应。 */
const CSV_HEADER = [
  'category',
  'group',
  'count',
  'enabled_count',
  'active_30d',
  'reason',
  'token_id',
  'token_name',
  'key_masked',
  'user_id',
  'username',
  'accessed_time',
]

export type QyGmOrphanSection = {
  /** 分类标识，直接写进 CSV 第一列（英文常量，不走 i18n —— 它是数据不是文案）。 */
  category: string
  rows: QyGmOrphanRow[]
}

/**
 * 生成 CSV 正文。
 *
 * 每个分组一行汇总，其下每条样本再各一行（样本行的汇总列留空，避免把同一个
 * 计数重复加总）。没有样本的分组只出汇总行 —— 后端的样本数有上限，
 * 「样本为空」不等于「没有令牌」。
 */
export function qyGmOrphansCsv(sections: QyGmOrphanSection[]): string {
  const lines = [CSV_HEADER.join(',')]

  for (const section of sections) {
    for (const row of section.rows) {
      lines.push(
        [
          csvField(section.category),
          csvField(row.group),
          csvField(row.count),
          csvField(row.enabled_count),
          csvField(row.active_30d),
          csvField(row.reason),
          '',
          '',
          '',
          '',
          '',
          '',
        ].join(',')
      )
      for (const sample of row.samples) {
        lines.push(
          [
            csvField(section.category),
            csvField(row.group),
            '',
            '',
            '',
            '',
            csvField(sample.token_id),
            csvField(sample.token_name),
            csvField(sample.key_masked),
            csvField(sample.user_id),
            csvField(sample.username),
            csvField(sample.accessed_time),
          ].join(',')
        )
      }
    }
  }

  return lines.join('\n')
}

/**
 * 触发浏览器下载。
 *
 * BOM 是必须的：不带它，Excel 会用系统 ANSI 码页打开这份 UTF-8 文件，
 * 中文分组名（站里的分组名基本都是中文）会变成一片乱码，而运营会以为是
 * 数据坏了。
 */
export function qyGmDownloadCsv(filename: string, content: string): void {
  const blob = new Blob([`﻿${content}`], {
    type: 'text/csv;charset=utf-8;',
  })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.click()
  URL.revokeObjectURL(url)
}
