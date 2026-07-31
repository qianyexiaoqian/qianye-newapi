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
/**
 * 十进制字符串的解析与比较。
 *
 * **这里刻意没有乘法。** 最终生效价（`分组级价 × 分组倍率`）由后端
 * `qianye/modules/grouppricing/effective.go` 计算并以字符串下发，前端再算一遍
 * 只会制造第二个真相：两处只要有一处漏乘分组倍率或多舍一位，管理端显示的
 * 数字就与实际扣费不一致 —— 那比不显示更糟。
 *
 * 前端只需要回答一个后端没直接给的问题：**这次改价是涨还是跌**。涨跌决定
 * 颜色与箭头，而颜色是运营扫一眼列表时唯一会看的东西，所以它必须用十进制
 * 精确比较得出，不能靠 `Number()` 之后比大小 —— `0.1 + 0.2 > 0.3` 在
 * IEEE754 下是 `true`。
 */

/**
 * 定点十进制数：`sign * digits / 10^scale`。
 *
 * `digits` 恒为非负，`scale` 恒为非负整数，`digits === 0n` 时 `sign` 恒为 1
 * （不存在 `-0`，否则比较要多一个分支）。
 */
export type QyDecimal = {
  sign: -1 | 1
  digits: bigint
  scale: number
}

/**
 * 允许的十进制字面量。
 *
 * 接受科学计数法：万一后端某天把某个字段改回 float64，`String(1e-7)` 就是
 * `'1e-7'`，拒收它会把一批合法配置显示成「无法解析」。
 */
const DECIMAL_PATTERN = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/

const ZERO: QyDecimal = { sign: 1, digits: 0n, scale: 0 }

function pow10(exponent: number): bigint {
  return 10n ** BigInt(exponent)
}

/**
 * 解析十进制字面量。**无法解析一律返回 `null`，不做任何猜测。**
 *
 * 空串同样是 `null`：后端用字段缺省表示「该口径未配置」，把它当 0 会把
 * 「没配价格」显示成「免费」。
 */
export function qyParseDecimal(
  raw: number | string | null | undefined
): QyDecimal | null {
  if (raw == null) return null

  let text: string
  if (typeof raw === 'number') {
    if (!Number.isFinite(raw)) return null
    text = String(raw)
  } else {
    text = raw.trim()
  }
  if (text === '' || !DECIMAL_PATTERN.test(text)) return null

  const expIndex = text.search(/[eE]/)
  const mantissa = expIndex < 0 ? text : text.slice(0, expIndex)
  const exponent = expIndex < 0 ? 0 : Number(text.slice(expIndex + 1))
  if (!Number.isFinite(exponent) || Math.abs(exponent) > 1000) return null

  let sign: -1 | 1 = 1
  let body = mantissa
  if (body.startsWith('-')) {
    sign = -1
    body = body.slice(1)
  } else if (body.startsWith('+')) {
    body = body.slice(1)
  }

  const dotIndex = body.indexOf('.')
  const intPart = dotIndex < 0 ? body : body.slice(0, dotIndex)
  const fracPart = dotIndex < 0 ? '' : body.slice(dotIndex + 1)
  const joined = `${intPart}${fracPart}`

  let digits = BigInt(joined === '' ? '0' : joined)
  let scale = fracPart.length - exponent
  if (scale < 0) {
    digits *= pow10(-scale)
    scale = 0
  }
  if (digits === 0n) return ZERO
  return { sign, digits, scale }
}

/** `a` 与 `b` 的大小关系：-1 / 0 / 1。对齐 scale 后用 BigInt 比较，无舍入。 */
function qyDecimalCompare(a: QyDecimal, b: QyDecimal): -1 | 0 | 1 {
  const scale = Math.max(a.scale, b.scale)
  const left = BigInt(a.sign) * a.digits * pow10(scale - a.scale)
  const right = BigInt(b.sign) * b.digits * pow10(scale - b.scale)
  if (left < right) return -1
  if (left > right) return 1
  return 0
}

/** 涨跌方向。`up` = 变贵（用户被多扣），`down` = 变便宜。 */
export type QyPriceDirection = 'down' | 'flat' | 'up'

/**
 * 两个十进制字符串的涨跌方向。
 *
 * 任一侧无法解析 → `null`，由调用方渲染「无可比基准」而不是画一个 `flat`
 * 箭头 —— 那会被读成「这次改价没有影响」。
 */
export function qyCompareDecimalStrings(
  before: string | null | undefined,
  after: string | null | undefined
): QyPriceDirection | null {
  const parsedBefore = qyParseDecimal(before)
  const parsedAfter = qyParseDecimal(after)
  if (parsedBefore == null || parsedAfter == null) return null

  const compared = qyDecimalCompare(parsedAfter, parsedBefore)
  if (compared > 0) return 'up'
  if (compared < 0) return 'down'
  return 'flat'
}

/**
 * 后端的涨跌幅字符串（形如 `-25.00`）→ 带符号的展示文案。
 *
 * 原样使用后端算好的百分比，不用前端的两个数字重新相除：那是第二个真相。
 * 空 / 不合法一律返回空串，由调用方决定退化成什么。
 */
export function qyFormatDeltaPercent(raw: string | null | undefined): string {
  const parsed = qyParseDecimal(raw)
  if (parsed == null) return ''
  const text = (raw ?? '').trim()
  return parsed.sign > 0 && parsed.digits !== 0n ? `+${text}%` : `${text}%`
}

/**
 * quota 差额的方向。对账视图用它决定「多收 / 少收 / 持平」。
 *
 * 与价格方向分开：这里的输入是已经发生的扣费差额（整数 quota），
 * 不需要十进制运算。
 */
export function qyQuotaDirection(delta: number): QyPriceDirection {
  if (!Number.isFinite(delta) || delta === 0) return 'flat'
  return delta > 0 ? 'up' : 'down'
}
