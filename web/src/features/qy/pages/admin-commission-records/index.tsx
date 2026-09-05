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
import { Link, useSearch } from '@tanstack/react-router'
import { ScrollText, Settings2, Users } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdDecimal } from '../../components/qy-sd-decimal'
import { qyArray } from '../../lib/array'
import { qyDaylineLabel, qyFormatAtDayline } from '../../lib/dayline'
import { qyTabTarget } from '../../lib/pages'
import {
  qyAdminAccrualsQuery,
  qyAdminCommissionHealthQuery,
} from '../admin-commission/api'
import type { QyAdminAccrual } from '../admin-commission/types'
import {
  BlockRelationDialog,
  type QyBlockRelationTarget,
} from '../admin-invite/components/block-relation-dialog'
import { QyPager } from '../components/qy-pager'
import { QY_PAGE_SIZE } from '../lib/constants'
import { ClawbackDialog } from './components/clawback-dialog'

/** 与后端 `commission/model.go` 的四个计佣行状态一致。 */
const STATUS_OPTIONS = ['accrued', 'settled', 'risk_hold', 'voided'] as const
// manual 是管理员手工增减佣金落下的账目行（`api_admin_adjust.go`）。它必须能被
// 单独筛出来：那是唯一没有业务单据触发的一类佣金，对账时第一个要看的就是它。
const SOURCE_OPTIONS = [
  'topup',
  'redemption',
  'consume',
  'clawback',
  'manual',
] as const

/**
 * 佣金审核（账本记的是星屑，界面按运营配的星屑单位名印）。
 *
 * 两个动作的语义边界必须分清，否则会误伤：
 *   - **冲正**：写一条负额计佣行并扣减余额，是"把已经发出去的钱要回来"；
 *   - **停止计佣**：只停止未来计佣，**不回收已发放的佣金**。它复用「邀请管理」
 *     那一个确认框（`admin-invite/components/block-relation-dialog`）：停 / 恢复
 *     邀请关系是 invite 模块的事，佣金与星屑两条线共用同一条关系、同一个开关。
 *
 * ── 「立即结算」不回来 ──
 * 项目方原话：「佣金审核的这个：立即结算 移除吧，全部由系统到时间自动结算。」
 * 撤掉按钮就必须同屏回答"那什么时候到账"，所以正文第一段是自动结算的时点，
 * 数据来自 `GET /admin/commission/health` 的 `daily_settle`（日界、下一轮开跑
 * 时刻、T+N），前端一个数都不自己算。结算之后到星屑余额那一跳由自动入账任务完成，
 * 逐笔在「佣金用户 → 入账记录」里看。
 *
 * ── 为什么是 Body 而不是整页 ──
 * 本页已被收进「结算台」的选择夹（`QY_TAB_GROUPS`），是第二张标签。区段头
 * （`GATE NN` + 大标题）由宿主页 `admin-settlement/hub.tsx` 出。
 * 旧地址 `/qy/admin/commission-records` 保留成重定向，`?inviter_id=` 一起转发。
 */
