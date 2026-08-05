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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Eye, PackageCheck, Undo2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyMaskedUser } from '../../../components/qy-masked-user'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import { QyPager } from '../../components/qy-pager'
import { QY_EMPTY_TEXT, formatQyTs } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { qyAdminLotTextPrizesQuery } from '../api'
import type { QyLotAdminTextPrize } from '../types'
import { QyLotFulfillDialog } from './lottery-fulfill-dialog'
import { QyLotRevealPrizeDialog } from './lottery-reveal-prize-dialog'
import { QyLotUnfulfillDialog } from './lottery-unfulfill-dialog'

const PAGE_SIZE = 20
const ALL = 'all'

/**
 * 文本奖履行队列。
 *
 * ## 这一页盯的是人，不是机器
 *
 * 额度奖有出款 worker、指数退避、转人工出口，卡住了会自己报警。文本奖没有 ——
 * 它的"发放"就是有人去填一段兑换码。没有这张队列，文本奖会**静默烂掉**，
 * 而用户会以为是抽奖作弊了。所以未履行是一个显式的待办项，而不是一条藏在
 * 出款列表里、状态看起来还挺正常的行。
 *
 * ## 列表里永远只有掩码
 *
 * 看明文是一个**独立的、要填事由的、写审计的**动作（`reveal`）。理由与提现的
 * 收款信息一模一样：一键直出会让"滑过列表时的随手点击"和"真正的核对"在事后的
 * 审计流水里混成一片，那时审计就不再能区分任何事情。
 *
 * ## `finished` 不等于履行完
 *
 * 活动收尾只看资金终态 —— `granted` 的文本奖不阻塞收尾（人工履行没有 SLA，
 * 拿它卡收尾会让活动永远到不了 finished，还白占一个并发名额）。所以这张队列
 * 在活动已经 finished 之后仍然可能有待办项，这是设计意图，不是漏收尾。
 */
export function QyLotFulfillQueueTab(props: { actNo: string }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [scope, setScope] = useState(ALL)
  const [fulfillTarget, setFulfillTarget] =
    useState<QyLotAdminTextPrize | null>(null)
  const [unfulfillTarget, setUnfulfillTarget] =
    useState<QyLotAdminTextPrize | null>(null)
  const [revealTarget, setRevealTarget] = useState<QyLotAdminTextPrize | null>(
    null
  )

  const params = {
    p: page,
    page_size: PAGE_SIZE,
    fulfilled: scope === ALL ? undefined : Number(scope),
  }
  const query = useQuery(qyAdminLotTextPrizesQuery(props.actNo, params))
  const items = qyArray(query.data?.items)

  // 履行 / 撤销都会同时改变这张队列、活动详情上的待办计数与事件流，
  // 所以整片失效，而不是挑几个 key —— 挑漏一个就是界面上"改了但没变"。
  const refresh = () => queryClient.invalidateQueries({ queryKey: qyKeys.all })

  return (
    <div className='space-y-3'>
      <QyFilterBar>
        <QyFilterField label={t('qy_common_status')}>
          <Select
            value={scope}
            onValueChange={(value) => {
              setScope(value ?? ALL)
              setPage(1)
            }}
          >
            <SelectTrigger className='w-40'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
              <SelectItem value='0'>{t('qy_lot_a_fulfill_pending')}</SelectItem>
              <SelectItem value='1'>{t('qy_lot_fulfilled')}</SelectItem>
            </SelectContent>
          </Select>
        </QyFilterField>
      </QyFilterBar>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyTitle={t('qy_lot_a_fulfill_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={items}
            getRowKey={(row: QyLotAdminTextPrize) => row.payout_no}
            columns={[
              {
                id: 'user',
                header: t('qy_common_user'),
                cell: (row: QyLotAdminTextPrize) => (
                  <QyMaskedUser
                    userId={row.user_id}
                    maskedName={row.username}
                    copyable
                  />
                ),
              },
              {
                id: 'tier',
                header: t('qy_lot_tier'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotAdminTextPrize) =>
                  t('qy_lot_tier_no', { no: row.tier }),
              },
              {
                id: 'code',
                header: t('qy_lot_code_masked'),
                // 列表里恒为掩码（后端只下发 `secret_mask`，明文压根不经过
                // 这个接口）。真正的码要点「查看明文」，那是一个写审计的
                // 独立动作 —— 见本文件顶部的说明。
                cellClassName: 'font-mono text-xs',
                cell: (row: QyLotAdminTextPrize) =>
                  row.secret_mask === '' ? QY_EMPTY_TEXT : row.secret_mask,
              },
              {
                id: 'note',
                header: t('qy_lot_fulfill_note'),
                cell: (row: QyLotAdminTextPrize) =>
                  row.fulfill_note === '' ? QY_EMPTY_TEXT : row.fulfill_note,
              },
              {
                id: 'fulfilled',
                header: t('qy_common_status'),
                cell: (row: QyLotAdminTextPrize) =>
                  !row.fulfilled ? (
                    <Badge variant='destructive'>
                      {t('qy_lot_a_fulfill_pending')}
                    </Badge>
                  ) : (
                    <span className='inline-flex flex-wrap items-center gap-1.5'>
                      <Badge variant='secondary'>{t('qy_lot_fulfilled')}</Badge>
                      <span className='text-muted-foreground text-xs'>
                        {formatQyTs(row.fulfilled_at)}
                      </span>
                    </span>
                  ),
              },
              {
                id: 'actions',
                header: t('qy_common_actions'),
                cell: (row: QyLotAdminTextPrize) => (
                  <span className='flex flex-wrap gap-1'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_lot_fulfill_btn')}
                      title={t('qy_lot_fulfill_btn')}
                      disabled={row.fulfilled}
                      onClick={() => setFulfillTarget(row)}
                    >
                      <PackageCheck aria-hidden='true' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_lot_text_reveal_btn')}
                      title={t('qy_lot_text_reveal_btn')}
                      disabled={!row.fulfilled}
                      onClick={() => setRevealTarget(row)}
                    >
                      <Eye aria-hidden='true' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_lot_unfulfill')}
                      title={t('qy_lot_unfulfill')}
                      disabled={!row.fulfilled}
                      onClick={() => setUnfulfillTarget(row)}
                    >
                      <Undo2 aria-hidden='true' />
                    </Button>
                  </span>
                ),
              },
            ]}
          />
          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
            disabled={query.isFetching}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_finished_means_funds_only')}
          </p>
        </div>
      </QyPageBoundary>

      <QyLotFulfillDialog
        target={fulfillTarget}
        onClose={() => setFulfillTarget(null)}
        onDone={() => {
          setFulfillTarget(null)
          toast.success(t('qy_lot_fulfill_done'))
          void refresh()
        }}
      />
      <QyLotUnfulfillDialog
        target={unfulfillTarget}
        onClose={() => setUnfulfillTarget(null)}
        onDone={() => {
          setUnfulfillTarget(null)
          toast.success(t('qy_lot_unfulfill_done'))
          void refresh()
        }}
      />
      <QyLotRevealPrizeDialog
        target={revealTarget}
        onClose={() => setRevealTarget(null)}
      />
    </div>
  )
}
