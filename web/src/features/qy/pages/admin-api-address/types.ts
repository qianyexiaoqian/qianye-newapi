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
 * API 地址簿管理端 DTO。对应 `qianye/modules/apiaddr/api_admin.go`。
 */

/** 管理端看到的一整行（含已停用的）。 */
export type QyApiAddress = {
  id: number
  name: string
  remark: string
  url: string
  /**
   * 逗号分隔的适用**用户分组**；空串 = 所有分组可见。
   *
   * 不绑定的地址就是「默认兜底线路」：没配专属线路的分组看到的恰好是这批。
   * 归一化（折叠大小写、去重、上限）全在后端 `normalizeUserGroups`，前端只做
   * 逗号拆/拼。
   */
  user_groups: string
  /**
   * 逗号分隔的**展示位置**（console = 控制台「API信息」卡片、picker =
   * 复制链接信息 / CC Switch）；空串 = 所有位置可见。过滤在服务端
   * （`?surface=`），白名单在后端 `normalizeSurfaces`。
   */
  surfaces: string
  /**
   * 控制台「API信息」卡片圆点颜色；空串 = 前端默认色。白名单在后端
   * `normalizeColor`（14 色调色板，与本页弹窗的选项同一份清单）。
   */
  color: string
  sort_order: number
  enabled: boolean
  created_at: number
  updated_at: number
}

export type QyApiAddressList = {
  items: QyApiAddress[]
  /**
   * 行数上限，由后端下发。
   *
   * 前端不自己抄一份：那是同一常量的第二份拷贝，后端调上限时前端的「新增」
   * 按钮不会跟着变，表现为按钮能点、点了报 400。
   */
  max: number
}

/** 新建 / 编辑的入参。`enabled` 缺省时后端按「新建=启用、编辑=保持原样」处理。 */
export type QyApiAddressUpsert = {
  name: string
  remark: string
  url: string
  /** 逗号分隔；空串 = 所有分组可见。与 remark 一样是整行提交，缺省即清空。 */
  user_groups: string
  /** 逗号分隔的展示位置；空串 = 所有位置可见。整行提交，缺省即清空。 */
  surfaces: string
  /** 空串 = 前端默认色。整行提交，缺省即清空。 */
  color: string
  enabled?: boolean
}
