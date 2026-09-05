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
 * 邀请这一摊上的每一个金额，必须走展示件；而且必须走**对**的那一个（D-14）。
 *
 * # 两种单位，绝不混用
 *
 *  - 额度：下线的消费基数（`base_quota` / `total_base_quota` /
 *    `pending_today_base_quota` / 日消费明细的三列）—— 只能走 `quota=`
 *    （`QyAmountText`）或 `formatQyQuota*`；
 *  - 星屑：返给邀请人的一切（`amount` / `total_stardust` / `granted` / `held` /
 *    `invite_register_stardust` / `kept_stardust`）—— 只能走 `amount=`
 *    （`QySdAmount`）或 `formatSd*`。
 *
 * 换错件比不换算更糟：`QyAmountText` 会把 500 星屑按 quota→USD→展示币种印成
 * `$0.001`，看起来完全正常，而它是错的；反过来把额度印成星屑则少了换算，
 * 3,700,000 额度会以「3,700,000 星屑」出现在用户眼前。
 *
 * 这类回退没有任何自动信号（typecheck 全绿，渲染测试也全绿），所以判据只能钉在
 * 源码结构上。扫描器沿用此前佣金那一版：从字段所在的成员表达式沿祖先链向外走，
 * 遇到的第一个有意义的节点决定结论。decimal 字符串（`gross`）不进本清单：它们
 * 只展示、由 `qy_sd_amount_with_unit` 拼上单位，与星屑页同一写法。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { parseSync } from 'oxc-parser'

// __tests__ → lib → qy
const qyDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..')

/** 按目录而不是按文件列举：新加一个组件自动进守卫。 */
const SCANNED_DIRS = [
  'pages/affiliate',
  'pages/invitees',
  'pages/invite-records',
  'pages/admin-invite',
  'pages/admin-daily-consume',
]

/** 额度口径的字段：必须渲染成与钱包余额完全一致的样子。 */
const QUOTA_FIELDS = [
  'base_quota',
  'total_base_quota',
  'pending_today_base_quota',
  'consume_quota',
  'invite_base_quota',
  'uncounted_quota',
]

/** 星屑口径的字段：整数 + 千分位 + 单位名。 */
const STARDUST_FIELDS = [
  'amount',
  'total_stardust',
  'granted',
  'held',
  'invite_register_stardust',
  'kept_stardust',
]

const QUOTA_WRAPPER_ATTRS = ['quota', 'minQuota', 'maxQuota']
const QUOTA_WRAPPER_CALLS = [
  'formatQyQuotaLedger',
  'formatQyQuotaHero',
  'qyQuotaValue',
]
const STARDUST_WRAPPER_ATTRS = ['amount']
const STARDUST_WRAPPER_CALLS = [
  'formatSd',
  'formatSdSigned',
  'formatSdWithUnit',
]

type Node = Record<string, unknown>

type Finding = {
  file: string
  field: string
  line: number
  /** `raw` = 裸渲染；`quota` / `stardust` = 被这一类换算件包住。 */
  wrapped: 'quota' | 'raw' | 'stardust'
}

function tsxFiles(): string[] {
  const out: string[] = []
  for (const dir of SCANNED_DIRS) {
    const walk = (current: string) => {
      for (const entry of readdirSync(current, { withFileTypes: true })) {
        const full = join(current, entry.name)
        if (entry.isDirectory()) {
          if (entry.name !== '__tests__') walk(full)
          continue
        }
        if (entry.name.endsWith('.tsx')) out.push(full)
      }
    }
    walk(join(qyDir, dir))
  }
  return out
}

