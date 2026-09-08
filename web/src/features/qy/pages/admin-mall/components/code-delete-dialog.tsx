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

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { deleteQyMallCode } from '../api'
import type { QyMallAdminCode } from '../types'

/**
 * 删掉一枚**未使用**的码（传错了、传重了）。
 *
 * 已发出 / 已撤回 / 已提取的行删不掉（后端 409 `qy_ml_code_not_deletable`）：
 * 它们是"这枚码去哪了"的唯一答案，与订单、事件一样永久保留。所以列表上只对
 * `unused` 的行显示这个按钮，这里的 409 是兜底（列表数据可能是十几秒前的）。
 *
 * 不可逆但不动钱：删的是一枚还没发出去的库存，没有任何人的星屑会被扣或退。
 * 警示正文要把这一点说清楚，别让运营以为按下去会触发退款。
 */
export function QyMallCodeDeleteDialog(props: {
  productNo: string
  code: QyMallAdminCode | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const remove = useMutation({
    mutationFn: () => {
      if (props.code == null) throw new Error('no code')
      return deleteQyMallCode(props.productNo, props.code.id)
    },
    onSuccess: async () => {
      toast.success(t('qy_mladm_code_delete_done'))
      props.onClose()
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => {
      toast.error(qyErrorMessage(error, t))
      props.onClose()
    },
  })

  return (
    <QyConfirmDialog
      open={props.code != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_mladm_code_delete_title')}
      description={t('qy_mladm_code_delete_desc')}
      details={
        props.code == null ? null : (
          <div>
            <QyKeyValue label={t('qy_mladm_code_id')}>
              <span className='font-mono text-xs'>#{props.code.id}</span>
            </QyKeyValue>
            <QyKeyValue label={t('qy_mladm_code_created_at')}>
              {formatQyTs(props.code.created_at)}
            </QyKeyValue>
          </div>
        )
      }
      irreversible
      irreversibleDesc={t('qy_mladm_code_delete_irreversible')}
      confirmText={t('qy_common_delete')}
      isLoading={remove.isPending}
      onConfirm={() => remove.mutate()}
    />
  )
}
