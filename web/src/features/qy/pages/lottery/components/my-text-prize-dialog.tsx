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
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyLotMyPrizeQuery } from '../api'
import { QyLotFinePrint } from './lottery-fine-print'

/**
 * 「我中的文本奖」——只对中奖者本人展示。
 *
 * ## 为什么这里没有"自动发放"
 *
 * 项目方已拍板：文本奖由管理员手动履行。这不是省事 —— **正因为不自动发放，
 * 混合奖档才根本不存在"码发了钱没发"这个跨系统两阶段问题**。额度那一条腿有
 * 完整的两阶段 + 探针 + 代次，文本这一条腿压根不跨库，把两者捆成一个分布式
 * 事务是在给一个不存在的问题付代价。
 *
 * ## 必须对用户说清的不对称
 *
 * 发布时被承诺的只有「这一档是文本奖、叫什么、公开说明是什么、有几份」。
 * **实际的兑换码在开奖之后才由管理员填入，因此它没有任何承诺** —— 第三方能
 * 验的是"奖品的形状与数量在发布时就钉死、事后没被增发没被掉包"，而"我拿到的
 * 码是不是承诺的那一个"在本轮不可验证，因为它在承诺时根本不存在。
 *
 * 这句话原样写在弹窗里（`qy_lot_text_no_commit_notice`），不粉饰。
 */
export function QyLotMyTextPrizeDialog(props: {
  payoutNo: string | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const payoutNo = props.payoutNo ?? ''
  const query = useQuery(qyLotMyPrizeQuery(payoutNo, payoutNo !== ''))
  const prize = query.data

  return (
    <QyResponsiveDialog
      open={props.payoutNo != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_text_prize_title')}
      description={prize?.title}
    >
      <QyPageBoundary query={query}>
        {prize != null && (
          <div className='space-y-4'>
            <div>
              <QyKeyValue label={t('qy_lot_tier')}>
                {t('qy_lot_tier_no', { no: prize.tier })} {prize.name}
              </QyKeyValue>
              <QyKeyValue label={t('qy_lot_text_desc')}>
                <span className='break-words whitespace-pre-wrap'>
                  {prize.text_desc}
                </span>
              </QyKeyValue>
            </div>

            {prize.status === 'fulfilled' && prize.secret != null ? (
              <div className='space-y-2 rounded-lg border p-3'>
                <div className='flex flex-wrap items-center gap-2'>
                  <span className='font-mono text-sm break-all'>
                    {prize.secret}
                  </span>
                  <CopyButton value={prize.secret} />
                </div>
                {(prize.note ?? '') !== '' && (
                  <p className='text-muted-foreground text-xs break-words'>
                    {prize.note}
                  </p>
                )}
                <p className='text-muted-foreground text-xs'>
                  {t('qy_lot_fulfilled')} · {formatQyTs(prize.fulfilled_at)}
                </p>
              </div>
            ) : (
              // 未履行时给的是"还没轮到"，不是一个空框：空框会让用户以为
              // 奖品是空的，然后跑去开工单。
              <p className='text-muted-foreground rounded-lg border p-3 text-sm'>
                {t('qy_lot_text_pending')}
              </p>
            )}

            {/* 这条边界由后端随奖品下发（`notice`），前端只是原样显示。
                写死在前端也能显示，但那样它就是一句可以被悄悄改掉的话术；
                从后端来意味着它与发放这件事同源。拿不到时回落到本地文案，
                而不是把整段"这串码没有承诺"的说明整体省掉。

                折叠而不是删：这是一份 78 字的协议边界声明，压在一串刚拿到手的
                兑换码上面，绝大多数人此刻只想复制那串码。触发文字直说里面是
                什么（「这串内容没有进入承诺哈希」），要追究的人一点就展开。 */}
            <QyLotFinePrint label={t('qy_lot_text_no_commit_label')}>
              <p className='break-words whitespace-pre-wrap'>
                {prize.notice === ''
                  ? t('qy_lot_text_no_commit_notice')
                  : prize.notice}
              </p>
            </QyLotFinePrint>
          </div>
        )}
      </QyPageBoundary>
    </QyResponsiveDialog>
  )
}
