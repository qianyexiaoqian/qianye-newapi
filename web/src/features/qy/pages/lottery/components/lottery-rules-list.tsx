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
import {
  CalendarClock,
  KeyRound,
  Lock,
  Mail,
  RefreshCw,
  ShieldBan,
  ShieldCheck,
  UserPlus,
  Users,
  Wallet,
  type LucideIcon,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { formatQyQuotaLedger } from '../../../lib/format'
import { parseQyLotRules } from '../lib/rules'
import type { QyLotDrawMode } from '../types'
import { QyLotFinePrint } from './lottery-fine-print'

/** 一条参与条件：图标 + 一句话；长的那几条把原文折在 `detail` 里。 */
type RuleLine = {
  icon: LucideIcon
  text: string
  /** 原文（协议承诺，一字不改），默认收起。 */
  detail?: string
}

/**
 * 把规范化 JSON 的参与条件变成人话：每条一个图标 + 一句话。
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
 *
 * ## 长句折起来，原文不删
 *
 * 「管理员能不能参加」那两条各有一整句"为什么"，是协议承诺、不能删；但它们
 * 是解释不是条件，铺在条件列表里会把一屏撑长。所以触发器上留一个词的结论
 * （图标 + 短句），原文折在下面一次点开就能读到，逐字不变。
 */
export function QyLotRulesList(props: {
  rulesText: string
  kind: string
  /**
   * 定档方式。转盘（`wheel`）那一句公示行与批次抽奖**不能共用**：批次抽奖的
   * "任何人都无法预知"对转盘是假的（decisions.md D-13），转盘只声称"可复算"。
   */
  drawMode?: QyLotDrawMode
}) {
  const { t } = useTranslation()

  const rules = parseQyLotRules(props.rulesText)
  if (rules == null) {
    return (
      <pre className='bg-muted/40 overflow-x-auto rounded-lg p-3 text-xs break-all whitespace-pre-wrap'>
        {props.rulesText}
      </pre>
    )
  }

  const lines: RuleLine[] = []
  if (rules.allow_groups.length > 0) {
    lines.push({
      icon: Users,
      text: t('qy_lot_rule_group_allow', {
        groups: rules.allow_groups.join('、'),
      }),
    })
  }
  if (rules.deny_groups.length > 0) {
    lines.push({
      icon: Users,
      text: t('qy_lot_rule_group_deny', {
        groups: rules.deny_groups.join('、'),
      }),
    })
  }
  if (rules.min_account_age_days > 0) {
    lines.push({
      icon: CalendarClock,
      text: t('qy_lot_rule_account_age', { days: rules.min_account_age_days }),
    })
  }
  if (rules.min_quota > 0) {
    lines.push({
      icon: Wallet,
      text: t('qy_lot_rule_min_quota', {
        amount: formatQyQuotaLedger(rules.min_quota),
      }),
    })
  }
  if (rules.min_used_quota > 0) {
    lines.push({
      icon: Wallet,
      text: t('qy_lot_rule_used_quota', {
        amount: formatQyQuotaLedger(rules.min_used_quota),
      }),
    })
  }
  if (rules.recent_spend_quota > 0 && rules.recent_spend_days > 0) {
    lines.push({
      icon: Wallet,
      text: t('qy_lot_rule_recent_spend', {
        days: rules.recent_spend_days,
        amount: formatQyQuotaLedger(rules.recent_spend_quota),
      }),
    })
  }
  if (rules.exclude_violation) {
    lines.push({ icon: ShieldBan, text: t('qy_lot_rule_violation_any') })
  }
  if (rules.max_violation_hits > 0) {
    lines.push({
      icon: ShieldBan,
      text: t('qy_lot_rule_violation_hits', {
        count: rules.max_violation_hits,
      }),
    })
  }
  if (rules.exclude_ever_auto_banned) {
    lines.push({ icon: ShieldBan, text: t('qy_lot_rule_ever_banned') })
  }
  if (rules.exclude_currently_disabled) {
    lines.push({ icon: ShieldBan, text: t('qy_lot_rule_disabled') })
  }
  if (rules.require_email) {
    lines.push({ icon: Mail, text: t('qy_lot_rule_email') })
  }
  if (rules.require_oauth) {
    lines.push({ icon: KeyRound, text: t('qy_lot_rule_oauth') })
  }
  if (rules.require_pay_password) {
    lines.push({ icon: Lock, text: t('qy_lot_rule_pay_password') })
  }
  if (rules.max_per_inviter > 0) {
    lines.push({
      icon: UserPlus,
      text: t('qy_lot_rule_per_inviter', { count: rules.max_per_inviter }),
    })
  }

  // 「管理员与活动创建者能不能参加」是不可配置的硬规则，因此写死在这里而不是
  // 从 rules 里读 —— 它在后端也没有对应的字段可读，读不到就漏掉更糟。
  // 分野与后端 Evaluate 完全同一条（D-10）：竞猜的答案由管理员录入，定答案的
  // 人不能下场对赌；抽奖（rank/prob/ball）的开奖结果绑定封盘冻结的名单，
  // 谁都无法预知，管理员与创建者照常可以参与 —— 这件事必须原样公示，
  // 否则第一个在名单里认出管理员的用户会把它当成作弊。
  //
  // 转盘（D-13）单独一句：管理员同样可以参与，但理由**不是**"无法预知" ——
  // 即时开奖没有名单可冻结，能读到种子的人可以挑自己的下一转。协议对转盘只
  // 保证"结果由服务端承诺的种子与序号算出、公示后可逐转复算"，公示行也只能
  // 这么说；把批次那句抄过来是一句对转盘为假的话。
  if (props.kind === 'draw' && props.drawMode === 'wheel') {
    lines.push(
      { icon: ShieldCheck, text: t('qy_lot_wheel_rule_admin_allowed') },
      {
        icon: RefreshCw,
        text: t('qy_lot_wheel_rule_recompute_short'),
        detail: t('qy_lot_wheel_rule_recompute'),
      }
    )
  } else if (props.kind === 'draw') {
    lines.push({
      icon: ShieldCheck,
      text: t('qy_lot_rule_admin_allowed_short'),
      detail: t('qy_lot_rule_admin_allowed'),
    })
  } else {
    lines.push({ icon: ShieldBan, text: t('qy_lot_rule_admin_excluded') })
  }

  return (
    <ul className='space-y-1.5 text-sm'>
      {lines.map((line) => {
        const Icon = line.icon
        return (
          <li key={line.text} className='flex items-start gap-2'>
            <Icon
              aria-hidden='true'
              className='text-muted-foreground mt-0.5 size-3.5 shrink-0'
            />
            <span className='min-w-0 break-words'>
              {line.text}
              {line.detail != null && (
                <QyLotFinePrint>
                  <p>{line.detail}</p>
                </QyLotFinePrint>
              )}
            </span>
          </li>
        )
      })}
    </ul>
  )
}
