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
import { Construction } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { QyPageBoundary } from '../../components/qy-page-boundary'

/** 一个"什么都没在取"的查询：让 `QyPageBoundary` 直接走到空态那一支。 */
const IDLE_QUERY = { isLoading: false, isError: false, error: null }

/**
 * 「建设中」占位 —— 星屑 / 商城 / 转盘那批页面的标签体在正文写出来之前先渲染它。
 *
 * 走 `QyPageBoundary` 而不是裸写一个 `EmptyState`：扩展关掉、扩展库降级这两档
 * 的表现由它统一给（深链接直达一个关掉的功能时看到的是中性空态，不是红字），
 * 后续开发者把正文换进来时这层外壳照旧。
 *
 * 替换方式：把宿主里 `<QyUnderConstruction />` 那一行换成真正的正文组件即可，
 * 本组件不接任何 props，删掉它不会留下悬空的接线。
 */
export function QyUnderConstruction() {
  const { t } = useTranslation()
  return (
    <QyPageBoundary
      query={IDLE_QUERY}
      isEmpty
      emptyIcon={Construction}
      emptyTitle={t('qy_common_under_construction_title')}
      emptyDescription={t('qy_common_under_construction_desc')}
    >
      {null}
    </QyPageBoundary>
  )
}