function scan(path: string): Finding[] {
  const source = readFileSync(path, 'utf8')
  const parsed = parseSync(path, source)
  assert.deepEqual(parsed.errors, [], `解析失败：${path}`)

  const file = relative(qyDir, path).split('\\').join('/')
  const fields = new Map<string, 'quota' | 'stardust'>()
  for (const f of QUOTA_FIELDS) fields.set(f, 'quota')
  for (const f of STARDUST_FIELDS) fields.set(f, 'stardust')

  const findings: Finding[] = []
  const stack: Node[] = []

  const classify = (): Finding['wrapped'] | null => {
    for (let i = stack.length - 1; i >= 0; i--) {
      const node = stack[i]
      const parent = stack[i - 1]
      if (node.type === 'JSXAttribute') {
        const name = (node.name as Node | undefined)?.name
        if (QUOTA_WRAPPER_ATTRS.includes(name as string)) return 'quota'
        if (STARDUST_WRAPPER_ATTRS.includes(name as string)) return 'stardust'
        return 'raw'
      }
      if (node.type === 'CallExpression') {
        const callee = node.callee as Node | undefined
        const name =
          callee?.type === 'Identifier' ? (callee.name as string) : ''
        if (QUOTA_WRAPPER_CALLS.includes(name)) return 'quota'
        if (STARDUST_WRAPPER_CALLS.includes(name)) return 'stardust'
        // `t('key', { x: row.amount })` —— 插值进文案，等于裸渲染。
        if (name === 't') return 'raw'
        // 其它函数把这个值吃掉了，出来的已经不是它本身，不是展示。
        return null
      }
      // 比较 / 算术：展示的是那个三元分支的结果，不表态。
      if (
        node.type === 'BinaryExpression' ||
        node.type === 'LogicalExpression' ||
        node.type === 'UnaryExpression' ||
        node.type === 'NewExpression' ||
        node.type === 'TemplateLiteral'
      ) {
        return null
      }
      if (
        node.type === 'JSXExpressionContainer' &&
        (parent?.type === 'JSXElement' || parent?.type === 'JSXFragment')
      ) {
        return 'raw'
      }
      if (
        node.type === 'ArrowFunctionExpression' &&
        parent?.type === 'Property' &&
        ((parent.key as Node | undefined)?.name as string) === 'cell'
      ) {
        return 'raw'
      }
    }
    return null
  }

  const visit = (value: unknown) => {
    if (value == null || typeof value !== 'object') return
    if (Array.isArray(value)) {
      for (const child of value) visit(child)
      return
    }
    const node = value as Node
    if (node.type === 'MemberExpression' && node.computed !== true) {
      const property = node.property as Node | undefined
      const name =
        property?.type === 'Identifier' ? (property.name as string) : ''
      const kind = fields.get(name)
      if (kind != null) {
        const wrapped = classify()
        if (wrapped != null) {
          const before = source.slice(0, node.start as number)
          findings.push({
            file,
            field: name,
            line: before.split('\n').length,
            wrapped,
          })
        }
      }
    }
    stack.push(node)
    for (const [key, child] of Object.entries(node)) {
      if (key === 'type' || key === 'start' || key === 'end') continue
      visit(child)
    }
    stack.pop()
  }

  visit(parsed.program)
  return findings
}

const ALL = tsxFiles().flatMap(scan)

describe('邀请返星屑的金额展示', () => {
  test('扫描器真的扫到了东西', () => {
    assert.ok(
      tsxFiles().length >= 8,
      `只扫到 ${tsxFiles().length} 个 .tsx，SCANNED_DIRS 大概率已经对不上目录结构`
    )
    assert.ok(
      ALL.length >= 12,
      `只认出 ${ALL.length} 处金额渲染点，判定逻辑大概率已经失效`
    )
  })

  test('没有任何金额字段被裸渲染', () => {
    const raw = ALL.filter((f) => f.wrapped === 'raw').map(
      (f) => `${f.file}:${f.line} ${f.field}`
    )
    assert.deepEqual(
      raw,
      [],
      `以下位置直接把金额字段渲染出来了。额度要走 QyAmountText，星屑要走 QySdAmount / formatSd*：\n${raw.join('\n')}`
    )
  })

  test('额度不许走星屑件，星屑不许走额度件', () => {
    const wrongUnit = ALL.filter((f) => f.wrapped !== fieldKind(f.field)).map(
      (f) =>
        `${f.file}:${f.line} ${f.field} 走的是 ${f.wrapped} 件，应当是 ${fieldKind(f.field)} 件`
    )
    assert.deepEqual(
      wrongUnit,
      [],
      `换错了展示件。这比不换算更糟：结果看起来完全正常，而它按一个错误的系数折算过：\n${wrongUnit.join('\n')}`
    )
  })

  test('星屑侧一处都不许再引用额度换算件', () => {
    // 概览页与明细页此前是佣金页（`QyAmountText` 满屏）。它们改成星屑口径之后，
    // 唯一还该出现额度件的地方是下线消费基数那几格。
    const stardustHits = ALL.filter(
      (f) => STARDUST_FIELDS.includes(f.field) && f.wrapped === 'quota'
    )
    assert.deepEqual(stardustHits, [])
  })
})

function fieldKind(field: string): 'quota' | 'stardust' {
  return QUOTA_FIELDS.includes(field) ? 'quota' : 'stardust'
}
