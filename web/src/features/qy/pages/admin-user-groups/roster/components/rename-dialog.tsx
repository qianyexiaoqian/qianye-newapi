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
import { AlertTriangle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../../../../components/qy-responsive-dialog'

/**
 * 改名。
 *
 * ── 它比看起来重 ──
 *
 * 改名要横跨两个数据库改六处:`users.group`、已售订阅的三列快照、套餐的
 * 升降级分组、`options.GroupGroupRatio` 的外层键、`TopupGroupRatio` 的键,
 * 以及各模块自己声明的范围 / 授权 / 费率 / 划转规则。所以弹窗里必须把
 * **会跟着走的人数**摆出来 —— 它同时是请求体里的一致性闸门:弹窗打开时是
 * 3 个人、按下按钮时已经是 300 个,后端返回 409 而不是照着新数字改下去。
 *
 * ── 为什么没有「顺便合并到另一个分组」 ──
 *
 * 那是删除 + 迁移,不是改名。混在一起的表现是运营以为自己只是换了个字,
 * 而实际上把两档人的可用范围与价格合成了一份。
 */
export function QyUgrRenameDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /**
   * 被改名的那一档：只要名字与在册人数。
   *
   * **刻意不收整个 `QyUgrRow`**：唯一的调用方是合并后那张表的编辑弹窗，它手上
   * 是合并行（观测 ∪ 登记 ∪ 倍率键），拿不出登记表那十几列。收窄到真正用到的
   * 两个字段之后，调用方不必为了满足类型去伪造 `default_mode` 之类的值 ——
   * 伪造出来的那些值会一路传到别的地方，而没有任何人知道它们是编的。
   */
  row: { name: string; users: number } | null
  newName: string
  onNewNameChange: (name: string) => void
  isSaving: boolean
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  const row = props.row
  const unchanged = row != null && props.newName.trim() === row.name

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_ugr_rename_title', { name: row?.name ?? '' })}
      description={t('qy_ugr_rename_desc')}
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            disabled={
              props.isSaving || props.newName.trim() === '' || unchanged
            }
            onClick={props.onConfirm}
          >
            {props.isSaving ? t('Saving...') : t('Save')}
          </Button>
        </>
      }
    >
      <div className='space-y-3 text-sm'>
        <div className='space-y-1.5'>
          <Label htmlFor='qy-ugr-rename-to'>{t('qy_ugr_field_new_name')}</Label>
          <Input
            id='qy-ugr-rename-to'
            value={props.newName}
            onChange={(event) => props.onNewNameChange(event.target.value)}
          />
        </div>

        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertDescription>
            {t('qy_ugr_rename_carries', { users: row?.users ?? 0 })}
          </AlertDescription>
        </Alert>
      </div>
    </QyResponsiveDialog>
  )
}
