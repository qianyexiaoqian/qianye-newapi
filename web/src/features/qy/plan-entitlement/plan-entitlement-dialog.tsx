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
import { Boxes, TriangleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../components/qy-responsive-dialog'
import { qyErrorMessage } from '../lib/api'
import { qyKeys } from '../lib/query-keys'
import { qyPlanEntitlementQuery, qySavePlanEntitlement } from './api'
import type { QyPlanBalanceScope } from './types'

type QyPlanEntitlementDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  planId: number
  planTitle: string
}

/**
 * 套餐的「解锁模型分组 + 余额使用范围」。
 *
 * ── 这两件事为什么在同一个弹窗里 ──
 *
 * 它们是同一个决定的两半，而且第二半只有在第一半非空时才有意义：
 *
 *   解锁模型分组    买了这个套餐的人能用哪些模型分组（**不绑定用户分组**）
 *   余额使用范围    套餐自带的那笔额度能不能花在别的模型分组上
 *
 * 拆成两个入口，运营会在"零绑定"的套餐上把余额设成「仅限」，而那是一份**任何
 * 请求都用不上的死钱** —— 用户看得见余额、花不掉、到期作废。所以这里由保存按钮
 * 直接拦住这个组合，而不是等用户来投诉。
 *
 * ── 为什么不放进上游那个套餐编辑抽屉 ──
 *
 * 那个抽屉编辑的是套餐本身（价格、周期、额度），数据落主库 `subscription_plans`；
 * 这两项落扩展库的两张表，是两次独立的写入、两条独立的审计。混进同一个表单意味着
 * 一次提交要横跨两个库，而它们不原子 —— 部分失败时运营会看到一个从未存在过的
 * 成功画面。分开之后，每一次保存的成败都是自明的。
 */
