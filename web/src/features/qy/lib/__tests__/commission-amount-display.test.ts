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
 * 佣金这一摊上的每一个金额，必须走展示件；而且必须走**对**的那一个。
 *
 * # 为什么需要源码级守卫
 *
 * 项目方原话：「当前佣金审核的金额显示全部改成显示站内余额，不要显示什么值，
 * 乱的很。」乱在同一屏上摆着两种写法：`available_quota` 走
 * `QyAmountText`（渲染成 `$0.27`），而 `gross_amount` 直接把后端的
 * `decimal(30,10)` 字符串印出来（`1370.0000000000`）—— 它们其实是**同一个
 * 单位**，看的人却完全没法判断这两列能不能相加。
 *
 * 这类回退没有任何自动信号：`bun run typecheck` 全绿（`{row.gross_amount}`
 * 是合法的 ReactNode），渲染测试也全绿（文字确实出现了）。所以判据只能钉在
 * 源码结构上。
 *
 * # 两条独立的断言
 *
 *  1. **不许裸渲染**：金额字段出现在渲染位置（JSX 子节点、`cell:` 的箭头返回
 *     值、`t()` 的插值对象）时，祖先链上必须有一个认可的换算件。
 *  2. **不许换错件**：额度口径只能走 `quota=` / `formatQyQuota*`，法币口径只能
 *     走 `QyFiatText` 的 `amount=`。这一条比第 1 条更要紧 —— 一个按错误系数
 *     换算过的金额看起来完全正常，而它是错的：法币是按**计佣当刻冻结的汇率**
 *     算出来的绝对值，再走一次 quota→USD→展示币种就是双重换算，用户看到的数字
 *     与实际到账对不上。
 *
 * # 单位从哪来的（这份清单是本测试的全部前提）
 *
 *  - `base_quota` / `available_quota` / `frozen_quota` / `withdrawn_quota` /
 *    `total_earned_quota` / `total_clawback_quota` / `ledger_drift`：站内额度整数。
 *  - `gross_amount` / `settled_amount` / `unsettled_amount` /
 *    `pending_mature_quota` / `total_commission`：**同样是站内额度**，只是
 *    `decimal(30,10)` 的精确值、后端以字符串下发。依据：
 *    `qianye/modules/commission/accrual.go` 的 `calcGross`
 *    （`gross = base_quota × rate_units / 10000`）与 `settle.go` 的
 *    `computeSettlement`（`floor(carry + Δgross)` 直接加进 `available_quota`）。
 *  - `available_fiat` / 提现单的 `gross_amount` / `fee_amount` / `net_amount` /
 *    `min_amount`：**法币**，按冻结汇率折算（`settle.go` 的 `applyFiat`
 *    是 `net / QuotaPerUnit × 冻结汇率`）。
 *  - `rate_bps` / `fee_bps` / `preview_fx_rate` 是费率，不是金额，不进本清单。
 *
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { parseSync } from 'oxc-parser'

// __tests__ → lib → qy
const qyDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..')

/**
 * 扫描范围：整个佣金 / 提现的渲染面。
 *
 * 按**目录**而不是按文件列举，这样新加一个组件自动进守卫 —— 按文件列举的话，
 * 下一个人新建 `components/xxx.tsx` 就落在守卫外面，而那正是本仓反复出现的
 * 「清单没跟上」形状。
 */
const SCANNED_DIRS = [
  'pages/admin-commission',
  'pages/admin-commission-balances',
  'pages/admin-commission-records',
  'pages/admin-commission-relations',
  'pages/admin-commission-users',
  'pages/affiliate',
  'pages/invitees',
  'pages/withdraw',
  'pages/withdrawals',
  'pages/admin-withdrawals',
]

/** 站内额度口径的字段：必须渲染成与钱包余额完全一致的样子。 */
const QUOTA_FIELDS = [
  'base_quota',
  'total_base_quota',
  'available_quota',
  'frozen_quota',
  'withdrawn_quota',
  'total_earned_quota',
  'total_clawback_quota',
  'derived_available_quota',
  'pending_mature_quota',
  'unsettled_amount',
  'settled_amount',
  'total_commission',
  'total_commission_quota',
  'min_settle_quota',
  'min_quota',
  'withdrawable_quota',
  'ledger_drift',
]

/** 法币口径的字段：只能走 `QyFiatText`。 */
const FIAT_FIELDS = ['available_fiat', 'fee_amount', 'net_amount', 'min_amount']

