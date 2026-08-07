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
import { queryOptions } from '@tanstack/react-query'
import type { TFunction } from 'i18next'

import { qyGet } from './api'
import { qyKeys } from './query-keys'

/**
 * 站点分组候选清单 —— 管理端每一处「填一个分组名」的输入共用的取值域。
 *
 * # 为什么是共享的
 *
 * 「让人手输分组名」这件事在扩展里已经出现过两次（划转分组限制、违规规则的分组
 * 作用域），两次的失败形状完全一样：打错一个字母，规则静默变成挂在一个不存在的
 * 分组上 —— 保存成功、界面正常、线上永不命中，而且**没有任何信号**。
 *
 * 划转那一页先解决了它（带元数据的下拉 + 未定义分组软告警）。这个模块把那份口径
 * 抬到共享层，让第二个消费方直接复用，而不是复制一份必然会漂移的实现。
 *
 * # 清单从哪来
 *
 * 走**已有的** `GET /admin/transfer/group-rules`，取它的 `group_options` 与
 * `channels_probe_ok`（后端 `qianye/modules/transfer/grouprule.go` 的
 * `listGroupCandidates`）。刻意不新开一个「分组清单」端点：
 *
 *   - 它已经是「分组倍率表 ∪ 用户可选分组白名单 ∪ default」这个正确的并集，
 *     并且带着运营在**挑**的那一刻真正需要的元数据（这个分组底下还有没有渠道）；
 *   - 它挂在 `guard.FlagCore` 上（见 module.go），只要扩展启用、扩展库可用就在，
 *     不随划转功能开关消失；
 *   - 同一份事实开两个端点，迟早会出现两页各自认为对方是错的那种状态。
 *
 * # 这份清单永远只是输入辅助
 *
 * 它**不是闸门**：拉不到、过期、或者名字不在里面，都不能阻止保存。历史分组
 * （倍率表里已删、users 里还有人挂着）恰恰是最需要被规则覆盖的那批账号。
 * 因此消费方必须做到两件事：拉取失败时仍然能手输；清单为空时不许把所有名字
 * 都标成「未定义」—— 那是假警报，而假警报比没有警报更糟。
 */

/** 下拉的一项。四个字段与后端 `groupCandidate` 逐字一致。 */
export type QyGroupOption = {
  name: string
  /**
   * 该名字在 `GroupRatio` 里的兜底倍率，**可以是 `null`**。
   *
   * `null` = 后端确实回答了「这个名字没有配过兜底倍率」：它要么只在「用户可选
   * 分组」白名单里、要么在倍率表里的写法与这里的归一化名字大小写不同。倍率侧
   * `GetGroupRatio` 是**精确 map 查找**，那两种情况下请求都会 fail-open 按 1.0
   * 计费 —— 所以这里绝不能填一个 1：它与 fail-open 的 1.0 数值巧合，看起来
   * 就像「运营配过原价」。
   */
  ratio: number | null
  /**
   * 该分组下是否还有启用的渠道（abilities）。
   *
   * `probe_ok` 为 false 时这一项恒为 false，此时它的含义是「不确定」而不是
   * 「确实没有」，任何提示都必须收起来。
   */
  has_channels: boolean
  /** 是否在「用户可选分组」白名单里。仅供参考。 */
  public_usable: boolean
}

export type QyGroupOptions = {
  options: QyGroupOption[]
  /** abilities 探测是否成功。false 时 `has_channels` 一律是「不确定」。 */
  probe_ok: boolean
}

/** 后端响应里本模块用得上的那两个字段，其余与分组清单无关，不声明。 */
type QyGroupCandidatesResponse = {
  group_options?: QyGroupOption[] | null
  channels_probe_ok?: boolean
}

/**
 * 分组候选清单。
 *
 * `retry: false`：这是一份纯辅助数据，拉不到时表单会退化成自由输入并给出提示，
 * 反复重试只会让「加载中」这一档在界面上赖着不走，运营反而以为是自己网络的问题。
 */
export function qyGroupOptionsQuery() {
  return queryOptions({
    queryKey: qyKeys.adminGroupOptions(),
    queryFn: async (): Promise<QyGroupOptions> => {
      const data = await qyGet<QyGroupCandidatesResponse>(
        '/admin/transfer/group-rules'
      )
      return {
        options: data.group_options ?? [],
        probe_ok: data.channels_probe_ok === true,
      }
    },
    staleTime: 5 * 60_000,
    retry: false,
  })
}

/**
 * 名单的默认分隔符。
 *
 * **刻意暴露成参数而不是写死**：两个消费方的后端解析口径并不相同 ——
 * 划转的 `parseGroupList` 认分号，违规的 `splitList` 不认（分号是分组名的一部分）。
 * 共享实现替两边选一个，就会在其中一边把一个合法的名字拦腰切开，
 * 而那正是这个模块要消灭的那种静默失效。
 */
export const QY_GROUP_LIST_SEPARATOR = /[,;\r\n]/

/**
 * 分组名归一。与后端 `qianye/groupname` 的 `Normalize` 逐字同口径
 * （去两侧空白 + 折叠大小写），空串原样返回空串。
 *
 * 必须一致的理由：后端判定时会把名字折叠成小写，前端若按原文比对「这个名字
 * 站点定义过没有」，运营输入 `VIP` 就会被误标成未定义分组。
 */
