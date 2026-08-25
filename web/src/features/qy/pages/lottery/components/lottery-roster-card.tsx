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
import { qyLotBallHits, qyLotBallSafeParsePick } from '../lib/ball'
import { qyLotEntryBadgeStatus, qyLotMaskRef } from '../lib/display'
import type { QyLotActivityDetail, QyLotProofEntry } from '../types'
import { QyLotBallNumbers } from './lottery-ball-numbers'
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
 *
 * ## 默认展开 + 展示层打码
 *
 * 名单默认**展开**：一份要多点一下才看得到的公示名单，在"公示"这件事上等于
 * 没有。同时两串长标识按 {@link qyLotMaskRef} 打码 —— 标识虽不是身份，却是
 * 一条场内的关联线（谁晒过自己的报名回执，谁在这一场里的每一注就都被钉在
 * 一起）。打的是**展示层**：接口下发的、本地复算读的、第三方从证据链端点拿到
 * 的都仍然是原值，所以公正性验证一个字节都没受影响。要逐行比对哈希的人打开
 * 「显示完整标识」即可。
 */
export function QyLotRosterCard(props: { activity: QyLotActivityDetail }) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  // 打码是**默认**，不是唯一形态。核对哈希是一次刻意动作，配一颗开关给它。
  const [revealed, setRevealed] = useState(false)
  const showRef = (value: string) => (revealed ? value : qyLotMaskRef(value))

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
  const isBall = props.activity.draw_mode === 'ball'
  // 开奖号取自活动记录而不是这份证据链：封盘之后、开奖之前两者都为空，
  // 而开奖之后它们是同一个值（证据链的 ball_result 就是从这一行读的）。
  // 少一次可空判断，也少一个"这两处为什么不一样"的问题。
  const drawn = qyLotBallSafeParsePick(props.activity.ball_result ?? '')

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
          /*
            名单表**默认展开**（项目方本轮明确要求：参与名单默认公开可见）。

            上一轮把它折起来的理由是字数：真实的 entry_no（27 字）与 user_ref
            （32 字）让每行成本约 80 字，满页 20 行是 2507 字。那个理由现在由
            打码接手 —— 两串各压到 11 位，每行成本降到 40 字上下，而"公示名单
            要点一下才看得到"这件事本身比字数更糟。

            折叠位留着：要收起来的人点一下即可，而收起时 Base UI 的 Collapsible
            不挂载面板，长名单不会一直压在页面上。
          */
          <QyLotFinePrint
            defaultOpen
            label={t('qy_lot_roster_open_label', {
              count: query.data?.total ?? entries.length,
            })}
          >
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
                  // 打码只发生在这里。`row.entry_no` 那份原值仍然完整地躺在
                  // 查询结果里，本地复算与第三方复算读的都是它。
                  cell: (row: QyLotProofEntry) => showRef(row.entry_no),
                },
                {
                  id: 'user_ref',
                  header: t('qy_lot_user_ref'),
                  cellClassName: 'font-mono text-xs',
                  cell: (row: QyLotProofEntry) => showRef(row.user_ref),
                },
                // 双色球换一列：`opt_no` 在这套玩法里恒为 0，于是这一列
                // 逐行显示 `-`，而**每一注买的号**（进哈希链、进名单原像的那份
                // 字节，接口一直下发在 `pick` 上）一格都没画出来。公开名单因此
                // 回答不了任何人的"这一期谁押中了"，包括看自己那一行的人。
                isBall
                  ? {
                      id: 'pick',
                      // 这张表列的是**全场每一个人**的号，不是我自己买的，
                      // 表头写「你的选号」会让人以为下面每一行都是自己的。
                      header: t('qy_lot_ball_pick_col'),
                      cell: (row: QyLotProofEntry) => (
                        <QyLotBallNumbers
                          hits={qyLotBallHits(
                            qyLotBallSafeParsePick(row.pick ?? ''),
                            drawn
                          )}
                          pick={row.pick ?? ''}
                          size='sm'
                        />
                      ),
                    }
                  : {
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
            {/* 打码是展示层的事，证据链里的原值一个字节都没动。要逐行比对
                哈希的人打开这颗开关即可 —— 而那是一次刻意动作，不是默认。 */}
            <button
              type='button'
              aria-pressed={revealed}
              className='text-muted-foreground hover:text-foreground mt-2 text-xs underline'
              onClick={() => setRevealed((prev) => !prev)}
            >
              {revealed
                ? t('qy_lot_roster_mask_on')
                : t('qy_lot_roster_reveal')}
            </button>
            <p className='text-muted-foreground mt-2 text-xs'>
              {t('qy_lot_roster_mask_note')}
            </p>
            <p className='text-muted-foreground mt-2 text-xs'>
              {t('qy_lot_roster_status_note')}
            </p>
          </QyLotFinePrint>
        )}
      </CardContent>
    </Card>
  )
}
