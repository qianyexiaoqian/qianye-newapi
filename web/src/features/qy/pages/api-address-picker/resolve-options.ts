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
import { readSiteServerAddress } from '@/lib/channel-connection-info'

import type { QyApiAddressOption } from './api'

/**
 * 把后端清单变成「真正会摆在用户面前的选项」。
 *
 * 一条都没配时换成一条指向站点自身的合成项，而不是返回空数组：
 *
 *   · 那正是本功能上线之前的行为 —— 运营什么都不配 = 与旧版完全一致；
 *   · 弹一个空列表会把一个本来能用的功能变成一堵墙。
 *
 * 「要不要弹窗」的判定与「窗口里列什么」共用这一个函数。各判各的必然出现
 * "判定说只有 1 条所以直接复制、窗口里其实还有别的"这种对不上 —— 那正是本仓
 * 反复出现的「同一概念的第 N 份拷贝」。
 *
 * 合成项的 id 取 0：后端主键从 1 起，不会撞。
 */
export function qyResolveAddressOptions(
  list: QyApiAddressOption[] | undefined,
  siteLabel: string
): QyApiAddressOption[] {
  if (list != null && list.length > 0) return list
  return [
    {
      id: 0,
      name: siteLabel,
      remark: '',
      url: readSiteServerAddress(),
      color: '',
    },
  ]
}

/** 上游「API 地址」弹出列表(`ApiKeysPrimaryButtons`)的一项。 */
export type QyApiInfoItem = {
  url: string
  route: string
  description: string
}

/**
 * 把二开地址簿换成上游「API 地址」弹出列表的形状。
 *
 * 密钥页右上角那个弹出列表原先只读上游「控制台设置 → API 信息」,与页面左侧
 * 地址卡(二开地址簿)是两份数据,运营改了地址簿,右上角仍挂着旧地址。现在地址簿
 * 优先;返回空数组表示地址簿一条都没有(或扩展未启用),调用方照旧回落上游配置。
 */
export function qyAddressBookAsApiInfo(
  list: QyApiAddressOption[] | undefined
): QyApiInfoItem[] {
  return (list ?? [])
    .filter((option) => option.url.trim() !== '')
    .map((option) => ({
      url: option.url,
      route: option.name,
      description: option.remark,
    }))
}
