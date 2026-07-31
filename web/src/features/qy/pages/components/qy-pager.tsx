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
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

type QyPagerProps = {
  /** 当前页，1 起。 */
  page: number
  pageSize: number
  /** 后端返回的总条数。 */
  total: number
  onPageChange: (page: number) => void
  /** 取数期间禁用翻页，避免连点把页码推到数据之外。 */
  disabled?: boolean
}

/**
 * 服务端分页的翻页条。
 *
 * 刻意不用 `DataTablePagination`：那个组件要求传入一个 TanStack `Table` 实例，
 * 而 qy 的流水页全部是服务端分页 + 只读展示，为了一个翻页条把整套客户端表格
 * 状态机拉进来并不划算。
 *
 * 总数为 0 时整条隐藏 —— 空态由 `QyPageBoundary` 负责，这里再画一个
 * "第 1 页 / 共 0 条"只是噪声。
 */
export function QyPager(props: QyPagerProps) {
  const { t } = useTranslation()
  if (props.total <= 0) return null

  const lastPage = Math.max(1, Math.ceil(props.total / props.pageSize))
  const page = Math.min(Math.max(1, props.page), lastPage)

  return (
    <div className='flex flex-wrap items-center justify-between gap-2 pt-3'>
      <span className='text-muted-foreground text-xs'>
        {t('qy_common_page_summary', {
          page,
          pages: lastPage,
          total: props.total,
        })}
      </span>
      <div className='flex items-center gap-2'>
        <Button
          variant='outline'
          size='sm'
          disabled={props.disabled === true || page <= 1}
          onClick={() => props.onPageChange(page - 1)}
        >
          <ChevronLeft className='size-4' aria-hidden='true' />
          {t('qy_common_prev_page')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          disabled={props.disabled === true || page >= lastPage}
          onClick={() => props.onPageChange(page + 1)}
        >
          {t('qy_common_next_page')}
          <ChevronRight className='size-4' aria-hidden='true' />
        </Button>
      </div>
    </div>
  )
}
