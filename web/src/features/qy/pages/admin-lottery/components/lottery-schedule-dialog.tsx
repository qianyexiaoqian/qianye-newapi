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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyError, qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { updateQyLotSchedule } from '../api'
import { qyLotFromLocalInput, qyLotToLocalInput } from '../lib/datetime'
import type { QyLotAdminActivity } from '../types'

/**
 * 这两个 code 的意思都是"这件事从此做不到"，不是"刷新再试"：批次玩法的时刻进
 * 承诺原像；转盘一旦封盘（到点 / 提前结束 / 库存耗尽）名单已冻结、只等揭示。
 * 弹窗留着只会让运营改一遍再撞一次 —— 直接关掉并刷新详情，让页面自己说出
 * 现在的状态。
 */
const SCHEDULE_LOCKED_CODES = new Set([
  'qy_lot_wheel_schedule_locked',
  'qy_lot_schedule_not_wheel',
])

/**
 * 改一场**已发布转盘**的排期（开始 / 结束）。
 *
 * ## 为什么转盘可以在发布之后改时刻，而别的玩法不行
 *
 * 批次玩法的结果由封盘那一刻冻结的名单决定 —— 谁能挑封盘时刻，谁就能挑名单，
 * 所以它们的四个时刻在发布那一刻进承诺哈希，之后只有定时任务按时间触发。
 * 转盘没有这一步：每一转当场开出（票面 = HMAC(seed, act_no ‖ seq ‖ client_seed)，
 * 里面没有时刻），排期改成什么都不会改变任何一张已经开出的票，也不会改变下一张
 * 票的推导。它只决定"什么时候收转"，像抽卡卡池的上下架时间。项目方原话
 * （2026-09-04）：「转盘应像游戏抽卡卡池一样：开始时间、结束时间就可以了。」
 *
 * 表单只有两格。`draw_at` 不出现：后端按「结束 + 强制间隔」重新派生。「提前结束」
 * 不在这里 —— 那是既有的取消（提前封盘），这里刻意不接受早于现在的结束时间，
 * 免得同一件事有两条路。
 */
export function QyLotScheduleDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  activity: QyLotAdminActivity
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [openAt, setOpenAt] = useState(props.activity.open_at)
  const [closeAt, setCloseAt] = useState(props.activity.close_at)
  // 每次打开都从服务端那份重新起草：弹窗关掉不代表改动被应用，留着上一次的
  // 草稿会让人以为"我明明改过了"。
  useEffect(() => {
    if (!props.open) return
    setOpenAt(props.activity.open_at)
    setCloseAt(props.activity.close_at)
  }, [props.open, props.activity.open_at, props.activity.close_at])

  // 与后端同一口径的三条（api_admin_schedule.go），在本地先说清楚，而不是让
  // 运营点保存吃一句 400。地平线那条（366 天）交给后端 —— 它不是日常会撞到的。
  const now = Math.floor(Date.now() / 1000)
  let problem: string | null = null
  if (openAt <= 0 || closeAt <= 0) problem = 'qy_lot_schedule_v_required'
  else if (closeAt <= openAt) problem = 'qy_lot_schedule_v_order'
  else if (closeAt <= now) problem = 'qy_lot_schedule_v_past'
  const unchanged =
    openAt === props.activity.open_at && closeAt === props.activity.close_at

  const save = useMutation({
    mutationFn: () =>
      updateQyLotSchedule(props.activity.act_no, {
        open_at: openAt,
        close_at: closeAt,
      }),
    onSuccess: async () => {
      toast.success(t('qy_lot_schedule_saved'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: async (error) => {
      toast.error(qyErrorMessage(error, t))
      if (
        error instanceof QyError &&
        SCHEDULE_LOCKED_CODES.has(error.code ?? '')
      ) {
        props.onOpenChange(false)
        await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      }
    },
  })

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_schedule_change')}
      description={t('qy_lot_schedule_change_desc')}
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={save.isPending || problem != null || unchanged}
            onClick={() => save.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <div className='grid gap-3 sm:grid-cols-2'>
          <ScheduleTimeField
            label={t('qy_lot_open_at')}
            value={openAt}
            onChange={setOpenAt}
          />
          <ScheduleTimeField
            label={t('qy_lot_wheel_end_at')}
            hint={t('qy_lot_wheel_end_at_hint')}
            value={closeAt}
            onChange={setCloseAt}
          />
        </div>

        {problem != null && (
          <p className='text-destructive text-xs'>{t(problem)}</p>
        )}

        {/* 这条边界必须写出来，否则运营会以为"原来发布之后什么都能改"。 */}
        <Alert>
          <AlertDescription>{t('qy_lot_schedule_scope_note')}</AlertDescription>
        </Alert>
      </div>
    </QyResponsiveDialog>
  )
}

/**
 * 与建活动向导里的时刻输入格同一个形状（`datetime-local` ↔ unix 秒，转换只有
 * `lib/datetime.ts` 一处）。**不从向导文件 import**：向导是一份四步、几十个字段的
 * 表单，这个弹窗只要两格；共用一个导出会让两边的改动互相牵连。
 */
function ScheduleTimeField(props: {
  label: string
  hint?: string
  value: number
  onChange: (value: number) => void
}) {
  const id = useId()
  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{props.label}</Label>
      <Input
        id={id}
        type='datetime-local'
        value={qyLotToLocalInput(props.value)}
        onChange={(event) =>
          props.onChange(qyLotFromLocalInput(event.target.value))
        }
      />
      {props.hint != null && (
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      )}
    </div>
  )
}
