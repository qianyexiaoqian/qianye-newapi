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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Plus, Ticket, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

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

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../components/qy-responsive-dialog'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { qyArray } from '../../lib/array'
import { formatQyQuotaLedger } from '../../lib/format'
import { QyPager } from '../components/qy-pager'
import { QyStatGrid } from '../components/qy-stat-grid'
import {
  qyLotActivityBadgeStatus,
  qyLotOutcomeKey,
} from '../lottery/lib/display'
import { formatQyTs } from '../ops/format'
import { QyFilterBar, QyFilterField } from '../ops/qy-ops-ui'
import {
  qyAdminLotActivitiesQuery,
  qyAdminLotConfigQuery,
  qyAdminLotTextPrizesQuery,
} from './api'
import { QyLotCreateWizard } from './components/lottery-create-wizard'
import { QyLotFulfillQueueTab } from './components/lottery-fulfill-queue-tab'
import { QyLotSeriesPanel } from './components/lottery-series-panel'
import type { QyLotAdminActivityBrief } from './types'

const PAGE_SIZE = 20
const ALL = 'all'

/**
 * 抽奖 / 竞猜活动管理。
 *
 * 列表页只负责两件事：**这场赚了还是亏了**，以及**有没有需要人处理的异常**。
 * 具体的名单、派奖进度、事件流全在详情页 —— 把它们摊在列表上，运营每天真正
 * 要扫一眼的那两列反而看不见了。
 */
