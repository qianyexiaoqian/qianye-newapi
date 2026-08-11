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
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyKeys } from '../../../lib/query-keys'
import { qyEnforcementActionKey } from '../../../lib/violation-thresholds'
import { qyOpsErrorMessage } from '../../ops/errors'
import {
  qyApplyViolationThresholdSuggestions,
  qyViolationThresholdSuggestionsQuery,
} from '../api'

/**
 * 「应用建议阈值」。
 *
 * ── 这个弹窗为什么必须存在 ──
 * 出厂时六个内置类型的阈值全是 0，于是「到多少次封号」这件事在界面上等于不存在。
 * 最省事的修法是把一组建议值写进出厂种子 —— 那样升级上来的站点会在部署完成的
 * 那一秒开始按一套**没有人设定过**的线封人。这个弹窗是那条纪律的出口：
 * 建议值只活在代码里，要落库必须有人**看着影响面按下确认**。
 *
 * ── 它必须摆出来的四个事实 ──
 *  1. 每一类建议几次，以及**为什么是这个数**（`why` 原样显示）。没有理由的数字
 *     只能被全盘照抄或全盘不用，而这两种都不是"按自己站点的情况拍板"。
 *  2. 这一改会让**多少人**立刻处在越线状态（去重后的账号数）。
 *  3. 越线之后会被**怎么**处置 —— 这来自兜底策略档，不是本页。只说"会触发处置"
 *     而不说站点当前的兜底动作正是封号，等于把最重的那一半藏起来。
 *  4. 应用**不会当场处置任何人**：写的只是类型表，已越线的账号在各自下一次
 *     违规命中时才被收走。少了这句，"点完一个人都没被封"会被当成没生效。
 *
 * ── 它绝不做的事 ──
 * 覆盖管理员已经手填过的线。后端只补空（`applicable` 为 false 的行在这里是灰的，
 * 并带着为什么被跳过），前端连一个"强制覆盖"的开关都不提供 —— 覆盖一条手填的
 * 线是一次静默收紧，而收紧方向的错误会直接把人封掉。
 */
export function QySuggestedThresholdsDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const query = useQuery({
    ...qyViolationThresholdSuggestionsQuery(),
    enabled: props.open,
  })
  const data = query.data

  const mutation = useMutation({
    // confirm 恒为 true：这个弹窗本身就是那次确认交互。后端的 409 兜的是
    // 绕过界面直接调接口的那条路，不是这里的正常路径。
    mutationFn: () => qyApplyViolationThresholdSuggestions(true),
    onSuccess: async (res) => {
      toast.success(
        t('qy_vcat_sug_applied', {
          count: res.applied_count,
          users: res.affected_users,
        })
      )
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: qyKeys.adminViolationCategories(),
        }),
        queryClient.invalidateQueries({
          queryKey: qyKeys.adminViolationCategorySuggestions(),
        }),
      ])
      props.onOpenChange(false)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const applicable = data?.applicable_count ?? 0

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_vcat_sug_title')}
      description={t('qy_vcat_sug_desc')}
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
            disabled={applicable === 0 || mutation.isPending || query.isLoading}
            onClick={() => mutation.mutate()}
          >
            {t('qy_vcat_sug_apply', { count: applicable })}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        {/* 影响面 + 处置动作 + "不会当场处置任何人"，三句摆在最上面。
            管理员只会认真读第一屏，而这三句正是"按下去会发生什么"的全部。 */}
        <div className='bg-muted/40 space-y-1 rounded-md p-3 text-xs'>
          <p>
            {t('qy_vcat_sug_impact', {
              count: applicable,
              users: data?.affected_users ?? 0,
            })}
          </p>
          <p>
            {t('qy_vcat_sug_action_note', {
              action: t(qyEnforcementActionKey(data?.account_action)),
            })}
          </p>
          <p className='text-muted-foreground'>
            {t('qy_vcat_sug_not_immediate')}
          </p>
          {data?.capped === true && (
            <p className='text-muted-foreground'>{t('qy_vcat_sug_capped')}</p>
          )}
        </div>

        <ul className='space-y-2'>
          {(data?.items ?? []).map((item) => (
            <li
              key={item.id}
              className={
                item.applicable
                  ? 'space-y-1 rounded-md border p-3'
                  : 'bg-muted/30 text-muted-foreground space-y-1 rounded-md border border-dashed p-3'
              }
            >
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <span className='text-sm font-medium'>{item.name}</span>
                <span className='flex items-center gap-2 text-xs'>
                  {item.applicable ? (
                    <Badge variant='outline'>
                      {t('qy_vcat_sug_change', {
                        threshold: item.suggested_threshold,
                        hours: item.suggested_window_hours,
                      })}
                    </Badge>
                  ) : (
                    <Badge variant='outline'>{t('qy_vcat_sug_skipped')}</Badge>
                  )}
                </span>
              </div>
              {/* applicable=false 时后端必给 skip_reason —— 一个只灰不给理由的行
                  会被当成 bug，而管理员的下一步就是绕过界面直接改库。 */}
              <p className='text-xs'>
                {item.applicable ? item.why : item.skip_reason}
              </p>
              {item.applicable && (
                <p className='text-muted-foreground text-xs'>
                  {t('qy_vcat_sug_row_impact', {
                    count: item.impact.matched,
                  })}
                </p>
              )}
            </li>
          ))}
        </ul>
      </div>
    </QyResponsiveDialog>
  )
}
