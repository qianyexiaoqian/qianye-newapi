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
import { CircleAlert, CircleMinus } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { QyResponsiveDialog } from '../components/qy-responsive-dialog'
import {
  QY_CHANNEL_BATCH_ITEM_I18N,
  type QyChannelBatchItem,
  type QyChannelBatchResult,
} from './api'

/**
 * 批次执行报告。
 *
 * # 为什么这一屏是必须的，而不是一个 toast
 *
 * 需求原话：「选了 20 个渠道，第 7 个删失败。⋯⋯要逐条返回结果并在界面上把
 * 失败的那几条单独列出来。」toast 装不下 20 行，也留不住 —— 而管理员接下来
 * 要做的事（去看那 3 条为什么失败、重试它们）需要那份名单一直在屏幕上。
 *
 * # 只列失败与跳过，不列成功
 *
 * 成功的那些不需要任何后续动作，逐条列出来只会让真正要看的 3 行淹在 17 行绿色里。
 * 顶部的三个计数已经把全貌说清楚了；跳过的那档列出来是因为它常常意味着
 * "你选错范围了"（比如对一批已启用的渠道点了启用），那是一条有用的信息。
 */
export function QyChannelBatchResultDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  result: QyChannelBatchResult | null
}) {
  const { t } = useTranslation()
  const result = props.result

  const noteworthy = result?.items.filter((item) => item.outcome !== 'ok') ?? []

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={props.title}
      description={t('qy_chops_result_desc')}
      contentClassName='sm:max-w-lg'
      footer={
        <Button onClick={() => props.onOpenChange(false)}>
          {t('qy_chops_result_close')}
        </Button>
      }
    >
      {result == null ? null : (
        <div className='space-y-4'>
          <div className='grid grid-cols-3 gap-2'>
            <CountTile
              tone='ok'
              label={t('qy_chops_count_succeeded')}
              value={result.succeeded}
            />
            <CountTile
              tone='skipped'
              label={t('qy_chops_count_skipped')}
              value={result.skipped}
            />
            <CountTile
              tone='failed'
              label={t('qy_chops_count_failed')}
              value={result.failed}
            />
          </div>

          {noteworthy.length === 0 ? (
            <p className='text-muted-foreground text-sm'>
              {t('qy_chops_result_all_ok', { count: result.succeeded })}
            </p>
          ) : (
            <ul className='divide-border divide-y rounded-md border'>
              {noteworthy.map((item) => (
                <ResultRow key={item.id} item={item} />
              ))}
            </ul>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}

/**
 * 一格计数。
 *
 * 三档各自上色而不是只把失败标红：`skipped` 用中性色是刻意的 —— 它既不是
 * 成绩也不是问题，染成黄色会让"本来就已经启用"看起来像一次警告。
 */
function CountTile(props: {
  label: string
  tone: 'failed' | 'ok' | 'skipped'
  value: number
}) {
  const toneClass = {
    ok: 'text-emerald-600 dark:text-emerald-400',
    skipped: 'text-muted-foreground',
    failed: 'text-destructive',
  }[props.tone]

  return (
    <div className='rounded-md border p-3 text-center'>
      <div className={`text-2xl font-semibold tabular-nums ${toneClass}`}>
        {props.value}
      </div>
      <div className='text-muted-foreground mt-1 text-xs'>{props.label}</div>
    </div>
  )
}

/** 一行明细：渠道名 + id + 原因。 */
function ResultRow(props: { item: QyChannelBatchItem }) {
  const { t } = useTranslation()
  const item = props.item
  const failed = item.outcome === 'failed'
  const known =
    item.code != null ? QY_CHANNEL_BATCH_ITEM_I18N[item.code] : undefined
  // 兜底文案写成两次字面量 t('…') 而不是 t(三元):i18n-key-coverage 那条守卫
  // 只认字面量,写成三元的话这两个键会被当成"零引用",下一次清理就会把它们删掉,
  // 而删掉的表现是界面上出现一行裸键。
  const fallback = failed
    ? t('qy_chops_item_failed')
    : t('qy_chops_item_no_change')
  // code 白名单 → 后端中文兜底 → 泛化文案。三级回落让新增后端 code 不会留白行。
  const reason = known != null ? t(known) : (item.detail ?? fallback)

  return (
    <li className='flex items-start gap-2 p-2.5 text-sm'>
      {failed ? (
        <CircleAlert
          className='text-destructive mt-0.5 size-4 shrink-0'
          aria-hidden='true'
        />
      ) : (
        <CircleMinus
          className='text-muted-foreground mt-0.5 size-4 shrink-0'
          aria-hidden='true'
        />
      )}
      <div className='min-w-0 flex-1'>
        {/* 名字必须在第一位：只给 id 的话，管理员得回列表页逐个对照才知道
            挂掉的是哪个渠道，而这一屏正是他做决定的地方。 */}
        <div className='truncate font-medium'>
          {item.name === '' ? `#${item.id}` : item.name}
          <span className='text-muted-foreground ml-1 font-normal'>
            #{item.id}
          </span>
        </div>
        <div className='text-muted-foreground'>{reason}</div>
      </div>
    </li>
  )
}
