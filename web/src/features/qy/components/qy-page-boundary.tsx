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
import { CloudOff, type LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import { useQyConfig } from '../hooks/use-qy-config'
import { isQyError, qyErrorMessage } from '../lib/api'

/** 只取用得到的字段，这样 `useQuery` 的任意泛型实例都能直接传进来。 */
type QyBoundaryQuery = {
  isLoading: boolean
  isError: boolean
  error: unknown
  refetch?: () => unknown
}

type QyPageBoundaryProps = {
  query: QyBoundaryQuery
  /** 数据为空。由调用方判断（`data.items.length === 0`），组件不猜。 */
  isEmpty?: boolean
  emptyIcon?: LucideIcon
  emptyTitle?: string
  emptyDescription?: string
  emptyAction?: ReactNode
  children: ReactNode
}

/**
 * qy 页面的统一状态边界：加载 / 扩展未启用 / 扩展库降级 / 错误 / 空态。
 *
 * 每个 qy 页面的内容区都应该包一层，理由是这五种状态的判定规则涉及后端的
 * 降级契约（404=隐藏、503=重试），逐页手写必然出现"扩展关了却弹红色报错"
 * 这类破坏"零痕迹"承诺的实现。
 */
export function QyPageBoundary(props: QyPageBoundaryProps) {
  const { t } = useTranslation()
  const config = useQyConfig()

  // 扩展被关掉。路由守卫本应已经把用户挡在外面，这里是深链接直达时的兜底：
  // 显示中性空态而不是红色错误 —— 功能不存在不是"出错了"。
  if (config.status === 'disabled') {
    return (
      <EmptyState
        icon={CloudOff}
        title={t('qy_err_disabled')}
        description={t('qy_cfg_disabled_desc')}
      />
    )
  }

  if (props.query.isLoading) {
    return <LoadingState />
  }

  if (props.query.isError) {
    const error = props.query.error
    // 请求本身撞上"功能已关闭"：同样按中性空态处理，不弹红。
    if (isQyError(error) && error.isHidden) {
      return (
        <EmptyState
          icon={CloudOff}
          title={t('qy_err_disabled')}
          description={t('qy_cfg_disabled_desc')}
        />
      )
    }
    return (
      <ErrorState
        title={t('qy_cfg_error_title')}
        description={qyErrorMessage(error, t)}
        onRetry={
          props.query.refetch == null
            ? undefined
            : () => {
                props.query.refetch?.()
              }
        }
      />
    )
  }

  const body =
    props.isEmpty === true ? (
      <EmptyState
        icon={props.emptyIcon}
        title={props.emptyTitle}
        description={props.emptyDescription}
        action={props.emptyAction}
      />
    ) : (
      props.children
    )

  // 扩展开着但新库不可用：数据可能是陈旧的，且写操作一定会吃 503。
  // 顶部挂降级横幅，页面本身照常渲染 —— 直接整页报错会让只想看历史记录的
  // 用户什么也做不了。
  //
  // 必须同时判 status==='enabled'：配置还在取数时 available 也是 false，
  // 只判 available 会在每次进页面时先闪一下"服务降级"的红色横幅。
  if (config.status === 'enabled' && !config.available) {
    return (
      <div className='flex min-h-0 flex-1 flex-col gap-3'>
        <Alert variant='destructive'>
          <CloudOff />
          <AlertTitle>{t('qy_cfg_db_down_title')}</AlertTitle>
          <AlertDescription>{t('qy_cfg_db_down_desc')}</AlertDescription>
        </Alert>
        {body}
      </div>
    )
  }

  return body
}
