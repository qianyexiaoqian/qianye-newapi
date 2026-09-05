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
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyRuneLength } from '../../lib/constants'
import { updateQyLotBasics } from '../api'
import { QY_LOT_INTRO_MAX_RUNES, QY_LOT_TITLE_MAX_RUNES } from '../lib/bounds'
import type { QyLotAdminActivity } from '../types'

/**
 * 改一场**已发布**活动的标题与说明。
 *
 * ## 为什么发布之后还能改
 *
 * 标题与说明不进 commit / rules / spec 三个哈希原像的任何一个：改它们不动结果推导，
 * 也不动"奖档是什么、时刻是什么、谁能参与"这些承诺。而"发布之后发现标题里有个错
 * 别字"是真实会发生的事，此前唯一的补救是取消整场、全额退款、重开一场。
 *
 * 能改到封盘为止（草稿 / 进行中 / 已封盘）；结算或结束之后 409
 * `qy_lot_basics_locked` —— 那时名字已经随证据链公示过了。每次改动写一条
 * `basics_changed` 事件（前后快照都在），参与者在活动页的事件流里看得到。
 *
 * **草稿不走这里**：草稿有整份可改的编辑向导，这个弹窗只给发布之后改错别字用。
 */
export function QyLotBasicsDialog(props: {
  activity: QyLotAdminActivity
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const titleId = useId()
  const introId = useId()
  // 老行 / 列表快照可能不带 intro：按空串起草，别让一个缺席的字段炸掉整页。
  const currentTitle = props.activity.title || ''
  const currentIntro = props.activity.intro || ''
  const [title, setTitle] = useState(currentTitle)
  const [intro, setIntro] = useState(currentIntro)

  // 每次打开都从服务端那份重新起草：弹窗关掉不代表改动被应用。
  useEffect(() => {
    if (!props.open) return
    setTitle(currentTitle)
    setIntro(currentIntro)
  }, [props.open, currentTitle, currentIntro])

  const mutation = useMutation({
    mutationFn: () =>
      updateQyLotBasics(props.activity.act_no, {
        title: title.trim(),
        intro,
      }),
    onSuccess: async () => {
      toast.success(t('qy_lot_basics_done'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: async (error) => {
      toast.error(qyErrorMessage(error, t))
      // 已结算 / 已结束：这件事从此做不到，弹窗留着只会让人改一遍再撞一次。
      if (isQyError(error) && error.code === 'qy_lot_basics_locked') {
        props.onOpenChange(false)
        await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      }
    },
  })

  const titleLength = qyRuneLength(title.trim())
  const introLength = qyRuneLength(intro)
  const titleInvalid = titleLength === 0 || titleLength > QY_LOT_TITLE_MAX_RUNES
  const introInvalid = introLength > QY_LOT_INTRO_MAX_RUNES
  const unchanged = title.trim() === currentTitle && intro === currentIntro
  const canSubmit =
    !mutation.isPending && !titleInvalid && !introInvalid && !unchanged

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_basics_title')}
      description={props.activity.act_no}
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
            disabled={!canSubmit}
            onClick={() => mutation.mutate()}
          >
            {t('qy_lot_basics_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <Alert>
          <AlertDescription>{t('qy_lot_basics_desc')}</AlertDescription>
        </Alert>
        <div className='space-y-1.5'>
          <Label htmlFor={titleId}>{t('qy_lot_title_field')}</Label>
          <Input
            id={titleId}
            value={title}
            autoComplete='off'
            aria-invalid={titleInvalid}
            disabled={mutation.isPending}
            onChange={(event) => setTitle(event.target.value)}
          />
          <p className='text-muted-foreground text-end text-xs tabular-nums'>
            {t('qy_common_rune_counter', {
              used: titleLength,
              max: QY_LOT_TITLE_MAX_RUNES,
            })}
          </p>
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor={introId}>{t('qy_lot_intro_field')}</Label>
          <Textarea
            id={introId}
            rows={6}
            value={intro}
            aria-invalid={introInvalid}
            disabled={mutation.isPending}
            onChange={(event) => setIntro(event.target.value)}
          />
          <p className='text-muted-foreground text-end text-xs tabular-nums'>
            {t('qy_common_rune_counter', {
              used: introLength,
              max: QY_LOT_INTRO_MAX_RUNES,
            })}
          </p>
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_intro_plain_hint')}
          </p>
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
