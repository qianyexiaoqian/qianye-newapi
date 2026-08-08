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
import { Eye, RotateCcw, Save } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import type { QyGmDiffCounts } from '../lib/draft'

type QyGmDiffBarProps = {
  counts: QyGmDiffCounts
  /** 草稿里有解析不通过的倍率格。 */
  invalidCount: number
  /**
   * 草稿里有**任何**被动过的格子（含只填了非法值的）。
   *
   * 「丢弃草稿」的禁用条件必须看它而不是看动作数：非法输入不产出动作，
   * 于是一格误粘贴的 `1e3` 会让保存被拦（正确）而丢弃按钮同时变灰 ——
   * 唯一的退路是自己去几十个格子里找回那一格。
   */
  hasDraft: boolean
  /** 含撤销或改价，且尚未对**当前这一份**草稿预览过。 */
  needsPreview: boolean
  isPreviewing: boolean
  isSaving: boolean
  onPreview: () => void
  onSave: () => void
  onReset: () => void
}

/**
 * 顶部常驻的未保存改动条。
 *
 * ── 为什么撤销要单独计数并标红 ──
 * 「未保存改动 12 项」这个数字本身不构成决策依据：12 项改价与 12 项撤销的
 * 后果完全不同 —— 后者会让一批正在跑的令牌当场变成 403。把三类动作合成一个
 * 数字，运营在按保存前唯一能看到的信息就恰好抹掉了最危险的那一类。
 *
 * ── 保存闸门 ──
 * 含撤销**或改价**时，看过预览之前保存键是禁用的。
 *
 * 改价也闸的理由：撤销让一批令牌当场 403，改价则在保存成功那一秒起就按新价
 * 扣钱，而本轮把倍率从「偶尔配的例外」提升成了主要机制。配合整列批量与整行
 * 复制，一次点击能改掉一整列的倍率 —— 只拦撤销的闸门会让这种改动完全没有
 * 影响面可看。放开（grant）不闸：它既不打断请求也不改价。
 */
export function QyGmDiffBar(props: QyGmDiffBarProps) {
  const { t } = useTranslation()
  const blocked = props.invalidCount > 0 || props.needsPreview

  return (
    <div
      className={cn(
        'bg-background/95 sticky top-0 z-20 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border px-3 py-2 backdrop-blur',
        props.counts.revoke > 0 && 'border-destructive/50'
      )}
    >
      <span className='text-sm font-medium'>
        {t('qy_group_matrix_diff_bar', { count: props.counts.total })}
      </span>

      <div className='flex flex-wrap items-center gap-x-3 gap-y-1 text-xs tabular-nums'>
        <span className='text-success'>
          {t('qy_group_matrix_diff_grant', { count: props.counts.grant })}
        </span>
        <span
          className={cn(
            props.counts.revoke > 0
              ? 'text-destructive font-semibold'
              : 'text-muted-foreground'
          )}
        >
          {t('qy_group_matrix_diff_revoke', { count: props.counts.revoke })}
        </span>
        <span className='text-muted-foreground'>
          {t('qy_group_matrix_diff_reprice', { count: props.counts.reprice })}
        </span>
        {/* 备注单独一栏：它不过影响面闸门（不动钱、不断流量），混进「改价」
            那个数字会让运营以为自己刚刚改了价。 */}
        <span className='text-muted-foreground'>
          {t('qy_gm_diff_note', { count: props.counts.note })}
        </span>
      </div>

      {props.invalidCount > 0 && (
        <span className='text-destructive text-xs'>
          {t('qy_group_matrix_invalid_cells', { count: props.invalidCount })}
        </span>
      )}

      <div className='ms-auto flex items-center gap-2'>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          onClick={props.onReset}
          disabled={!props.hasDraft}
        >
          <RotateCcw aria-hidden='true' />
          {t('qy_group_matrix_reset')}
        </Button>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={props.onPreview}
          disabled={props.isPreviewing || props.invalidCount > 0}
        >
          <Eye aria-hidden='true' />
          {t('qy_group_matrix_preview_btn')}
        </Button>
        <Button
          type='button'
          size='sm'
          onClick={props.onSave}
          disabled={props.counts.total === 0 || props.isSaving || blocked}
          title={
            props.needsPreview
              ? t('qy_group_matrix_save_needs_preview')
              : undefined
          }
        >
          <Save aria-hidden='true' />
          {t('qy_group_matrix_save')}
        </Button>
      </div>

      {props.needsPreview && (
        <p className='text-warning w-full text-xs'>
          {t('qy_group_matrix_save_needs_preview')}
        </p>
      )}
    </div>
  )
}
