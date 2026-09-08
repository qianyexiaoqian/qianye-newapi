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
import { ShieldAlert } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import { uploadQyMallCodes } from '../api'
import {
  QY_MALL_CODE_MAX_RUNES,
  QY_MALL_CODE_UPLOAD_MAX,
  qyMallParseCodes,
} from '../lib/codes'
import type { QyMallCodesUploadResult } from '../types'

/**
 * 添加码库存：选好商品之后粘贴一行一条，入库即密文。
 *
 * ## 明文只出现在这一次请求体里
 *
 * 后端 `sealCode` 落库前就加密，这条路由登记在 `credentialBodyRoutes`、请求台账
 * 不存 body；前端这边也一样：不进 react-query、不进 localStorage，上传成功即
 * 清空文本框并关窗。留着"方便再传一次"的那份明文就是一份没人管的码库。
 *
 * ## 结果逐条回报
 *
 * `accepted` 一个数 + `rejected[{index, reason}]`：粘贴 200 行里第 37 行是空的 /
 * 重复的 / 超长的 / 是本站余额兑换码，运营需要知道是**哪一行**，而不是"有 3 条
 * 失败"。所以有拒绝清单时**不自动关窗**，让人先看完再关。
 */
export function QyMallCodeUploadDialog(props: {
  open: boolean
  productNo: string
  productTitle: string
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const textId = useId()
  const [text, setText] = useState('')
  const [result, setResult] = useState<QyMallCodesUploadResult | null>(null)

  // 换一件商品、或重新打开，都要把上一轮的明文与结果丢掉。
  useEffect(() => {
    setText('')
    setResult(null)
  }, [props.open, props.productNo])

  const codes = qyMallParseCodes(text)
  const tooMany = codes.length > QY_MALL_CODE_UPLOAD_MAX
  const tooLong = codes.some((code) => [...code].length > QY_MALL_CODE_MAX_RUNES)

  const upload = useMutation({
    mutationFn: () => uploadQyMallCodes(props.productNo, codes),
    onSuccess: async (data) => {
      setResult(data)
      // 上传成功就把明文清掉：这个文本框是整个前端里唯一持有明文的地方。
      setText('')
      toast.success(t('qy_mladm_codes_uploaded', { count: data.accepted }))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      // 全部入库才自动关窗；有被拒的行就留在原地，那份清单是这次唯一的线索。
      if (qyArray(data.rejected).length === 0) props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const canUpload =
    props.productNo !== '' &&
    codes.length > 0 &&
    !tooMany &&
    !tooLong &&
    !upload.isPending

  const rejected = qyArray(result?.rejected)

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      dismissible={false}
      title={t('qy_mladm_codes_add_title')}
      description={props.productTitle}
      contentClassName='sm:max-w-2xl'
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_close')}
          </Button>
          <Button
            type='button'
            disabled={!canUpload}
            onClick={() => upload.mutate()}
          >
            {t('qy_mladm_codes_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <Alert>
          <ShieldAlert />
          <AlertTitle>{t('qy_mladm_codes_policy_title')}</AlertTitle>
          <AlertDescription>{t('qy_mladm_codes_policy_desc')}</AlertDescription>
        </Alert>

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
              ? t('qy_mladm_codes_too_long', { max: QY_MALL_CODE_MAX_RUNES })
              : t('qy_mladm_codes_count', {
                  count: codes.length,
                  max: QY_MALL_CODE_UPLOAD_MAX,
                })}
          </p>
        </div>

        {result != null && (
          <div className='space-y-2 rounded-lg border p-3 text-sm'>
            <p>
              {t('qy_mladm_codes_result', {
                accepted: result.accepted,
                rejected: rejected.length,
              })}
            </p>
            {rejected.length > 0 && (
              <ul className='text-muted-foreground max-h-64 space-y-1 overflow-y-auto text-xs'>
                {rejected.map((item) => (
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
    </QyResponsiveDialog>
  )
}
