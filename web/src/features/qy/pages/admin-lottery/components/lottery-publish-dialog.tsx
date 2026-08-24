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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { formatQyQuotaLedger } from '../../../lib/format'
import { qyKeys } from '../../../lib/query-keys'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { publishQyLotActivity } from '../api'
import type { QyLotAdminActivity } from '../types'

/**
 * 发布。**这是整个模块里最不可逆的一次点击。**
 *
 * 这一刻服务端会把参与条件、奖档 / 选项、四个时刻、算法版本、以及每一个影响
 * 结果的开关一起算进 `commit_hash` 写死。此后：
 *   · 上面这些字段全部只读；
 *   · 种子已经在 `draft` 时生成，现在它的哈希被公开，开奖时必须能对上；
 *   · 管理员在开奖这件事上只剩「整场取消」一个动作 —— 他能「不开」，不能
 *     「挑一个开」。
 *
 * 所以走 `QyConfirmDialog` 的 `irreversible` 分支：强制勾选确认框，
 * 并在明细区复述那几个即将被冻结的值。用户只会看这一屏。
 */
export function QyLotPublishDialog(props: {
  activity: QyLotAdminActivity
  /**
   * Σ(数量 × 额度)，来自详情接口的 `economics.prize_total_quota`。
   *
   * 抽奖派奖对用户余额是**净增发**，发出去没有任何回收路径 —— 这是发布这一屏
   * 上唯一会让平台亏钱的数，所以它必须出现在强制勾选的确认框旁边。竞猜恒为 0
   * （彩池制，平台数学上不可能倒贴），那一档不显示。
   */
  prizeTotalQuota: number
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { activity } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const mutation = useMutation({
    mutationFn: () => publishQyLotActivity(activity.act_no),
    onSuccess: async () => {
      toast.success(t('qy_lot_published'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyConfirmDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_publish_title')}
      description={t('qy_lot_publish_desc')}
      irreversible
      irreversibleDesc={
        props.prizeTotalQuota > 0
          ? t('qy_lot_publish_net_issue_note', {
              amount: formatQyQuotaLedger(props.prizeTotalQuota),
            })
          : undefined
      }
      isLoading={mutation.isPending}
      confirmText={t('qy_lot_publish_confirm')}
      details={
        <div>
          <QyKeyValue label={t('qy_lot_activity')}>{activity.title}</QyKeyValue>
          <QyKeyValue label={t('qy_lot_stake')}>
            <QyAmountText quota={activity.stake_quota} />
          </QyKeyValue>
          {props.prizeTotalQuota > 0 && (
            <QyKeyValue label={t('qy_lot_worst_net_issue')}>
              <QyAmountText quota={props.prizeTotalQuota} />
            </QyKeyValue>
          )}
          <QyKeyValue label={t('qy_lot_open_at')}>
            {formatQyTs(activity.open_at)}
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_close_at')}>
            {formatQyTs(activity.close_at)}
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_draw_at')}>
            {formatQyTs(activity.draw_at)}
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_allow_multi_win')}>
            {activity.allow_multi_win ? t('qy_common_on') : t('qy_common_off')}
          </QyKeyValue>
          {activity.kind === 'guess' && (
            <QyKeyValue label={t('qy_lot_fee_bps')}>
              {activity.fee_bps}
            </QyKeyValue>
          )}
          <QyKeyValue label={t('qy_lot_min_entries_field')}>
            {activity.min_entries_to_hold}
          </QyKeyValue>
        </div>
      }
      onConfirm={() => mutation.mutate()}
    />
  )
}