export function QyPlanEntitlementDialog(props: QyPlanEntitlementDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const query = useQuery(qyPlanEntitlementQuery(props.planId, props.open))
  const data = query.data

  const [selected, setSelected] = useState<string[]>([])
  const [scope, setScope] = useState<QyPlanBalanceScope>('universal')

  // 每次打开都从服务端状态重置。残留上一次编辑的**另一个套餐**的勾选，
  // 会让运营在完全不知情的情况下把 A 套餐的解锁清单按到 B 套餐头上，
  // 而这一步直接决定一笔钱从哪个池子里扣。
  useEffect(() => {
    if (!props.open || data == null) return
    setSelected(data.unlock_groups)
    setScope(data.balance_scope)
  }, [props.open, data])

  const saveMutation = useMutation({
    mutationFn: () =>
      qySavePlanEntitlement(props.planId, {
        unlock_groups: selected,
        balance_scope: scope,
        note: data?.note ?? '',
      }),
    onSuccess: async () => {
      toast.success(t('qy_plan_entitlement_saved'))
      // 两个键都要失效。矩阵页的格子带「经套餐 P 可达」，它的数据源是这份绑定：
      // 只失效自己那一个键的话，运营切回矩阵页会看到一格仍然画着"不可达"，
      // 然后在看不见影响面的情况下去调那一列的兜底倍率 —— 正是这一版反复
      // 强调的「可达性认知漂移」。
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: qyKeys.adminPlanEntitlement(props.planId),
        }),
        queryClient.invalidateQueries({ queryKey: qyKeys.adminGroupMatrix() }),
      ])
      props.onOpenChange(false)
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  // 写入侧后端也会 400，但按钮就该在这里禁用并说明原因 ——
  // 让人按下去再吃一个错误是更差的交代。
  // 「仅限」+ 零**有效**绑定 = 一份任何请求都用不上的额度池。
  // 判定用的是活着的绑定数：已从倍率表消失的名字在快照里已被剔除，绑着等于没绑，
  // 把它算进来会让一个实际上花不掉的余额池通过校验。
  const liveSelected = selected.filter((group) =>
    (data?.model_group_candidates ?? []).includes(group)
  )
  const restrictedWithoutBinding =
    scope === 'restricted' && data != null && liveSelected.length === 0

  /*
    已经绑定、但已经不在候选清单里的模型分组（运营把它从「分组倍率」里删了）。

    它们必须**渲染出来并且可以取消勾选**。只画一条红色横幅、而复选框只列候选的
    话，这个名字在界面上不可见也取消不掉，而每一次保存都会把它原样提交回去 ——
    在加入存量豁免之前，那意味着这个套餐从此永远存不下去（包括横幅里那个
    「改回通用」按钮，它只改 scope、不动清单）。现在后端放行存量，但运营仍然
    需要一个能把它摘掉的地方，否则这份死绑定只能靠改库清理。
  */
  const orphanSelected = selected.filter(
    (group) => !(data?.model_group_candidates ?? []).includes(group)
  )

  const toggle = (group: string) => {
    setSelected((current) =>
      current.includes(group)
        ? current.filter((item) => item !== group)
        : [...current, group]
    )
  }

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_plan_entitlement_title', { plan: props.planTitle })}
      description={t('qy_plan_entitlement_desc')}
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
            disabled={
              query.isPending ||
              saveMutation.isPending ||
              restrictedWithoutBinding
            }
            onClick={() => saveMutation.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      {data != null && (
        <div className='space-y-4'>
          {/*
            模块被拉闸了。读写两条接口都不看 plan_entitlement.enabled，所以这里
            照样能配、能保存成功、审计也记成功 —— 而已购用户一个解锁分组都拿不到。
            界面不说的话，运营会对着一个已经停用的功能反复调参。
          */}
          {!data.enabled && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertTitle>{t('qy_plan_entitlement_disabled_alert')}</AlertTitle>
              <AlertDescription>
                {t('qy_plan_entitlement_disabled_hint')}
              </AlertDescription>
            </Alert>
          )}

          {/*
            余额范围的判定住在扩展里、执行在上游扣费路径上。握手没完成时它只是一份
            存下来的配置：restricted 的余额照样会被任意模型分组花掉。**这一条必须
            说出来** —— 否则管理端显示「已经限制住了」，而钱照样从那张套餐里扣。
          */}
          {!data.balance_scope_enforced &&
            data.balance_scope === 'restricted' && (
              <Alert variant='destructive'>
                <TriangleAlert />
                <AlertTitle>
                  {t('qy_plan_balance_scope_not_enforced')}
                </AlertTitle>
                <AlertDescription>
                  {t('qy_plan_balance_scope_not_enforced_hint')}
                </AlertDescription>
              </Alert>
            )}

          {/*
            绑定的模型分组已经从分组倍率表里消失了。

            这是"运营删了一个仍被引用的模型分组"唯一能被发现的地方 —— 上游删除时
            不做任何引用检查。restricted 套餐撞上它时，那笔余额变成**死钱**：
            用户看得见、花不掉、到期作废。所以给一个「改回通用」的即时补救，
            那是纯配置变更、无数据迁移、立即生效。
          */}
          {data.missing_groups.length > 0 && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertTitle>{t('qy_plan_bound_group_missing_alert')}</AlertTitle>
              <AlertDescription>
                <span className='block'>{data.missing_groups.join('、')}</span>
                {scope === 'restricted' && (
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    className='mt-1'
                    onClick={() => setScope('universal')}
                  >
                    {t('qy_plan_bound_group_reset_to_universal')}
                  </Button>
                )}
              </AlertDescription>
            </Alert>
          )}

          {/*
            影响面分母。改这一段的后果**立刻**作用在这些人身上（解锁是并集，
            收窄则让他们下一次请求就选不到那个模型分组），所以数字必须在按保存
            之前就摆在眼前，而不是藏在别的页面里。
          */}
          <p className='text-muted-foreground text-xs tabular-nums'>
            {t('qy_plan_entitlement_impact', {
              count: data.active_subscriptions,
            })}
          </p>

          <div className='space-y-2'>
            <Label>{t('qy_plan_unlock_groups_label')}</Label>
            {/*
              候选**只从模型分组轴取**。两个轴共用同一个字符串命名空间，
              把用户分组混进这份候选，运营会解锁一个根本不是模型分组的名字，
              而它在计价时会撞上上游 GetGroupRatio 的 fail-open（静默按 1.0 扣）。
            */}
            {data.model_group_candidates.length === 0 ? (
              <p className='text-muted-foreground text-xs'>
                {t('qy_plan_unlock_groups_empty')}
              </p>
            ) : (
              <div className='grid max-h-56 gap-1 overflow-auto rounded-md border p-2 sm:grid-cols-2'>
                {data.model_group_candidates.map((group) => (
                  <label
                    key={group}
                    className='hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-xs'
                  >
                    <Checkbox
                      checked={selected.includes(group)}
                      onCheckedChange={() => toggle(group)}
                    />
                    <Boxes
                      aria-hidden='true'
                      className='text-muted-foreground size-3 shrink-0'
                    />
                    <span className='truncate'>{group}</span>
                  </label>
                ))}
              </div>
            )}
            {/*
              已绑定但已经不在候选里的名字。单列一块、默认勾着、可以取消 ——
              不渲染它们的话，这个名字在界面上不可见也摘不掉，运营唯一的出路是
              改库。它们不参与 restricted 的"至少绑一个"判定，因为它们在快照里
              已经被剔除，绑着等于没绑。
            */}
            {orphanSelected.length > 0 && (
              <div className='border-destructive/50 grid gap-1 rounded-md border border-dashed p-2 sm:grid-cols-2'>
                {orphanSelected.map((group) => (
                  <label
                    key={group}
                    className='hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-xs'
                  >
                    <Checkbox checked onCheckedChange={() => toggle(group)} />
                    <TriangleAlert
                      aria-hidden='true'
                      className='text-destructive size-3 shrink-0'
                    />
                    <span className='truncate line-through'>{group}</span>
                  </label>
                ))}
              </div>
            )}
            <p className='text-muted-foreground text-xs'>
              {t('qy_plan_unlock_groups_hint')}
            </p>
          </div>

          <div className='space-y-2'>
            <Label>{t('qy_plan_balance_scope_label')}</Label>
            <div className='grid gap-2'>
              <QyPlanScopeOption
                active={scope === 'universal'}
                title={t('qy_plan_balance_scope_universal')}
                desc={t('qy_plan_balance_scope_universal_desc')}
                onSelect={() => setScope('universal')}
              />
              <QyPlanScopeOption
                active={scope === 'restricted'}
                title={t('qy_plan_balance_scope_restricted')}
                desc={t('qy_plan_balance_scope_restricted_hint')}
                onSelect={() => setScope('restricted')}
              />
            </div>
            {restrictedWithoutBinding && (
              <p className='text-destructive text-xs'>
                {t('qy_plan_balance_scope_need_binding')}
              </p>
            )}
          </div>

          {/*
            该套餐解锁的每个模型分组，在**每个用户分组**下实际会按多少扣。

            项目方原话是「按模型分组 G 的倍率」，字面指兜底倍率；但计价链路会
            无条件优先用 (用户分组, G) 的交叉格。这个偏差是刻意保留的 ——
            用户组谈好的价不该因为买了套餐而失效 —— 但它必须**看得见**，
            否则运营只能靠猜。数值由后端现算，不经过任何扩展缓存。
          */}
          {data.ratio_table.length > 0 && (
            <div className='space-y-1'>
              <Label>{t('qy_plan_ratio_table_title')}</Label>
              <div className='max-h-40 overflow-auto rounded-md border'>
                <table className='w-full text-xs tabular-nums'>
                  <tbody>
                    {data.ratio_table.map((row) => (
                      <tr
                        key={`${row.user_group} ${row.model_group}`}
                        className='border-b last:border-b-0'
                      >
                        <td className='px-2 py-1'>{row.user_group}</td>
                        <td className='text-muted-foreground px-2 py-1'>
                          {row.model_group}
                        </td>
                        <td className='px-2 py-1 text-end font-medium'>
                          {row.ratio}
                        </td>
                        <td className='text-muted-foreground px-2 py-1 text-end'>
                          {row.source === 'group_ratio'
                            ? t('qy_plan_ratio_inherited')
                            : ''}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}

function QyPlanScopeOption(props: {
  active: boolean
  title: string
  desc: string
  onSelect: () => void
}) {
  return (
    <button
      type='button'
      onClick={props.onSelect}
      aria-pressed={props.active}
      className={
        props.active
          ? 'border-primary bg-primary/5 rounded-lg border p-3 text-start'
          : 'hover:bg-muted/50 rounded-lg border p-3 text-start'
      }
    >
      <span className='block text-sm font-medium'>{props.title}</span>
      <span className='text-muted-foreground block text-xs'>{props.desc}</span>
    </button>
  )
}