export function QyAdminCommissionRecordsBody() {
  const { t } = useTranslation()

  // 从佣金用户那张表下钻进来时,URL 上带着 `?inviter_id=412`。只拿它做**初值**、
  // 之后由输入框自己接管:双向同步会让每敲一个字符就压一条历史记录。
  //
  // `strict: false` 而不是绑死宿主页的路由 id：这一页是**一张标签**，渲染它的是
  // 宿主页那条路由（`/qy/admin/settlement`），而它自己那条路由只剩重定向。
  const search = useSearch({ strict: false })

  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('')
  const [sourceType, setSourceType] = useState('')
  const [inviterId, setInviterId] = useState(search.inviter_id ?? '')
  const [clawbackTarget, setClawbackTarget] = useState<QyAdminAccrual | null>(
    null
  )
  const [blockTarget, setBlockTarget] = useState<QyBlockRelationTarget | null>(
    null
  )

  const query = useQuery(
    qyAdminAccrualsQuery({
      p: page,
      page_size: QY_PAGE_SIZE,
      status,
      source_type: sourceType,
      inviter_id: inviterId.trim(),
    })
  )
  const items = qyArray(query.data?.items)

  // 自动结算的时点。撤掉手动入口就必须把"系统什么时候替你做这件事"写在同一屏上。
  const settleSnapshot = useQuery(qyAdminCommissionHealthQuery()).data
    ?.daily_settle

  const columns: StaticDataTableColumn<QyAdminAccrual>[] = [
    {
      id: 'created_at',
      header: t('qy_common_time'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactMutedCell,
      cell: (row) => formatTimestampToDate(row.created_at),
    },
    {
      id: 'inviter',
      header: t('qy_cm_inviter'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => `#${row.inviter_id}`,
    },
    {
      id: 'invitee',
      header: t('qy_aff_invitee'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      // 关系当前被停时打标：不然运营点开确认框才知道自己按的是「恢复」，
      // 而这一列恰恰是他判断"这条关系现在是什么状态"的地方。
      cell: (row) => (
        <span className='inline-flex items-center gap-1.5'>
          {`#${row.invitee_id}`}
          {row.relation_blocked && (
            <Badge variant='destructive'>{t('qy_rel_state_blocked')}</Badge>
          )}
        </span>
      ),
    },
    {
      id: 'source',
      header: t('qy_aff_source'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => t(`qy_aff_src_${row.source_type}`, row.source_type),
    },
    {
      id: 'base',
      header: t('qy_aff_base_quota'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      cell: (row) => <QyAmountText quota={row.base_quota} />,
    },
    {
      id: 'gross',
      header: t('qy_aff_gross'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactNumericCell,
      // gross 是**星屑**（= base_quota × 费率 / 刻度），与左边那一列不是同一个
      // 单位。两列并排是刻意的："花了多少额度、返了多少星屑"是用户与运营唯一
      // 能自己验算的那条式子；把它们印成同一个单位，验算就变成了一句谎话。
      // 原样印 decimal(30,10) 换不来精度，只换来一列 `0.3700000000`，所以去零。
      cell: (row) => <QySdDecimal value={row.gross_amount} />,
    },
    {
      id: 'settled',
      header: t('qy_cm_settled_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QySdDecimal value={row.settled_amount} />,
    },
    {
      id: 'status',
      header: t('qy_common_status'),
      className: staticDataTableClassNames.compactHeaderCell,
      cellClassName: staticDataTableClassNames.compactCell,
      cell: (row) => (
        <span className='inline-flex items-center gap-1.5'>
          <Badge
            variant={row.status === 'risk_hold' ? 'destructive' : 'secondary'}
          >
            {t(`qy_aff_st_${row.status}`, row.status)}
          </Badge>
          {row.risk_flags !== '' && (
            <Badge variant='outline'>{row.risk_flags}</Badge>
          )}
        </span>
      ),
    },
    {
      id: 'actions',
      header: t('qy_common_actions'),
      className: staticDataTableClassNames.actionHeaderCell,
      cellClassName: staticDataTableClassNames.actionCell,
      cell: (row) => (
        <div className='flex justify-end gap-1'>
          {/* 「停止计佣」只在这一行**真的挂在一条邀请关系上**时才渲染。
              手工调整落下的计佣行（`source_type = manual`）的 `invitee_id` 是 0：
              它不是任何人邀请任何人产生的，后端对 `invitee_id <= 0` 直接 400。
              按钮的方向由 `relation_blocked` 决定：本页既能停、也能恢复。 */}
          {row.invitee_id > 0 ? (
            <Button
              variant='ghost'
              size='sm'
              onClick={() =>
                setBlockTarget({
                  inviteeId: row.invitee_id,
                  blocked: row.relation_blocked,
                })
              }
            >
              {row.relation_blocked ? t('qy_cm_unblock') : t('qy_cm_block')}
            </Button>
          ) : (
            // 不渲染按钮，但也不留一片空白：运营需要知道"这一行为什么没有这个
            // 动作"，否则他会以为是页面坏了，转而去别处找同一个按钮。
            <span className='text-muted-foreground self-center text-xs'>
              {t('qy_cm_block_na')}
            </span>
          )}
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setClawbackTarget(row)}
          >
            {t('qy_cm_clawback')}
          </Button>
        </div>
      ),
    },
  ]

  const resetPage = () => setPage(1)

  return (
    <div className='space-y-3'>
      {/* 撤掉「立即结算」之后，"什么时候到账"必须写在同一屏上。三个数全部来自
          后端 `daily_settle`，前端一个都不复刻：T+N 里那个 +1（桶要等一整天
          结束才封板）已经在 `payoutDayOffset` 上错过一次，不能再有第二份口径。
          取不到快照时退化成不带数字的那句话，而不是印一串「—」。 */}
      <div className='text-muted-foreground space-y-1 text-sm'>
        <p>
          {settleSnapshot == null
            ? t('qy_cm_auto_settle_plain')
            : t('qy_cm_auto_settle', {
                // 日界标签与「下一轮开跑」的时刻必须用**同一个偏移**渲染。
                dayline: qyDaylineLabel(settleSnapshot.day_offset_minutes),
                days: settleSnapshot.payout_day_offset,
                next: qyFormatAtDayline(
                  settleSnapshot.next_run_after,
                  settleSnapshot.day_offset_minutes
                ),
              })}
        </p>
        <p>
          {/* 手动补救仍然在，只是不在这一页上。不写这一句的话，运维在结算卡住
              时会以为整条手动通路被删了（后端接口其实还在）。 */}
          {t('qy_cm_auto_settle_fallback')}{' '}
          <Link
            to='/qy/admin/commission'
            hash='qy-daily-settle'
            className='underline underline-offset-2'
          >
            {t('qy_cm_ds_title')}
          </Link>
        </p>
        {/* 结算之后到星屑余额那一跳由自动入账任务完成：这句话回答的是"已结算的钱
            去哪了"，与上面"什么时候结算"是两个问题。 */}
        <p>{t('qy_cm_auto_credit_note')}</p>
      </div>

      {/* 佣金用户是隔壁那张标签，佣金配置在系统设置抽屉里。这几个按钮不是重复：
          侧栏回答"从零开始去哪找"，这里回答"我正看着这一笔，另外那几张表怎么开"。

          它们**跟着正文走、不进宿主页的 Actions 槽**：那个槽是三张标签共用的，
          而这两个入口只对佣金审核这一屏成立。跳转走 `qyTabTarget`：直接 to 旧地址
          也到得了（旧路由会重定向），但那是**先离开再被弹回来**的一次白闪。 */}
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          size='sm'
          render={<Link {...qyTabTarget('/qy/admin/commission-users')} />}
        >
          <Users aria-hidden='true' />
          {t('qy_nav_a_commission_users')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/commission' />}
        >
          <Settings2 aria-hidden='true' />
          {t('qy_nav_a_commission')}
        </Button>
      </div>

      <div className='space-y-3'>
        <div className='flex flex-wrap items-center gap-2'>
          <NativeSelect
            size='sm'
            aria-label={t('qy_common_status')}
            value={status}
            onChange={(event) => {
              resetPage()
              setStatus(event.target.value)
            }}
          >
            <NativeSelectOption value=''>
              {t('qy_common_all')}
            </NativeSelectOption>
            {STATUS_OPTIONS.map((value) => (
              <NativeSelectOption key={value} value={value}>
                {t(`qy_aff_st_${value}`)}
              </NativeSelectOption>
            ))}
          </NativeSelect>

          <NativeSelect
            size='sm'
            aria-label={t('qy_aff_source')}
            value={sourceType}
            onChange={(event) => {
              resetPage()
              setSourceType(event.target.value)
            }}
          >
            <NativeSelectOption value=''>
              {t('qy_cm_all_sources')}
            </NativeSelectOption>
            {SOURCE_OPTIONS.map((value) => (
              <NativeSelectOption key={value} value={value}>
                {t(`qy_aff_src_${value}`)}
              </NativeSelectOption>
            ))}
          </NativeSelect>

          <Input
            className='h-8 w-44'
            inputMode='numeric'
            value={inviterId}
            placeholder={t('qy_cm_inviter_id_ph')}
            onChange={(event) => {
              resetPage()
              setInviterId(event.target.value)
            }}
          />
        </div>

        <QyPageBoundary
          query={query}
          isEmpty={items.length === 0}
          emptyIcon={ScrollText}
          emptyTitle={t('qy_cm_empty_title')}
          emptyDescription={t('qy_cm_empty_desc')}
        >
          <div className='w-full overflow-x-auto'>
            <StaticDataTable
              columns={columns}
              data={items}
              getRowKey={(row) => row.accrual_no}
              tableClassName='min-w-[1100px]'
            />
          </div>
          <QyPager
            page={page}
            pageSize={QY_PAGE_SIZE}
            total={query.data?.total ?? 0}
            disabled={query.isFetching}
            onPageChange={setPage}
          />
        </QyPageBoundary>
      </div>

      <ClawbackDialog
        accrual={clawbackTarget}
        onClose={() => setClawbackTarget(null)}
      />

      {/* 停止 / 恢复计佣共用邀请管理那一个弹窗：方向由行上的当前状态决定，
          「停止计返」与「解绑」的区别写在里面。 */}
      <BlockRelationDialog
        target={blockTarget}
        onClose={() => setBlockTarget(null)}
      />
    </div>
  )
}
