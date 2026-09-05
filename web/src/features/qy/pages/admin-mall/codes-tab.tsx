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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ShieldAlert, Ticket } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyErrorMessage } from '../../lib/api'
import { qyArray } from '../../lib/array'
import { qyKeys } from '../../lib/query-keys'
import { QyStatGrid, type QyStatItem } from '../components/qy-stat-grid'
import { qyAdminMallProductsQuery, uploadQyMallCodes } from './api'
import {
  QY_MALL_CODE_MAX_RUNES,
  QY_MALL_CODE_UPLOAD_MAX,
  qyMallParseCodes,
} from './lib/codes'
import type { QyMallCodesUploadResult } from './types'

/**
 * 码库存（第二张标签）：选一件 `code` 商品 → 粘贴一行一条 → 入库。
 *
 * ## 明文只出现在这一次请求体里
 *
 * 入库即 AES-GCM 密文（后端 `sealCode`），请求体登记在 `credentialBodyRoutes`
 * 不进请求台账；前端这边也一样：不进 react-query、不进 localStorage、
 * 上传成功即清空文本框。留着"方便再传一次"的那份明文就是一份没人管的码库。
 *
 * ## 不得上架本站余额兑换码（D-K）
 *
 * 星屑不可兑回余额；"星屑 → 码 → `users.quota`" 隔了一跳就把这条纪律绕过去了。
 * 后端可能对每条明文点查主库 `redemptions.key`，命中即 rejected；但那只是机器
 * 闸门，运营口径要写在这一屏上。
 *
 * ## 结果逐条回报
 *
 * `accepted` 一个数 + `rejected[{index, reason}]`：粘贴 200 行里第 37 行是空的
 * / 重复的 / 超长的，运营需要知道是**哪一行**，而不是"有 3 条失败"。
 */
export function QyMallAdminCodesTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const productId = useId()
  const textId = useId()
  const [productNo, setProductNo] = useState('')
  const [text, setText] = useState('')
  const [result, setResult] = useState<QyMallCodesUploadResult | null>(null)

  // 只拉 code 类。上限 100 是后端分页硬顶；一个站点不会有一百件兑换码商品。
  const productsQuery = useQuery(
    qyAdminMallProductsQuery({ page: 1, page_size: 100, kind: 'code' })
  )
  const products = qyArray(productsQuery.data?.items)
  const selected = products.find((row) => row.product_no === productNo) ?? null

  const codes = qyMallParseCodes(text)
  const tooMany = codes.length > QY_MALL_CODE_UPLOAD_MAX
  const tooLong = codes.some(
    (code) => [...code].length > QY_MALL_CODE_MAX_RUNES
  )

  const upload = useMutation({
    mutationFn: () => uploadQyMallCodes(productNo, codes),
    onSuccess: async (data) => {
      setResult(data)
      // 上传成功就把明文清掉：这份文本框是整个前端里唯一持有明文的地方。
      setText('')
      toast.success(t('qy_mladm_codes_uploaded', { count: data.accepted }))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const stats: QyStatItem[] =
    selected == null
      ? []
      : [
          {
            key: 'unused',
            label: t('qy_mladm_stock_unused'),
            value: selected.code_stock.unused,
            emphasis: true,
          },
          {
            key: 'issued',
            label: t('qy_mladm_stock_issued'),
            value: selected.code_stock.issued,
          },
          {
            key: 'revoked',
            label: t('qy_mladm_stock_revoked'),
            value: selected.code_stock.revoked,
          },
        ]

  const canUpload =
    selected != null &&
    codes.length > 0 &&
    !tooMany &&
    !tooLong &&
    !upload.isPending

  return (
    <QyPageBoundary
      query={productsQuery}
      isEmpty={productsQuery.data != null && products.length === 0}
      emptyIcon={Ticket}
      emptyTitle={t('qy_mladm_codes_no_product_title')}
      emptyDescription={t('qy_mladm_codes_no_product_desc')}
    >
      <div className='space-y-4'>
        <Alert>
          <ShieldAlert />
          <AlertTitle>{t('qy_mladm_codes_policy_title')}</AlertTitle>
          <AlertDescription>{t('qy_mladm_codes_policy_desc')}</AlertDescription>
        </Alert>

        <div className='space-y-1.5'>
          <Label htmlFor={productId}>{t('qy_mladm_codes_product')}</Label>
          <NativeSelect
            id={productId}
            className='w-full sm:w-96'
            value={productNo}
            onChange={(event) => {
              setProductNo(event.target.value)
              setResult(null)
            }}
          >
            <NativeSelectOption value=''>
              {t('qy_mladm_codes_product_pick')}
            </NativeSelectOption>
            {products.map((row) => (
              <NativeSelectOption key={row.product_no} value={row.product_no}>
                {row.title}
                {row.enabled ? '' : ` (${t('qy_common_off')})`}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>

        <QyStatGrid items={stats} />

        {selected != null && (
          <div className='space-y-1.5'>
            <Label htmlFor={textId}>{t('qy_mladm_codes_paste')}</Label>
            <Textarea
              id={textId}
              rows={10}
              value={text}
              autoComplete='off'
              spellCheck={false}
              placeholder={t('qy_mladm_codes_paste_ph')}
              aria-invalid={tooMany || tooLong}
              disabled={upload.isPending}
              className='font-mono text-xs'
              onChange={(event) => {
                setText(event.target.value)
                setResult(null)
              }}
            />
            <p
              className={
                tooMany || tooLong
                  ? 'text-destructive text-xs tabular-nums'
                  : 'text-muted-foreground text-xs tabular-nums'
              }
            >
              {tooLong
                ? t('qy_mladm_codes_too_long', {
                    max: QY_MALL_CODE_MAX_RUNES,
                  })
                : t('qy_mladm_codes_count', {
                    count: codes.length,
                    max: QY_MALL_CODE_UPLOAD_MAX,
                  })}
            </p>
            <Button
              type='button'
              disabled={!canUpload}
              onClick={() => upload.mutate()}
            >
              {t('qy_mladm_codes_submit')}
            </Button>
          </div>
        )}

        {result != null && (
          <div className='space-y-2 rounded-lg border p-3 text-sm'>
            <p>
              {t('qy_mladm_codes_result', {
                accepted: result.accepted,
                rejected: qyArray(result.rejected).length,
              })}
            </p>
            {qyArray(result.rejected).length > 0 && (
              <ul className='text-muted-foreground max-h-64 space-y-1 overflow-y-auto text-xs'>
                {qyArray(result.rejected).map((item) => (
                  // 同一行只会被拒一次，行号足够做 key。
                  <li key={item.index} className='flex gap-2'>
                    <span className='shrink-0 font-mono tabular-nums'>
                      {/* 后端 index 从 0 起，运营数的是"第几行"。 */}
                      {t('qy_mladm_codes_line_no', { no: item.index + 1 })}
                    </span>
                    <span className='min-w-0 break-words'>{item.reason}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </div>
    </QyPageBoundary>
  )
}
