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
import { Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { formatQyQuotaLedger } from '../../../lib/format'
import { parseQyLotRules } from '../lib/rules'

/**
 * 把规范化 JSON 的参与条件变成人话。
 *
 * ## 为什么不直接把 `rules_text` 原文贴出来
 *
 * 它是给验证脚本吃的字节，不是给人读的。用户要判断的是"我够不够格"，
 * 而不是 `{"min_used_quota":100000}` 是什么意思。
 *
 * ## 为什么解析失败时贴原文
 *
 * 条件是**已经进了承诺哈希**的东西，不能因为前端读不懂就不显示 ——
 * 那会让"公示"变成"我们展示了我们能展示的部分"。读不懂就把原文摆出来，
 * 用户至少还能自己看、能截图、能拿去对哈希。
 */
export function QyLotRulesList(props: { rulesText: string }) {
  const { t } = useTranslation()

  const rules = parseQyLotRules(props.rulesText)
  if (rules == null) {
    return (
      <pre className='bg-muted/40 overflow-x-auto rounded-lg p-3 text-xs break-all whitespace-pre-wrap'>
        {props.rulesText}
      </pre>
    )
  }

  const lines: string[] = []
  if (rules.allow_groups.length > 0) {
    lines.push(
      t('qy_lot_rule_group_allow', { groups: rules.allow_groups.join('、') })
    )
  }
  if (rules.deny_groups.length > 0) {
    lines.push(
      t('qy_lot_rule_group_deny', { groups: rules.deny_groups.join('、') })
    )
  }
  if (rules.min_account_age_days > 0) {
    lines.push(
      t('qy_lot_rule_account_age', { days: rules.min_account_age_days })
    )
  }
  if (rules.min_quota > 0) {
    lines.push(
      t('qy_lot_rule_min_quota', {
        amount: formatQyQuotaLedger(rules.min_quota),
      })
    )
  }
  if (rules.min_used_quota > 0) {
    lines.push(
      t('qy_lot_rule_used_quota', {
        amount: formatQyQuotaLedger(rules.min_used_quota),
      })
    )
  }
  if (rules.recent_spend_quota > 0 && rules.recent_spend_days > 0) {
    lines.push(
      t('qy_lot_rule_recent_spend', {
        days: rules.recent_spend_days,
        amount: formatQyQuotaLedger(rules.recent_spend_quota),
      })
    )
  }
  if (rules.exclude_violation) lines.push(t('qy_lot_rule_violation_any'))
  if (rules.max_violation_hits > 0) {
    lines.push(
      t('qy_lot_rule_violation_hits', { count: rules.max_violation_hits })
    )
  }
  if (rules.exclude_ever_auto_banned) lines.push(t('qy_lot_rule_ever_banned'))
  if (rules.exclude_currently_disabled) lines.push(t('qy_lot_rule_disabled'))
  if (rules.require_email) lines.push(t('qy_lot_rule_email'))
  if (rules.require_oauth) lines.push(t('qy_lot_rule_oauth'))
  if (rules.require_pay_password) lines.push(t('qy_lot_rule_pay_password'))
  if (rules.max_per_inviter > 0) {
    lines.push(t('qy_lot_rule_per_inviter', { count: rules.max_per_inviter }))
  }

  // 「管理员与活动创建者一律不能参加」是不可配置的硬规则，因此写死在这里而不是
  // 从 rules 里读 —— 它在后端也没有对应的字段可读，读不到就漏掉更糟。
  lines.push(t('qy_lot_rule_admin_excluded'))

  return (
    <ul className='space-y-1.5 text-sm'>
      {lines.map((line) => (
        <li key={line} className='flex items-start gap-2'>
          <Check
            aria-hidden='true'
            className='text-muted-foreground mt-0.5 size-3.5 shrink-0'
          />
          <span className='min-w-0 break-words'>{line}</span>
        </li>
      ))}
    </ul>
  )
}