export function qyNormalizeGroupName(raw: string): string {
  return raw.trim().toLowerCase()
}

/** 把逗号分隔的名单拆成数组，供徽章展示与软告警计算。 */
export function qySplitGroupNames(
  raw: string,
  separator: RegExp = QY_GROUP_LIST_SEPARATOR
): string[] {
  return raw
    .split(separator)
    .map((item) => item.trim())
    .filter((item) => item !== '')
}

/**
 * 把一项追加到逗号分隔的名单里，已经在里面就原样返回。
 *
 * 归一后比对而不是原文比对：从下拉选 `vip`、名单里已有 `VIP`，两者在后端是
 * 同一个分组，再追加一次只会让运营看到一份自己删不干净的重复名单。
 */
export function qyAppendGroupName(
  raw: string,
  entry: string,
  separator: RegExp = QY_GROUP_LIST_SEPARATOR
): string {
  const existing = qySplitGroupNames(raw, separator)
  const target = qyNormalizeGroupName(entry)
  if (target === '') return raw
  if (existing.some((item) => qyNormalizeGroupName(item) === target)) return raw
  return [...existing, target].join(',')
}

/**
 * 名单里「站点没定义过」的那些名字。
 *
 * **只用于提示，绝不阻止提交。** 因此它刻意不出现在任何 zod schema 里 ——
 * 放进 schema 就会变成一道校验闸门，而那是后端明确不做的事。
 *
 * 调用方还必须自己判断「清单到底拉到了没有」：`options` 为空时（拉取失败、
 * 或者站点真的一个分组都没定义）这个函数会把每一个名字都算成未定义，
 * 那是一片假警报，必须在调用侧收起来而不是在这里猜。
 *
 * 入参只要求 `{ name }`：模型分组（{@link QyGroupOption}）与用户分组
 * （{@link QyUserGroupOption}）两份清单在这里的用法完全一致，
 * 而它们的元数据不同，绑死其中一种只会逼出第二份必然漂移的拷贝。
 */
export function qyUnknownGroupNames(
  names: string[],
  options: { name: string }[]
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
 *
 * 三个 i18n 键沿用划转分组规则页原有的 `qy_trg_option_*`：两页渲染的是同一件
 * 事，共用同一份文案才不会出现「同一个分组在两页上写法不一样」。
 */
export function qyGroupOptionLabel(
  option: QyGroupOption,
  probeOk: boolean,
  t: TFunction
): string {
  const parts = [
    option.name,
    option.ratio == null
      ? t('qy_trg_option_ratio_unset')
      : t('qy_trg_option_ratio', { ratio: option.ratio }),
  ]
  if (option.public_usable) parts.push(t('qy_trg_option_public'))
  if (probeOk && !option.has_channels) {
    parts.push(t('qy_trg_option_no_channels'))
  }
  return parts.join(' · ')
}

/**
 * 用户分组下拉的一项。四个字段与后端 `userGroupCandidate` 逐字一致。
 *
 * # 它与上面那个 {@link QyGroupOption} 不是同一件事
 *
 * `QyGroupOption` 是**模型分组**（`channels.group` / `abilities.group` /
 * `relayInfo.UsingGroup`）：这次请求去哪个渠道池子、按哪个倍率计费。它现在
 * 唯一的消费方是违规规则的「分组作用域」，那一处比的正是 `UsingGroup`。
 *
 * 这一个是**用户分组**（`users.group`）：这个人是谁。划转的分组限制与门槛分档
 * 比的都是它。
 *
 * 两者一度共用一份清单，后果全在看得见的那一层：运营从下拉里挑一个「分组」
 * 配划转限制，配出来的是一条永不命中的规则 —— 没有任何用户的 `users.group`
 * 等于一个模型分组的名字。判定一个字节都没受影响，这正是它能一直藏着的原因。
 * 详见 `qianye/modules/transfer/grouprule.go` 的 `definedUserGroups`。
 *
 * 元数据刻意只有登记表上那三样。倍率与渠道数是模型分组的属性，把它们摆在
 * 用户分组的下拉里，正是这一轮在消灭的那种混淆。
 */
export type QyUserGroupOption = {
  name: string
  display_name: string
  /**
   * 登记表上的启停位。**只影响可见性，不遮断任何判定** —— 一个被停用的用户
   * 分组底下仍然可能挂着用户，给它配规则/门槛是合法的，因此这里只做标注。
   */
  enabled: boolean
  note: string
}

/**
 * 用户分组下拉项的文案：名字 +（可选）显示名 +（可选）已停用标注。
 *
 * 显示名与名字都摆出来而不是二选一：运营在表单里填的、以及最终落库的都是
 * `name`，只显示 display_name 会让他保存之后在列表里认不出自己刚配的那一档。
 */
export function qyUserGroupOptionLabel(
  option: QyUserGroupOption,
  t: TFunction
): string {
  const parts = [option.name]
  if (option.display_name !== '' && option.display_name !== option.name) {
    parts.push(option.display_name)
  }
  if (!option.enabled) parts.push(t('qy_ugopt_disabled'))
  return parts.join(' · ')
}
