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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { qyLotProofQuery } from '../api'
import { qyLotEntryBadgeStatus } from '../lib/display'
import type { QyLotActivityDetail, QyLotProofEntry } from '../types'
import { QyLotFinePrint } from './lottery-fine-print'

const PAGE_SIZE = 20

/**
 * 公开参与名单。
 *
 * ## 为什么封盘之前不显示
 *
 * 名单在封盘那一刻才冻结并公开哈希（设计文档 T2）。开放期就把它逐条摊开，
 * 等于让所有人实时看到彼此的下注 —— 竞猜里这直接改变博弈（跟风押注、
 * 最后一秒压满获胜选项），而抽奖里它没有任何用处。
 *
 * ## 为什么显示的是 `user_ref` 而不是用户名
 *
 * `user_ref` 是每场活动独立加盐的稳定标识：同一个人在同一场里的多张票标同一个
 * `user_ref`（用户自己可以核对），但跨场无法关联，也无法反查回用户 ID。
 * 盐永不公开 —— 一旦公开，几万个 user_id 的空间可以被完整枚举反查。
 */
export function QyLotRosterCard(props: { activity: QyLotActivityDetail }) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)

  const ready =
    props.activity.status === 'locked' ||
    props.activity.status === 'settling' ||
    props.activity.status === 'finished'

  const query = useQuery(
    qyLotProofQuery(
      props.activity.act_no,
      { p: page, page_size: PAGE_SIZE },
      ready
    )
  )
  const entries = qyArray(query.data?.entries)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_lot_roster_title')}</CardTitle>
        <CardDescription>{t('qy_lot_roster_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        {!ready ? (
          // 封盘前这张卡一条数据都没有，此前却顶着两段说明（卡片描述 24 字 +
          // 这一句 24 字）。结论留在明面上，理由折起来。
          <QyLotFinePrint label={t('qy_lot_roster_sealed')}>
            <p>{t('qy_lot_roster_sealed_why')}</p>
          </QyLotFinePrint>
        ) : (
          <>
            <StaticDataTable
              data={entries}
              getRowKey={(row: QyLotProofEntry) => row.entry_no}
              empty={entries.length === 0}
              emptyContent={t('qy_lot_roster_empty')}
              columns={[
                {
                  id: 'seq',
                  header: t('qy_lot_seq'),
                  cellClassName: 'tabular-nums',
                  cell: (row: QyLotProofEntry) => row.seq,
                },
                {
                  id: 'entry_no',
                  header: t('qy_lot_entry_no'),
                  cellClassName: 'font-mono text-xs',
                  cell: (row: QyLotProofEntry) => row.entry_no,
                },
                {
                  id: 'user_ref',
                  header: t('qy_lot_user_ref'),
                  cellClassName: 'font-mono text-xs',
                  cell: (row: QyLotProofEntry) => row.user_ref,
                },
                {
                  id: 'opt_no',
                  header: t('qy_lot_option'),
                  cellClassName: 'tabular-nums',
                  cell: (row: QyLotProofEntry) =>
                    row.opt_no === 0 ? '-' : row.opt_no,
                },
                {
                  id: 'amount',
                  header: t('qy_common_amount'),
                  cell: (row: QyLotProofEntry) => (
                    <QyAmountText quota={row.amount} />
                  ),
                },
                {
                  id: 'status',
                  header: t('qy_common_status'),
                  cell: (row: QyLotProofEntry) => (
                    <QyStatusBadge status={qyLotEntryBadgeStatus(row.status)} />
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
            {/* 链刻意不含 status —— 否则每次扣费失败都会破链。所以"这一条到底
                成没成"由资金单交叉佐证，这句话必须写出来，不能让用户以为
                哈希链保证了它。折叠而不是删：它只对"正在拿这张表核对哈希"的人
                有意义，而那个人一定会点开这一层。 */}
            <QyLotFinePrint label={t('qy_lot_roster_status_label')}>
              <p>{t('qy_lot_roster_status_note')}</p>
            </QyLotFinePrint>
          </>
        )}
      </CardContent>
    </Card>
  )
}