export function QyAdminLottery() {
  const { t } = useTranslation()
  const [kind, setKind] = useState(ALL)
  const [status, setStatus] = useState(ALL)
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  const [queueOpen, setQueueOpen] = useState(false)
  const [seriesOpen, setSeriesOpen] = useState(false)

  const params = {
    p: page,
    page_size: PAGE_SIZE,
    kind: kind === ALL ? undefined : kind,
    status: status === ALL ? undefined : status,
  }
  const query = useQuery(qyAdminLotActivitiesQuery(params))
  const configQuery = useQuery(qyAdminLotConfigQuery())
  /*
    跨活动的「还欠着多少份码」。
    只取 total，所以 page_size 拉到最小 —— 这一格要的是那个数，不是那一页。

    它必须在**列表页**上，不能只藏在活动详情里：文本奖的 granted 不阻塞收尾，
    一场配了文本奖的活动几秒内就走到 finished，从"进行中"的注意力范围里消失。
    此后运营再也不会主动打开它，而中奖者正在「我的参与」里等着 —— 第一次告警
    要等对账任务 14 天的阈值，那时用户早就认定是抽奖作弊了。
  */
  const pendingTextQuery = useQuery(
    qyAdminLotTextPrizesQuery('', { fulfilled: 0, p: 1, page_size: 1 })
  )
  const pendingText = pendingTextQuery.data?.total ?? 0
  const items = qyArray(query.data?.items)

  // 本页汇总。跨页的总账在对账任务与资金单裁决台上，这里只是"当前这一屏"，
  // 所以标签必须写清是本页 —— 否则运营会把它当成全站收支。
  const totals = items.reduce(
    (acc, row) => ({
      pool: acc.pool + row.pool_quota,
      payout: acc.payout + row.payout_quota,
      fee: acc.fee + row.platform_fee_quota,
      flags: acc.flags + row.open_flag_count,
    }),
    { pool: 0, payout: 0, fee: 0, flags: 0 }
  )

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_lottery')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/lottery-config' />}
        >
          {t('qy_nav_a_lottery_config')}
        </Button>
        {/* 双色球的期次系列必须先建，一期双色球才开得出来：号池、投注入池比例
            与累计发行上限全部由系列决定，创建活动时没有任何字段能覆盖它们。 */}
        <Button size='sm' variant='outline' onClick={() => setSeriesOpen(true)}>
          {t('qy_lot_ball_series_manage')}
        </Button>
        <Button size='sm' onClick={() => setCreateOpen(true)}>
          <Plus aria-hidden='true' />
          {t('qy_lot_create_title')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <QyStatGrid
            items={[
              {
                key: 'pool',
                label: t('qy_lot_a_stat_pool'),
                value: formatQyQuotaLedger(totals.pool),
                hint: t('qy_lot_a_stat_page_scope'),
              },
              {
                key: 'payout',
                label: t('qy_lot_a_stat_payout'),
                value: formatQyQuotaLedger(totals.payout),
              },
              {
                key: 'fee',
                label: t('qy_lot_a_stat_fee'),
                value: formatQyQuotaLedger(totals.fee),
                hint: t('qy_lot_a_stat_fee_hint'),
              },
              {
                key: 'flags',
                label: t('qy_lot_a_stat_flags'),
                value: totals.flags,
                emphasis: totals.flags > 0,
              },
              {
                key: 'text_prizes',
                label: t('qy_lot_a_fulfill_pending'),
                value: pendingText,
                hint: t('qy_lot_finished_means_funds_only'),
                emphasis: pendingText > 0,
              },
            ]}
          />

          {pendingText > 0 && (
            <div className='flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3'>
              <span className='text-sm'>
                {t('qy_lot_a_fulfill_pending_badge', { count: pendingText })}
              </span>
              <Button
                size='sm'
                variant='outline'
                onClick={() => setQueueOpen(true)}
              >
                {t('qy_lot_a_fulfill_open_queue')}
              </Button>
            </div>
          )}

          <QyFilterBar>
            <QyFilterField label={t('qy_lot_filter_kind')}>
              <Select
                value={kind}
                onValueChange={(value) => {
                  setKind(value ?? ALL)
                  setPage(1)
                }}
              >
                <SelectTrigger className='w-32'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                  <SelectItem value='draw'>{t('qy_lot_kind_draw')}</SelectItem>
                  <SelectItem value='guess'>
                    {t('qy_lot_kind_guess')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </QyFilterField>
            <QyFilterField label={t('qy_common_status')}>
              <Select
                value={status}
                onValueChange={(value) => {
                  setStatus(value ?? ALL)
                  setPage(1)
                }}
              >
                <SelectTrigger className='w-36'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                  <SelectItem value='draft'>{t('qy_lot_st_draft')}</SelectItem>
                  <SelectItem value='published'>
                    {t('qy_lot_st_published')}
                  </SelectItem>
                  <SelectItem value='locked'>
                    {t('qy_lot_st_locked')}
                  </SelectItem>
                  <SelectItem value='settling'>
                    {t('qy_lot_st_settling')}
                  </SelectItem>
                  <SelectItem value='finished'>
                    {t('qy_lot_st_finished')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </QyFilterField>
          </QyFilterBar>

          <QyPageBoundary
            query={query}
            isEmpty={query.data != null && items.length === 0}
            emptyIcon={Ticket}
            emptyTitle={t('qy_lot_a_empty')}
            /* 站里一条活动都没有时，这一屏是运营看到的**唯一**一句话。
               「三个玩法各是什么、从哪儿开」必须写在这里 —— 双色球此前埋在
               二级下拉里找不到，正是从这一屏开始找不到的。 */
            emptyDescription={t('qy_lot_a_empty_desc')}
            emptyAction={
              <Button size='sm' onClick={() => setCreateOpen(true)}>
                <Plus aria-hidden='true' />
                {t('qy_lot_create_title')}
              </Button>
            }
          >
            <div className='space-y-3'>
              <StaticDataTable
                data={items}
                getRowKey={(row: QyLotAdminActivityBrief) => row.act_no}
                columns={[
                  {
                    id: 'title',
                    header: t('qy_lot_activity'),
                    cell: (row: QyLotAdminActivityBrief) => (
                      <Link
                        to='/qy/admin/lottery/$actNo'
                        params={{ actNo: row.act_no }}
                        className='hover:underline'
                      >
                        {row.title}
                      </Link>
                    ),
                  },
                  {
                    // 双色球的 kind 也是 draw，只靠这一列运营在列表上分不出来，
                    // 而那一行的 pool_quota（本期投注额）与它真正的奖池不是一
                    // 回事 —— 分不出来就会照着错的数判断收支。
                    id: 'kind',
                    header: t('qy_lot_kind'),
                    cell: (row: QyLotAdminActivityBrief) =>
                      row.draw_mode === 'ball'
                        ? `${t('qy_lot_mode_ball')} · ${t('qy_lot_ball_issue_no', { no: row.issue_no ?? 0 })}`
                        : t(`qy_lot_kind_${row.kind}`),
                  },
                  {
                    id: 'status',
                    header: t('qy_common_status'),
                    cell: (row: QyLotAdminActivityBrief) => {
                      const outcomeKey = qyLotOutcomeKey(row.outcome)
                      return (
                        <span className='inline-flex flex-wrap items-center gap-1.5'>
                          <QyStatusBadge
                            status={qyLotActivityBadgeStatus(
                              row.status,
                              row.outcome
                            )}
                          />
                          {outcomeKey != null && (
                            <Badge variant='secondary'>{t(outcomeKey)}</Badge>
                          )}
                          {/* 已下架的场次在这一列之外与在架的完全同形。
                              没有这枚徽标，运营在列表上分不出哪几场已经从
                              用户端撤下，只能逐个点进详情去看。 */}
                          {row.hidden_at > 0 && (
                            <Badge variant='outline'>
                              {t('qy_lot_hidden_badge')}
                            </Badge>
                          )}
                        </span>
                      )
                    },
                  },
                  {
                    id: 'entries',
                    header: t('qy_lot_entries_count'),
                    cellClassName: 'tabular-nums',
                    cell: (row: QyLotAdminActivityBrief) => row.active_count,
                  },
                  {
                    id: 'pool',
                    header: t('qy_lot_pool'),
                    cell: (row: QyLotAdminActivityBrief) => (
                      <QyAmountText quota={row.pool_quota} />
                    ),
                  },
                  {
                    id: 'payout',
                    header: t('qy_lot_a_stat_payout'),
                    cell: (row: QyLotAdminActivityBrief) => (
                      <QyAmountText quota={row.payout_quota} />
                    ),
                  },
                  {
                    id: 'net',
                    header: t('qy_lot_a_net'),
                    cell: (row: QyLotAdminActivityBrief) => (
                      // 净值 = 收进来的参与费 − 发出去的奖 − 退回去的钱。
                      // 抽奖里它经常是负数，那是正常的（平台出奖品），
                      // 所以必须带符号显示而不是取绝对值。
                      <QyAmountText
                        quota={
                          row.pool_quota - row.payout_quota - row.refund_quota
                        }
                        signed
                      />
                    ),
                  },
                  {
                    id: 'draw_at',
                    header: t('qy_lot_draw_at'),
                    cell: (row: QyLotAdminActivityBrief) =>
                      formatQyTs(row.draw_at),
                  },
                  {
                    id: 'flags',
                    header: t('qy_lot_a_flags'),
                    cell: (row: QyLotAdminActivityBrief) =>
                      row.open_flag_count > 0 ? (
                        <Badge variant='destructive' className='gap-1'>
                          <TriangleAlert
                            aria-hidden='true'
                            className='size-3'
                          />
                          {row.open_flag_count}
                        </Badge>
                      ) : (
                        <span className='text-muted-foreground'>-</span>
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
            </div>
          </QyPageBoundary>
        </div>
      </QySectionPageLayout.Content>

      <QyResponsiveDialog
        open={queueOpen}
        onOpenChange={setQueueOpen}
        title={t('qy_lot_a_tab_fulfill_queue')}
      >
        {/* actNo 传空串 = 跨活动。这正是后端 /admin/lottery/text-prizes 的默认口径。 */}
        <QyLotFulfillQueueTab actNo='' />
      </QyResponsiveDialog>

      <QyResponsiveDialog
        open={seriesOpen}
        onOpenChange={setSeriesOpen}
        title={t('qy_lot_ball_series_manage')}
        description={t('qy_lot_ball_series_manage_desc')}
        contentClassName='sm:max-w-4xl'
      >
        <QyLotSeriesPanel />
      </QyResponsiveDialog>

      <QyLotCreateWizard
        open={createOpen}
        onOpenChange={setCreateOpen}
        config={configQuery.data}
      />
    </QySectionPageLayout>
  )
}
