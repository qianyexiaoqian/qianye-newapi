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
import { z } from 'zod'

import {
  QY_GROUP_SELF_TOKEN,
  type QyGroupPolicy,
  type QyTransferGroupRule,
  type QyTransferGroupRuleInput,
} from '../types'

/**
 * 表单契约。
 *
 * **前端校验刻意只做最表层的一层**（非空、长度）：真正的规则合法性
 * （空白名单等同于禁止发起、名单不能含通配符、分组名不能带分隔符）全部由后端
 * `validateGroupRule` 判定并回传中文原因。两处各写一套判定，迟早会出现
 * 「前端放行、后端拒绝」或者更糟的「前端拦住了一条后端本来允许的规则」。
 */

/** 下拉顺序：从最宽松到最严格，符合运营收紧规则时的思考顺序。 */
export const QY_GROUP_POLICIES: QyGroupPolicy[] = [
  'allow_all',
  'allow_list',
  'deny_list',
  'deny_all',
]

/** 只有名单类策略才需要填 `to_groups`。 */
export function qyGroupPolicyNeedsList(policy: QyGroupPolicy): boolean {
  return policy === 'allow_list' || policy === 'deny_list'
}

export const qyGroupRuleSchema = z.object({
  from_group: z
    .string()
    .trim()
    .min(1, 'qy_trg_err_from_required')
    .max(64, 'qy_trg_err_group_too_long'),
  policy: z.enum(['allow_all', 'allow_list', 'deny_all', 'deny_list']),
  to_groups: z.string().max(1024, 'qy_trg_err_list_too_long'),
  enabled: z.boolean(),
  remark: z.string().max(255, 'qy_trg_err_remark_too_long'),
})

export type QyGroupRuleFormValues = z.infer<typeof qyGroupRuleSchema>

/**
 * 新建时的初值。
 *
 * 默认 `enabled: false`：一条刚建出来的规则立刻生效，等于让人在没看过矩阵的
 * 情况下就改变了资金流向。先存下来、在矩阵里确认「谁能转给谁」变成了什么，
 * 再回来打开它。
 */
export function qyEmptyGroupRule(): QyGroupRuleFormValues {
  return {
    from_group: '',
    policy: 'allow_list',
    to_groups: '',
    enabled: false,
    remark: '',
  }
}

export function qyGroupRuleToForm(
  rule: QyTransferGroupRule
): QyGroupRuleFormValues {
  return {
    from_group: rule.from_group,
    policy: rule.policy,
    to_groups: rule.to_groups,
    enabled: rule.enabled,
    remark: rule.remark,
  }
}

/**
 * 表单值 → 请求体。
 *
 * 非名单策略把 `to_groups` 清成空串，与后端 `validateGroupRule` 的归一化一致：
 * 留着一个不再生效的名单，下一个人打开这条规则时会以为它还算数。
 */
export function qyGroupRuleToPayload(
  values: QyGroupRuleFormValues
): QyTransferGroupRuleInput {
  return {
    from_group: values.from_group.trim(),
    policy: values.policy,
    to_groups: qyGroupPolicyNeedsList(values.policy)
      ? values.to_groups.trim()
      : '',
    enabled: values.enabled,
    remark: values.remark.trim(),
  }
}

/** 把 `a,b,@self` 拆成数组，供徽章展示。与后端 `parseGroupList` 同口径。 */
export function qySplitGroupList(raw: string): string[] {
  return raw
    .split(/[,;\r\n]/)
    .map((item) => item.trim())
    .filter((item) => item !== '')
}

/** `@self` 在列表里要显示成人话，而不是让运营去猜这个令牌是什么意思。 */
export function qyIsSelfToken(entry: string): boolean {
  return entry === QY_GROUP_SELF_TOKEN
}
