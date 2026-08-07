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
import { Info } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../../../../components/qy-responsive-dialog'
import type { QyUgrCreateRequest } from '../types'

/**
 * 新建一个用户分组。
 *
 * ── 为什么名字之外只有两个字段 ──
 *
 * 一个刚建出来的用户分组是**空的**:0 个人、0 个令牌、没有任何授权。它的
 * 可用模型分组与倍率在另一处编辑(「用户分组 × 模型分组」),默认模型分组在
 * 登记表那一行上编辑。在这里把那些也摆出来,等于让运营在一个还没有任何人的
 * 分组上先配一遍权限与价格,而其中每一项都需要单独的预览与闸门。
 *
 * ── 名字的校验为什么不在前端做 ──
 *
 * 后端那一道有五条判据(长度按 rune、不能叫 auto、与已登记的用户分组重名、
 * 与模型分组 roster 跨命名空间冲突、以及"已经有人在用只是还没登记")。
 * 最后两条要读 `options.GroupRatio`、abilities 与 `users.group` 的现值 ——
 * 前端抄一份必然与服务端漂移,而漂移的方向是"这里说能用、提交回一句看不懂的
 * 报错"。这里只挡住空串(那连一次请求都不值得发)。
 */
export function QyUgrCreateDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  draft: QyUgrCreateRequest
  onDraftChange: (patch: Partial<QyUgrCreateRequest>) => void
  isSaving: boolean
  onConfirm: () => void
}) {
  const { t } = useTranslation()

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_ugr_create_title')}
      description={t('qy_ugr_create_desc')}
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            disabled={props.isSaving || props.draft.name.trim() === ''}
            onClick={props.onConfirm}
          >
            {props.isSaving ? t('Saving...') : t('Save')}
          </Button>
        </>
      }
    >
      <div className='space-y-3 text-sm'>
        <div className='space-y-1.5'>
          <Label htmlFor='qy-ugr-new-name'>{t('qy_ugr_field_name')}</Label>
          <Input
            id='qy-ugr-new-name'
            value={props.draft.name}
            placeholder={t('qy_ugr_field_name_placeholder')}
            onChange={(event) =>
              props.onDraftChange({ name: event.target.value })
            }
          />
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor='qy-ugr-new-display'>
            {t('qy_ugr_field_display_name')}
          </Label>
          <Input
            id='qy-ugr-new-display'
            value={props.draft.display_name ?? ''}
            onChange={(event) =>
              props.onDraftChange({ display_name: event.target.value })
            }
          />
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor='qy-ugr-new-note'>{t('qy_ugr_field_note')}</Label>
          <Input
            id='qy-ugr-new-note'
            value={props.draft.note ?? ''}
            placeholder={t('qy_ugr_field_note_placeholder')}
            onChange={(event) =>
              props.onDraftChange({ note: event.target.value })
            }
          />
        </div>

        {/*
          「建好了但还不能用」必须在按下按钮**之前**就说清楚,而不是等运营把
          人挪进去之后自己去撞 403 / 503。后端还会在返回值里带一份现算的
          `warnings`(它读得到授权与 abilities 的现值),两者是同一件事的
          事前提醒与事后确认。
        */}
        <Alert>
          <Info className='h-4 w-4' />
          <AlertDescription>{t('qy_ugr_create_empty_warn')}</AlertDescription>
        </Alert>
      </div>
    </QyResponsiveDialog>
  )
}
