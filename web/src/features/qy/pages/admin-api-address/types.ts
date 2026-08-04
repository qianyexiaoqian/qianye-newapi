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
  enabled?: boolean
}
