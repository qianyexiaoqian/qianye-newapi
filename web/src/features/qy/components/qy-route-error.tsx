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
import type { ErrorComponentProps } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'

import { isQyError, qyErrorMessage } from '../lib/api'

/**
 * qy 工作区的路由级错误边界。
 *
 * 挂在 `qy/route.tsx` 上是为了把异常拦在工作区内容区，不让它冒泡到
 * `__root.tsx` 的 `GeneralError` —— 那是全屏错误页，侧边栏都没了，
 * 用户会以为整站挂了。
 */
export function QyRouteError(props: ErrorComponentProps) {
  const { t } = useTranslation()
  // QyError 有语义化文案；其余异常（渲染错误、加载器抛错）只能退回原始 message，
  // 否则用户拿到一句"未知错误"，排查时连线索都没有。
  const description = isQyError(props.error)
    ? qyErrorMessage(props.error, t)
    : (props.error.message ?? t('qy_err_unknown'))

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('qy_nav_group')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <ErrorState
          title={t('qy_cfg_error_title')}
          description={description}
          onRetry={props.reset}
        />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
