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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import type { QyApiAddress, QyApiAddressUpsert } from '../types'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 为空表示新建。 */
  current: QyApiAddress | null
  isPending: boolean
  onSubmit: (body: QyApiAddressUpsert) => void
}

/**
 * 新建 / 编辑一条 API 地址。
 *
 * ── 为什么校验只做「必填 + 空白」这一层 ──
 * URL 的合法性判据（scheme 白名单、不许带凭据、不许带查询串、尾斜杠归一化）
 * 全部在后端 `normalizeURL` 里，前端**刻意不复述**：复述一遍就是同一份规则的
 * 第二份拷贝，两份迟早漂移，而漂移的方向必然是前端更松（后端加了新判据前端
 * 不会跟）或更严（前端挡住了后端本来接受的写法，用户完全无从申诉）。
 * 这里只挡「按钮点了什么都没发生」这一种体验问题，其余交给后端的 code → 文案。
 */
export function QyApiAddressFormDialog(props: Props) {
  const { t } = useTranslation()
  const formId = useId()
  const [name, setName] = useState('')
  const [remark, setRemark] = useState('')
  const [url, setUrl] = useState('')
  const [enabled, setEnabled] = useState(true)

  const current = props.current
  // 每次打开都从入参重置：留着上一次的草稿会让「编辑 A → 关掉 → 新建」带出
  // A 的内容，而那看起来完全像是新建表单的默认值。
  useEffect(() => {
    if (!props.open) return
    setName(current?.name ?? '')
    setRemark(current?.remark ?? '')
    setUrl(current?.url ?? '')
    setEnabled(current?.enabled ?? true)
  }, [props.open, current])

  const canSubmit = name.trim() !== '' && url.trim() !== '' && !props.isPending

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={current == null ? t('qy_aa_create') : t('qy_aa_edit')}
      description={t('qy_aa_form_desc')}
      contentClassName='sm:max-w-lg'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('qy_aa_cancel')}
          </Button>
          <Button form={formId} type='submit' disabled={!canSubmit}>
            {t('qy_aa_save')}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className='space-y-4'
        onSubmit={(event) => {
          event.preventDefault()
          if (!canSubmit) return
          props.onSubmit({
            name: name.trim(),
            remark: remark.trim(),
            url: url.trim(),
            enabled,
          })
        }}
      >
        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-name`}>{t('qy_aa_field_name')}</Label>
          <Input
            id={`${formId}-name`}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t('qy_aa_field_name_ph')}
          />
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-url`}>{t('qy_aa_field_url')}</Label>
          <Input
            id={`${formId}-url`}
            value={url}
            onChange={(event) => setUrl(event.target.value)}
            placeholder='https://api.example.com'
            autoComplete='off'
            spellCheck={false}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_aa_field_url_hint')}
          </p>
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-remark`}>{t('qy_aa_field_remark')}</Label>
          <Input
            id={`${formId}-remark`}
            value={remark}
            onChange={(event) => setRemark(event.target.value)}
            placeholder={t('qy_aa_field_remark_ph')}
          />
        </div>

        <div className='flex items-center justify-between gap-3 rounded-lg border p-3'>
          <div className='space-y-0.5'>
            <Label htmlFor={`${formId}-enabled`}>
              {t('qy_aa_field_enabled')}
            </Label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_aa_field_enabled_hint')}
            </p>
          </div>
          <Switch
            id={`${formId}-enabled`}
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>
      </form>
    </QyResponsiveDialog>
  )
}