/**
 * `gross_amount` 在两个模块里是两种钱。
 *
 * 计佣行上它是额度（`calcGross`）；提现单上它是法币毛额（`withdraw/view.go`
 * 按冻结汇率算出来的 `gross/fee/net` 三件套）。同名不同义正是最容易换错件的
 * 地方，所以按目录分派而不是按字段名一刀切。
 */
const FIAT_SCOPES = [
  'pages/withdraw',
  'pages/withdrawals',
  'pages/admin-withdrawals',
]

/**
 * 认可的额度换算件。
 *
 * `quota=` 是 `QyAmountText` 的入口；`minQuota=` / `maxQuota=` 是
 * `QyAmountInput` 的上下界，那个组件内部同样过 `formatQyQuotaLedger`。
 */
const QUOTA_WRAPPER_ATTRS = ['quota', 'minQuota', 'maxQuota']
const QUOTA_WRAPPER_CALLS = [
  'formatQyQuotaLedger',
  'formatQyQuotaHero',
  'qyQuotaValue',
  // 返佣配置页那一条**精确** USD 通道（`lib/quota-usd.ts`）：门槛存的是整数
  // 额度，界面换算成 USD 再换回来必须逐位相同，所以它刻意不走展示币种链路。
  // 它同样是"换算过"的，不是裸数字。
  'qyFormatQuotaAsUsd',
  'qyQuotaToUsdText',
]
/** 认可的法币换算件。`amount=` 是 `QyFiatText` 的入口。 */
const FIAT_WRAPPER_ATTRS = ['amount']

type Node = Record<string, unknown>

