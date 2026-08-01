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
import type { TFunction } from 'i18next'
import { z } from 'zod'

import {
  QY_GROUP_SELF_TOKEN,
  QY_GROUP_WILDCARD,
  type QyGroupPolicy,
  type QyTransferGroupOption,
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

/**
 * 分组名归一。与后端 `qianye/groupname` 的 `Normalize` 逐字同口径
 * （去两侧空白 + 折叠大小写），空串原样返回空串。
 *
 * 必须一致的理由：后端保存时会把名字折叠成小写落库，前端若按原文比对
 * 「这个名字站点定义过没有」，运营输入 `VIP` 就会被误标成未定义分组，
 * 而保存之后它显示的是 `vip`。软告警最怕的就是这种假警报 ——
 * 报错一次没人信，之后真的打错字也不会有人看。
 */
export function qyNormalizeGroupName(raw: string): string {
  return raw.trim().toLowerCase()
}

/**
 * 一条规则引用到的全部分组名（已归一，去掉通配符与 `@self`）。
 *
 * 与后端 `ruleReferencedGroups` 同口径：通配符与 `@self` 都不是分组名，
 * 把它们当分组会让软告警报出两个永远不存在的「未定义分组」。
 */
export function qyRuleGroupNames(
  fromGroup: string,
  toGroups: string,
  policy: QyGroupPolicy
): string[] {
  const out: string[] = []
  const from = qyNormalizeGroupName(fromGroup)
  if (from !== '' && from !== QY_GROUP_WILDCARD) out.push(from)
  if (qyGroupPolicyNeedsList(policy)) {
    for (const entry of qySplitGroupList(toGroups)) {
      const name = qyNormalizeGroupName(entry)
      if (name === QY_GROUP_SELF_TOKEN || name === QY_GROUP_WILDCARD) continue
      if (name !== '') out.push(name)
    }
  }
  return [...new Set(out)]
}

/**
 * 表单里当场算出的「站点没定义过」的分组名。
 *
 * **只用于提示，绝不阻止提交**：历史分组（倍率表里已删、users 里还有人挂着）
 * 恰恰是最需要限制转出的一批账号。因此它刻意不出现在 zod schema 里 ——
 * 放进 schema 就会变成一道校验闸门，而那是后端明确不做的事。
 */
export function qyUnknownGroupNames(
  names: string[],
  options: QyTransferGroupOption[]
): string[] {
  const defined = new Set(
    options.map((option) => qyNormalizeGroupName(option.name))
  )
  return names.filter((name) => !defined.has(name))
}

/**
 * 下拉项的文案：名字 + 倍率 +（可选）两条警示。
 *
 * 元数据必须出现在选项本身上，而不是选完之后才提示：运营是在**挑**的那一刻
 * 需要知道「这个分组底下还有没有可用渠道」，选完再说就已经晚了。
 *
 * `probeOk` 为 false 时一律不提渠道 —— 那时 `has_channels` 全是「不确定」，
 * 照样标警告会让整张下拉挂满假警报。
 */
export function qyGroupOptionLabel(
  option: QyTransferGroupOption,
  probeOk: boolean,
  t: TFunction
): string {
  const parts = [option.name, t('qy_trg_option_ratio', { ratio: option.ratio })]
  if (option.public_usable) parts.push(t('qy_trg_option_public'))
  if (probeOk && !option.has_channels) {
    parts.push(t('qy_trg_option_no_channels'))
  }
  return parts.join(' · ')
}

/**
 * 把一项追加到逗号分隔的名单里，已经在里面就原样返回。
 *
 * 归一后比对而不是原文比对：从下拉选 `vip`、名单里已有 `VIP`，两者落库后是
 * 同一项（后端 `validateGroupRule` 会折叠大小写并去重），此时再追加一次只会
 * 让运营看到一份自己删不干净的重复名单。
 */
export function qyAppendGroup(raw: string, entry: string): string {
  const existing = qySplitGroupList(raw)
  const target = qyNormalizeGroupName(entry)
  if (target === '') return raw
  if (existing.some((item) => qyNormalizeGroupName(item) === target)) return raw
  return [...existing, target].join(',')
}
