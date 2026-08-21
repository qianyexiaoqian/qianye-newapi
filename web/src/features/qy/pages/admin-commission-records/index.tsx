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
import { Link2, ScrollText, Settings2, Users, Wallet } from 'lucide-react'
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
import { qyTabTarget } from '../../lib/pages'
import {
  qyAdminAccrualsQuery,
  qyAdminCommissionHealthQuery,
} from '../admin-commission/api'
import {
  BlockRelationDialog,
  type QyBlockRelationTarget,
} from '../admin-commission/components/block-relation-dialog'
import type { QyAdminAccrual } from '../admin-commission/types'
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
 * 佣金审核。
 *
 * 三个动作的语义边界必须分清，否则会误伤：
 *   - **冲正**：写一条负额计佣行并扣减余额，是"把已经发出去的钱要回来"；
 *   - **拉黑关系**：只停止未来计佣，**不回收已发放的佣金**。
 *
 * ── 「立即结算」已经移除 ──
 * 项目方原话：「佣金审核的这个：立即结算 移除吧，全部由系统到时间自动结算。」
 * 那个按钮此前挂在每一行上，按人触发一次结算。移除的只是**这个按钮**，后端
 * `POST /api/qy/admin/commission/settle` 原样保留（理由见下方那段说明与
 * `qianye/modules/commission/module.go`）。
 *
 * 撤掉按钮就必须同屏回答"那什么时候到账" —— 否则运营点不到、也不知道要等
 * 多久，只会来问人。所以正文第一段是自动结算的时点，数据来自
 * `GET /admin/commission/health` 的 `daily_settle`（日界、下一轮开跑时刻、
 * T+N），前端一个数都不自己算。
 *
 * ── 为什么是 Body 而不是整页 ──
 * 本页已被收进「结算台」的选择夹（`QY_TAB_GROUPS`），是第二张标签。区段头
 * （`GATE NN` + 大标题）由宿主页 `admin-settlement/hub.tsx` 出。
 * 旧地址 `/qy/admin/commission-records` 保留成重定向，`?inviter_id=` 一起转发。
 */
export function QyAdminCommissionRecordsBody() {
  const { t } = useTranslation()

  // 从佣金余额那张表下钻进来时,URL 上带着 `?inviter_id=412`。只拿它做**初值**、
  // 之后由输入框自己接管:双向同步会让每敲一个字符就压一条历史记录,而运营改完
  // 筛选按返回键期望回到上一个页面,不是回到"少打一个字"的那一帧。
  //
  // `strict: false` 而不是绑死宿主页的路由 id：这一页现在是**一张标签**，
  // 渲染它的是宿主页那条路由（`/qy/admin/settlement`），而它自己那条路由只剩
  // 重定向。写死 `from` 会让"这块正文将来挂到哪个宿主上"变成它的编译期依赖 ——
  // 上一次搬家就是这么断的：`from` 还指着旧路由，而旧路由已经不渲染任何东西，
  // 运行期直接抛 "Could not find an active match"。
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
  const items = query.data?.items ?? []

  // 自动结算的时点。它与「立即结算」按钮是**同一次改动的两面**：撤掉手动入口
  // 就必须把"系统什么时候替你做这件事"写在同一屏上。
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
      // 与左边的「计佣基数」同一个单位（gross = base_quota × 费率），所以走
      // 同一个展示件。原样印 decimal(30,10) 字符串换不来精度 —— 展示只留 4 位
      // 小数 —— 只换来一列 `1370.0000000000` 挨着一列 `$2.74`。
      cell: (row) => <QyAmountText quota={row.gross_amount} />,
    },
    {
      id: 'settled',
      header: t('qy_cm_settled_amount'),
      className: staticDataTableClassNames.compactHeaderCellRight,
      cellClassName: staticDataTableClassNames.compactMutedNumericCell,
      cell: (row) => <QyAmountText quota={row.settled_amount} />,
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
              它不是任何人邀请任何人产生的，后端 `adminBlockRelation` 对
              `invitee_id <= 0` 直接 400。此前这里无条件渲染，于是那些行上有一个
              点了必然报错的按钮 —— 而报出来的还是一句让人以为扣了钱的话。

              按钮的方向由 `relation_blocked` 决定，与用户佣金页那一处完全一致。
              此前这里写死 `blocked: true`，于是本页只能停、不能恢复 ——
              项目方看的正是这一页，"停止计佣没法恢复"这个结论对它成立。 */}
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
                dayline: `UTC${settleSnapshot.day_offset_minutes >= 0 ? '+' : ''}${settleSnapshot.day_offset_minutes / 60}`,
                days: settleSnapshot.payout_day_offset,
                next: formatTimestampToDate(settleSnapshot.next_run_after),
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
      </div>

      {/* 佣金余额与 AFF 关系现在是「用户佣金」的两张标签（侧栏上那一行）。
          这几个按钮不是重复：侧栏回答"从零开始去哪找"，这里回答"我正看着这
          一笔，另外那几张表怎么开"。删掉它们运营就得绕回侧栏重新找一遍。

          它们**跟着正文走、不进宿主页的 Actions 槽**：那个槽是三张标签共用的，
          而这四个入口只对佣金审核这一屏成立 —— 放上去的话，运营在「日消费
          明细」标签上也会看到一排通往佣金表的按钮。

          跳转一律走 `qyTabTarget`：直接 `to='/qy/admin/commission-records/
          balances'` 也到得了（旧路由会重定向），但那是**先离开再被弹回来**，
          用户看到的是一次白闪，而且选中的标签由重定向那一跳决定。 */}
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          size='sm'
          render={
            <Link {...qyTabTarget('/qy/admin/commission-records/users')} />
          }
        >
          <Users aria-hidden='true' />
          {t('qy_cu_title')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          render={
            <Link {...qyTabTarget('/qy/admin/commission-records/relations')} />
          }
        >
          <Link2 aria-hidden='true' />
          {t('qy_rel_title')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          render={
            <Link {...qyTabTarget('/qy/admin/commission-records/balances')} />
          }
        >
          <Wallet aria-hidden='true' />
          {t('qy_cb_title')}
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

      {/* 停止 / 恢复计佣共用同一个弹窗：方向由行上的当前状态决定，
          「停止计佣」与「解绑」的区别写在里面。 */}
      <BlockRelationDialog
        target={blockTarget}
        onClose={() => setBlockTarget(null)}
      />
    </div>
  )
}
