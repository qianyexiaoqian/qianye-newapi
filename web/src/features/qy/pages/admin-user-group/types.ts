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
 * 新用户默认分组管理端 DTO。对应 `qianye/modules/usergroup/api_admin.go`。
 */

/** 下拉的一项。清单由后端从分组倍率表生成，前端不自己拼。 */
export type QyUserGroupOption = {
  name: string
  ratio: number
  /**
   * 该分组下是否有启用的渠道（abilities）。
   *
   * 为 false 时选它意味着新用户注册后一个模型都调不通 —— 这是运营唯一能提前
   * 看到这件事的地方，必须显式警告而不是默默让人选。
   */
  has_channels: boolean
  /** 是否在「用户可选分组」白名单里。仅供参考，不影响能否作为默认分组。 */
  public_usable: boolean
}

export type QyUserGroupConfig = {
  /** 运营配置的原始值，空串表示未配置。 */
  default_group: string
  /** 此刻真正会落到新用户身上的分组。 */
  effective_group: string
  /** 配置值当前是否仍然有效。false + default_group 非空 = 分组被删掉了。 */
  configured_valid: boolean
  /** 不配置（或配置失效）时上游数据库默认值兜底出来的分组。 */
  fallback_group: string
  groups: QyUserGroupOption[]
  /**
   * abilities 探测是否成功。false 时全部 `has_channels` 都是「不确定」而非
   * 「确实没有」，前端必须收起警告，否则运营会被一片红叹号吓住。
   */
  channels_probe_ok: boolean
}