type Finding = {
  file: string
  field: string
  line: number
  /** `raw` = 裸渲染；`quota` / `fiat` = 被这一类换算件包住。 */
  wrapped: 'raw' | 'quota' | 'fiat'
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

/**
 * 找出一个文件里**渲染位置**上的金额字段，并判定它被哪一类换算件包着。
 *
 * 判定方式是从字段所在的成员表达式沿祖先链**向外**走，遇到的第一个有意义的
 * 节点决定结论：
 *
 *   - `quota={…}` / `amount={…}` 属性、`formatQyQuota*(…)` 调用 → 已换算；
 *   - 作为 JSX 子节点的 `{…}`、`cell:` 箭头函数的返回值、`t()` 的插值对象
 *     → 裸渲染；
 *   - 值先被别的运算吃掉了（`row.ledger_drift === 0` 的比较、
 *     `Math.floor(Number(balance.unsettled_amount))` 的算上限、
 *     `fiat.min_amount !== ''` 的存在性判断）→ 那不是在展示这个数，不表态；
 *   - 一路走到顶也没遇到渲染位置 → 同样不表态。
 *
 * 「不表态」这两支是刻意的：把比较与算术也报成缺陷，会逼着下一个人给守卫开
 * 白名单，而白名单一旦开口，真正的裸渲染也会被塞进去。
 */
function scan(path: string): Finding[] {
  const source = readFileSync(path, 'utf8')
  const parsed = parseSync(path, source)
  assert.deepEqual(parsed.errors, [], `解析失败：${path}`)

  const file = relative(qyDir, path).split('\\').join('/')
  const fiatScope = FIAT_SCOPES.some((scope) => file.startsWith(scope))
  const fields = new Map<string, 'quota' | 'fiat'>()
  for (const f of QUOTA_FIELDS) fields.set(f, 'quota')
  for (const f of FIAT_FIELDS) fields.set(f, 'fiat')
  fields.set('gross_amount', fiatScope ? 'fiat' : 'quota')

  const findings: Finding[] = []
  const stack: Node[] = []

  const classify = (): Finding['wrapped'] | null => {
    for (let i = stack.length - 1; i >= 0; i--) {
      const node = stack[i]
      const parent = stack[i - 1]
      if (node.type === 'JSXAttribute') {
        const name = (node.name as Node | undefined)?.name
        if (QUOTA_WRAPPER_ATTRS.includes(name as string)) return 'quota'
        if (FIAT_WRAPPER_ATTRS.includes(name as string)) return 'fiat'
        return 'raw'
      }
      if (node.type === 'CallExpression') {
        const callee = node.callee as Node | undefined
        const name =
          callee?.type === 'Identifier' ? (callee.name as string) : ''
        if (QUOTA_WRAPPER_CALLS.includes(name)) return 'quota'
        // `t('key', { x: row.gross_amount })` —— 插值进文案，等于裸渲染。
        if (name === 't') return 'raw'
        // 其它函数（`Number(…)`、`Math.floor(…)`、`String(…)`）把这个值吃掉了，
        // 出来的已经不是它本身，不是展示。
        return null
      }
      // 比较 / 算术同理：`row.ledger_drift === 0` 展示的是那个三元分支的结果。
      if (
        node.type === 'BinaryExpression' ||
        node.type === 'LogicalExpression' ||
        node.type === 'UnaryExpression' ||
        node.type === 'NewExpression'
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

describe('佣金与提现的金额展示', () => {
  test('扫描器真的扫到了东西', () => {
    // 一个扫不到任何东西的守卫会永远全绿。这两个下界对着当前的渲染面，
    // 少一大截就说明目录改名 / 解析失效，而不是"缺陷被修好了"。
    assert.ok(
      tsxFiles().length >= 15,
      `只扫到 ${tsxFiles().length} 个 .tsx，SCANNED_DIRS 大概率已经对不上目录结构`
    )
    assert.ok(
      ALL.length >= 25,
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
      '以下位置直接把金额字段渲染出来了。额度要走 QyAmountText / formatQyQuota*，' +
        '法币要走 QyFiatText —— 裸渲染的表现是同一屏上 `$0.27` 与 ' +
        '`1370.0000000000` 并排，而它们是同一种钱：\n' +
        raw.join('\n')
    )
  })

  test('额度不许走法币件，法币不许走额度件', () => {
    const wrongUnit: string[] = []
    for (const finding of ALL) {
      let expected: 'quota' | 'fiat' = 'quota'
      if (finding.field === 'gross_amount') {
        expected = FIAT_SCOPES.some((scope) => finding.file.startsWith(scope))
          ? 'fiat'
          : 'quota'
      } else if (FIAT_FIELDS.includes(finding.field)) {
        expected = 'fiat'
      }
      if (finding.wrapped !== expected) {
        wrongUnit.push(
          `${finding.file}:${finding.line} ${finding.field} 走的是 ${finding.wrapped} 件，应当是 ${expected} 件`
        )
      }
    }
    assert.deepEqual(
      wrongUnit,
      [],
      '换错了展示件。这比不换算更糟：结果看起来完全正常，而它按一个错误的系数' +
        '折算过 —— 法币走额度件会被当前汇率再乘一遍，额度走法币件则完全不换算：\n' +
        wrongUnit.join('\n')
    )
  })

  test('计佣流水的三个额度列走的是同一个展示件', () => {
    // 项目方点名的那一屏。基数、佣金、已结算三列同单位，任何一列掉队都会
    // 让运营以为这三个数不能互相比较。
    const page = ALL.filter(
      (f) => f.file === 'pages/admin-commission-records/index.tsx'
    )
    for (const field of ['base_quota', 'gross_amount', 'settled_amount']) {
      const hit = page.filter((f) => f.field === field)
      assert.ok(
        hit.length > 0,
        `佣金审核页不再渲染 ${field}？列被删了还是改名了`
      )
      for (const f of hit) {
        assert.equal(f.wrapped, 'quota', `${f.file}:${f.line} ${field}`)
      }
    }
  })
})

/*
 * ── 变异验证 ─────────────────────────────────────────────────────────
 *
 * 把这四处任意一处改回裸写法，上面对应的用例必须变红（各实测过一次）：
 *
 *  1. `pages/admin-commission-records/index.tsx`
 *     `cell: (row) => <QyAmountText quota={row.gross_amount} />`
 *     → `cell: (row) => row.gross_amount`
 *     ⇒「没有任何金额字段被裸渲染」红，报 `gross_amount`（cell 箭头返回值）。
 *
 *  2. `pages/admin-commission-balances/components/adjust-commission-dialog.tsx`
 *     `<QyAmountText quota={balance.unsettled_amount} />`
 *     → `{balance.unsettled_amount}`
 *     ⇒ 同上，报 JSX 子节点位置。
 *
 *  3. `pages/affiliate/index.tsx`
 *     `carry: formatQyQuotaLedger(summary.unsettled_amount)`
 *     → `carry: summary.unsettled_amount`
 *     ⇒ 同上，报 `t()` 插值位置 —— 这一支单靠 JSX 扫描是看不见的。
 *
 *  4. `pages/withdrawals/components/withdrawal-detail-dialog.tsx`
 *     `<QyFiatText amount={withdrawal.net_amount} …/>`
 *     → `<QyAmountText quota={withdrawal.net_amount} />`
 *     ⇒「额度不许走法币件」红。这一条是四条里唯一**看不出来**的错法：
 *        页面照样渲染出一个像模像样的金额，只是它被当前汇率又乘了一遍。
 */
