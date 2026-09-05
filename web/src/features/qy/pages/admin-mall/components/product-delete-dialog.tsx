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
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { deleteQyMallProduct } from '../api'
import type { QyMallAdminProduct } from '../types'

/**
 * 删除商品。
 *
 * 有未完结订单（paid / shipped / held）时后端 409 `qy_ml_has_open_orders`：
 * 那句话已经登记在 `QY_ERROR_CODE_I18N`，这里只负责把它原样 toast 出来并关掉
 * 弹窗 —— 重试没有意义，得先把那几张单处理完。
 *
 * 不可逆但不动钱：删商品只清 unused 的码，订单与已发出的码永不删（它们是
 * 证据表），所以警示正文换成"不退不扣"的那一句，别让运营以为按下去会退款。
 */
export function QyMallProductDeleteDialog(props: {
  product: QyMallAdminProduct | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { product } = props

  const remove = useMutation({
    mutationFn: () => {
      if (product == null) throw new Error('no product')
      return deleteQyMallProduct(product.product_no)
    },
    onSuccess: async () => {
      toast.success(t('qy_mladm_delete_done'))
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
      open={product != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_mladm_delete_title')}
      description={t('qy_mladm_delete_desc')}
      details={
        product == null ? null : (
          <div>
            <QyKeyValue label={t('qy_mladm_col_title')}>
              {product.title}
            </QyKeyValue>
            <QyKeyValue label={t('qy_mladm_product_no')}>
              <span className='font-mono text-xs'>{product.product_no}</span>
            </QyKeyValue>
            {product.kind === 'code' && (
              <QyKeyValue label={t('qy_mladm_stock_unused')}>
                {product.code_stock.unused}
              </QyKeyValue>
            )}
          </div>
        )
      }
      irreversible
      irreversibleDesc={t('qy_mladm_delete_irreversible')}
      confirmText={t('qy_common_delete')}
      isLoading={remove.isPending}
      onConfirm={() => remove.mutate()}
    />
  )
}
