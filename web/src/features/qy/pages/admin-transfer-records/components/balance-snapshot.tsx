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
import { QyAmountText } from '../../../components/qy-amount-text'

type QyBalanceSnapshotProps = {
  /** 「转出」/「收款」之类的行首标注。 */
  label: string
  /** 扣款前的 quota 整数（后端原样下发，前端只负责展示口径）。 */
  before: number
  /** 扣款后的 quota 整数。 */
  after: number
}

/**
 * 划转流水里的「扣之前 → 扣之后」一行。
 *
 * ── 为什么不再直接印 quota 整数 ──
 * 这一列此前把后端下发的 quota 原样打出来（`1500000 → 1000000`）。管理员在这张
 * 表上做的事是仲裁争议，而争议双方谈的是钱，不是 token 计数 —— 每看一行都要
 * 心算一次 `÷ quotaPerUnit` 是逼着人算错。改成与钱包页、金额列**同一套**展示
 * 口径（`QyAmountText` → `formatQuotaWithCurrency`），换算与小数位都不在这里
 * 实现：站内出现第二套汇率/精度必然与钱包页对不上。
 *
 * ── 原始值没有丢 ──
 * 挂在 `title` 上，鼠标停一下就能看到 `before → after` 的 quota 整数。
 * 仲裁到最后要和后端账本逐位对，那时需要的是没经过任何换算的数；但它不该是
 * **主显示** —— 项目方的原话就是"转成 USD 显示不要显示这个原始值"。
 *
 * 换算只发生在展示层：后端下发的、以及本页发回去的，始终是 quota 整数。
 */
export function QyBalanceSnapshot(props: QyBalanceSnapshotProps) {
  return (
    <div
      className='flex items-center gap-1.5 whitespace-nowrap'
      title={`${props.before} → ${props.after}`}
    >
      <span className='text-muted-foreground w-8 shrink-0 text-[10px] tracking-wider uppercase'>
        {props.label}
      </span>
      <QyAmountText quota={props.before} className='text-xs' />
      <span className='text-muted-foreground text-xs' aria-hidden='true'>
        →
      </span>
      <QyAmountText quota={props.after} className='text-xs' />
    </div>
  )
}
