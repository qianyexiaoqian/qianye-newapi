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

import { qyGet, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QySiteThemeConfig, QySiteThemePayload } from './types'

/** 后端路由，注册于 `qianye/modules/sitetheme/sitetheme.go` 的 `RegisterAdminRoutes`。 */
export const QY_SITE_THEME_PATH = '/admin/site-theme'

export function qySiteThemeQuery() {
  return queryOptions({
    queryKey: qyKeys.adminSiteTheme(),
    queryFn: () => qyGet<QySiteThemeConfig>(QY_SITE_THEME_PATH),
  })
}

/**
 * 保存站点默认主题。
 *
 * 两个字段一律整体提交：后端 `putSiteThemeRequest` 是全量覆盖语义，
 * 漏传 `force_preset` 会被 Go 的零值当成「关闭强制」而不是「保持不变」。
 */
export function qySaveSiteTheme(payload: QySiteThemePayload) {
  return qyPut<QySiteThemePayload>(QY_SITE_THEME_PATH, {
    default_preset: payload.default_preset,
    force_preset: payload.force_preset,
  })
}
