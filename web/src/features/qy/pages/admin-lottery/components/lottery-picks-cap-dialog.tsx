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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyLotBatchSeconds } from '../../lottery/lib/seats'
import { qyAdminLotConfigQuery, setQyLotActivityPicksCap } from '../api'
import { QY_LOT_PICKS_DEFAULT_CAP, QY_LOT_PICKS_HARD_CAP } from '../lib/draft'

/**
 * 改一场活动的「一次最多下多少注」。
 *
 * ## 为什么这个动作不受活动状态限制
 *
 * 与换封面同一条理由，而且是本模块**第二个**、也是仅有的另一个这样的字段:
 * 它不进 commit / rules / spec 三个哈希原像的任何一个。它不改变任何人最终能
 * 拿到几张票（那个数由「每人参与上限」说了算），也不改变开奖 —— 它只决定同样
 * 这些票要分几次请求买完。
 *
 * 需要单独一个入口的理由也一样:创建向导只在 `draft` 阶段可用，而"10 注太少了"
 * 这件事恰恰是活动**开起来之后**才发现的。没有这个入口，唯一的补救会变成
 * "取消这一期、全额退款、重开一期"。
 *
 * ## 那句代价必须印在按钮上方
 *
 * 999 注不是一个免费的数字:一次 N 注在服务端是 N 次串行扣费（每一注一张独立
 * 资金单、一条链环、一份可复算回执），满配是一次三十几秒的请求。运营在保存
 * 之前就该看到这个数，而不是等用户来投诉"点了确认之后页面卡住了"。
 */
export function QyLotPicksCapDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  actNo: string
  /** 活动行上的原始值。`0` = 没配过，生效值是后端默认。 */
  maxPicksPerRequest: number
}) {
  const { t } = useTranslation()
  const id = useId()
  const queryClient = useQueryClient()
  const configQuery = useQuery({
    ...qyAdminLotConfigQuery(),
    enabled: props.open,
  })
  const yaml = configQuery.data?.yaml_readonly
  const defaultCap =
    yaml?.max_picks_per_request_default ?? QY_LOT_PICKS_DEFAULT_CAP
  const hardCap = yaml?.max_picks_per_request_hard ?? QY_LOT_PICKS_HARD_CAP

  const [value, setValue] = useState(props.maxPicksPerRequest)
  // 每次打开都从服务端那份重新起草:弹窗关掉不代表改动被应用，留着上一次的
  // 草稿会让人以为"我明明改过了"。
  useEffect(() => {
    if (!props.open) return
    setValue(props.maxPicksPerRequest)
  }, [props.open, props.maxPicksPerRequest])

  const effective = value <= 0 ? defaultCap : Math.min(value, hardCap)
  const overCap = value > hardCap

  const save = useMutation({
    mutationFn: () => setQyLotActivityPicksCap(props.actNo, value),
    onSuccess: async () => {
      toast.success(t('qy_lot_picks_cap_saved'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_picks_cap_change')}
      description={t('qy_lot_picks_cap_change_desc')}
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
            disabled={save.isPending || overCap}
            onClick={() => save.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <div className='space-y-1'>
          <Label htmlFor={id}>{t('qy_lot_rule_f_picks_per_request')}</Label>
          <Input
            id={id}
            inputMode='numeric'
            value={String(value)}
            onChange={(event) => {
              // 只收非负整数。空串归 0 = 清掉这一格，回到后端默认。
              const digits = event.target.value.replaceAll(/\D/g, '')
              setValue(digits === '' ? 0 : Number(digits))
            }}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_rule_f_picks_per_request_hint', {
              fallback: defaultCap,
              max: hardCap,
            })}
          </p>
        </div>

        {overCap && (
          <p className='text-destructive text-xs'>
            {t('qy_lot_v_picks_per_request')}
          </p>
        )}

        {/* 生效值单独说一遍:填 0 与填 10 在行为上一模一样，而输入框里
            只看得到那个 0。 */}
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_picks_cap_effective', { count: effective })}
        </p>

        {effective > 1 && (
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_rule_f_picks_cost_hint', {
              count: effective,
              seconds: qyLotBatchSeconds(
                effective,
                yaml?.entry_batch_ms_per_pick
              ),
            })}
          </p>
        )}

        {/* 这条边界必须写出来，否则运营会以为"原来发布之后什么都能改"。 */}
        <Alert>
          <AlertDescription>
            {t('qy_lot_picks_cap_scope_note')}
          </AlertDescription>
        </Alert>
      </div>
    </QyResponsiveDialog>
  )
}
