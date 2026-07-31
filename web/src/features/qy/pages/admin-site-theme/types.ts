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
 * 站点主题管理端 DTO。逐字段对应 `qianye/modules/sitetheme/api.go`
 * 的 `handleGetSiteTheme` 响应与 `putSiteThemeRequest`。
 */

/** GET /api/qy/admin/site-theme 的 data。 */
export type QySiteThemeConfig = {
  /** 当前生效的站点默认预设。未配置时后端回落成 `upstream_default`。 */
  default_preset: string
  /** 真为强制:忽略访客的个人偏好，全站统一用 `default_preset`。 */
  force_preset: boolean
  /**
   * 后端认可的全部预设，已排序。
   *
   * 下拉必须用它而不是前端的 `THEME_PRESETS`：校验口径在后端
   * (`sitetheme/store.go` 的 `allowedPresets`)，前端自己列一遍必然漂移，
   * 表现为「下拉里能选、保存时报未知预设」。
   */
  allowed_presets: string[]
  /** 上游硬编码的默认值。用于把「未配置」和「配成了同一个值」讲清楚。 */
  upstream_default: string
}

/** PUT /api/qy/admin/site-theme 的请求体。两个字段都必传。 */
export type QySiteThemePayload = {
  default_preset: string
  force_preset: boolean
}
