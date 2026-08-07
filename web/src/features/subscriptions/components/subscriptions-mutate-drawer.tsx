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
import { zodResolver } from '@hookform/resolvers/zod'
import {
  Boxes,
  CalendarClock,
  CreditCard,
  RefreshCw,
  Settings2,
  TriangleAlert,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  SideDrawerSection,
  sideDrawerSwitchItemClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { IconBadge } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { QyResponsiveDialog } from '@/features/qy/components/qy-responsive-dialog'
import { useQyConfig } from '@/features/qy/hooks/use-qy-config'
import { isQyError, qyErrorMessage } from '@/features/qy/lib/api'
import { useQyPlanUnlockGroups } from '@/features/qy/plan-entitlement/use-plan-unlock-groups'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { cn } from '@/lib/utils'

import {
  createPlan,
  updatePlan,
  getPlanUsage,
  setPlanSeatLimit,
  createWaffoPancakeSubscriptionProduct,
  listWaffoPancakeSubscriptionProductOptions,
} from '../api'
import { getDurationUnitOptions, getResetPeriodOptions } from '../constants'
import {
  getPlanFormSchema,
  PLAN_FORM_DEFAULTS,
  planToFormValues,
  formValuesToPlanPayload,
  type PlanFormValues,
} from '../lib'
import type { PlanRecord, PlanUsage } from '../types'
import { useSubscriptions } from './subscriptions-provider'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentRow?: PlanRecord
}

/**
 * 总名额那一格的可用状态。
 *
 * `error` 与 `hidden` 必须分开：`hidden` 是"这套后端根本没这功能"（扩展关闭或
 * 接口未部署），零痕迹不显示；`error` 是"功能在、这次没读到"，要把格子显示出来
 * 但禁用并说明原因 —— 此时表单里的 0 是占位而不是真值，允许提交就会把已设好的
 * 名额上限悄悄抹成"不限"。
 */
type SeatCapState = 'error' | 'hidden' | 'loading' | 'ready'

export function SubscriptionsMutateDrawer({
  open,
  onOpenChange,
  currentRow,
}: Props) {
  const { t } = useTranslation()
  const isEdit = !!currentRow?.plan?.id
  const { triggerRefresh } = useSubscriptions()
  const { meta: currencyMeta } = getCurrencyDisplay()
  const tokensOnly = currencyMeta.kind === 'tokens'
  const currencyLabel = getCurrencyLabel()
  const qyConfig = useQyConfig()
  const seatCapSupported = qyConfig.status !== 'disabled'
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [seatUsage, setSeatUsage] = useState<PlanUsage | null>(null)
  const [seatCapState, setSeatCapState] = useState<SeatCapState>('hidden')
  const [creatingPancakeProduct, setCreatingPancakeProduct] = useState(false)
  const [pancakeProducts, setPancakeProducts] = useState<
    { id: string; name: string; status: string }[]
  >([])

  // 解锁模型分组：与总名额同一个形状（扩展库、独立的一次写入、读不到就禁用），
  // 所以判据也用同一个 seatCapSupported。
  const unlockGroups = useQyPlanUnlockGroups({
    open,
    planId: currentRow?.plan?.id ?? 0,
    supported: seatCapSupported,
  })

  const schema = getPlanFormSchema(t)
  const form = useForm<PlanFormValues>({
    resolver: zodResolver(schema) as unknown as Resolver<PlanFormValues>,
    defaultValues: PLAN_FORM_DEFAULTS,
  })

  useEffect(() => {
    if (open) {
      if (currentRow?.plan) {
        form.reset(planToFormValues(currentRow.plan))
      } else {
        form.reset(PLAN_FORM_DEFAULTS)
      }
      // Best-effort — empty list still lets the operator use "+ Create".
      listWaffoPancakeSubscriptionProductOptions()
        .then((res) => {
          if (
            res.message === 'success' &&
            typeof res.data === 'object' &&
            res.data &&
            Array.isArray((res.data as { products?: unknown }).products)
          ) {
            setPancakeProducts(
              (res.data as { products: typeof pancakeProducts }).products
            )
          } else {
            setPancakeProducts([])
          }
        })
        .catch(() => setPancakeProducts([]))
    }
  }, [open, currentRow, form])

  // 总名额存在扩展库里，和套餐本体不是同一次请求，所以单独一个 effect：
  // 混进上面那个会让"扩展配置从 unknown 变成 disabled"顺带把整张表单 reset 掉，
  // 管理员填了一半的内容就没了。
  useEffect(() => {
    if (!open) return
    if (!seatCapSupported) {
      setSeatUsage(null)
      setSeatCapState('hidden')
      return
    }
    const planId = currentRow?.plan?.id
    if (planId == null || planId <= 0) {
      // 新建：没有 id 可查，空占用 + 可编辑，保存成功后再写。
      setSeatUsage(null)
      setSeatCapState('ready')
      return
    }
    setSeatUsage(null)
    setSeatCapState('loading')
    let stale = false
    getPlanUsage(planId)
      .then((usage) => {
        if (stale) return
        setSeatUsage(usage)
        setSeatCapState('ready')
        // 只在用户没动过这一格时回填：请求在途期间改的值不能被覆盖掉。
        if (!form.getFieldState('max_total_users').isDirty) {
          form.setValue('max_total_users', usage.capacity)
        }
      })
      .catch((error) => {
        if (stale) return
        setSeatUsage(null)
        setSeatCapState(isQyError(error) && error.isHidden ? 'hidden' : 'error')
      })
    return () => {
      stale = true
    }
  }, [open, currentRow, form, seatCapSupported])

  const durationUnit = form.watch('duration_unit')
  const resetPeriod = form.watch('quota_reset_period')
  // Gate "+ Create on Pancake" on the same checks the mint handler runs.
  const watchedTitle = form.watch('title')
  const watchedPrice = form.watch('price_amount')
  const pancakeCreateReady =
    typeof watchedTitle === 'string' &&
    watchedTitle.trim().length > 0 &&
    Number(watchedPrice ?? 0) > 0

  const onSubmit = async (values: PlanFormValues) => {
    // 先挡住必然会被后端拒绝的组合，再动套餐本体。反过来的话套餐已经改完了，
    // 而运营看到的是一条解锁分组的报错 —— 他不会知道前半截已经落库了。
    if (unlockGroups.restrictedWithoutBinding) {
      toast.error(t('qy_plan_balance_scope_need_binding'))
      return
    }
    setIsSubmitting(true)
    try {
      const payload = formValuesToPlanPayload(values)
      let planId = currentRow?.plan?.id ?? 0
      if (isEdit && planId > 0) {
        const res = await updatePlan(planId, payload)
        if (!res.success) return
      } else {
        const res = await createPlan(payload)
        if (!res.success) return
        planId = res.data?.id ?? 0
      }

      // 套餐本体（上游表）与总名额（扩展库）是两次写入，中间没有事务。
      // 只在值真的变了时才写第二次，避免每次保存都往审计里塞一条没有内容的改动；
      // 第二次失败时不能报"保存成功"，否则运营会以为名额已经生效。
      const nextSeatCap = Number(values.max_total_users || 0)
      let seatCapError: unknown = null
      if (
        seatCapState === 'ready' &&
        planId > 0 &&
        nextSeatCap !== (seatUsage?.capacity ?? 0)
      ) {
        try {
          await setPlanSeatLimit(planId, nextSeatCap)
        } catch (error) {
          seatCapError = error
        }
      }

      // 解锁模型分组是**第三次**写入（主库套餐 → 扩展库名额 → 扩展库解锁），
      // 同样没有事务。失败必须单独报出来：它决定"买了这个套餐的人能用哪些模型
      // 分组"，报成"保存成功"会让运营以为已经开通了，而买过的人一个都拿不到。
      let unlockError: unknown = null
      try {
        await unlockGroups.saveIfChanged(planId)
      } catch (error) {
        unlockError = error
      }

      if (seatCapError == null && unlockError == null) {
        toast.success(isEdit ? t('Update succeeded') : t('Create succeeded'))
      }
      if (seatCapError != null) {
        toast.error(
          `${t('qy_plan_seat_cap_save_failed')}: ${qyErrorMessage(seatCapError, t)}`
        )
      }
      if (unlockError != null) {
        toast.error(
          `${t('Plan saved, but the unlocked model groups were not stored. Reopen the plan and set them again')}: ${qyErrorMessage(unlockError, t)}`
        )
      }
      onOpenChange(false)
      triggerRefresh()
    } catch {
      toast.error(t('Request failed'))
    } finally {
      setIsSubmitting(false)
    }
  }

  // Mints a Pancake OnetimeProduct (not SubscriptionProduct — see
  // controller) using persisted creds + the form's title/price, then
  // pins the returned PROD_ ID into the form field.
  const handleCreatePancakeProduct = async () => {
    const title = form.getValues('title').trim()
    const priceAmount = Number(form.getValues('price_amount') || 0)
    if (!title) {
      toast.error(t('Plan title is required'))
      return
    }
    if (priceAmount <= 0) {
      toast.error(t('Plan price must be greater than zero'))
      return
    }
    setCreatingPancakeProduct(true)
    try {
      const res = await createWaffoPancakeSubscriptionProduct({
        name: title,
        amount: priceAmount.toFixed(2),
      })
      if (
        res.message === 'success' &&
        typeof res.data === 'object' &&
        res.data
      ) {
        const created = res.data as {
          product_id: string
          product_name: string
        }
        form.setValue('waffo_pancake_product_id', created.product_id, {
          shouldDirty: true,
        })
        // Refetch from GraphQL so the dropdown reflects authoritative state.
        try {
          const refresh = await listWaffoPancakeSubscriptionProductOptions()
          if (
            refresh.message === 'success' &&
            typeof refresh.data === 'object' &&
            refresh.data &&
            Array.isArray((refresh.data as { products?: unknown }).products)
          ) {
            setPancakeProducts(
              (refresh.data as { products: typeof pancakeProducts }).products
            )
          }
        } catch {
          // Best-effort — form value already points at the new product;
          // raw-ID fallback covers the missing label.
        }
        toast.success(
          `${t('Waffo Pancake product created')}: ${created.product_id}`
        )
      } else {
        const reason = typeof res.data === 'string' ? res.data : undefined
        toast.error(
          reason
            ? `${t('Waffo Pancake product creation failed')}: ${reason}`
            : t('Waffo Pancake product creation failed')
        )
      }
    } catch (err) {
      toast.error(
        `${t('Waffo Pancake product creation failed')}: ${err instanceof Error ? err.message : String(err)}`
      )
    } finally {
      setCreatingPancakeProduct(false)
    }
  }

  const durationUnitOpts = getDurationUnitOptions(t)
  const resetPeriodOpts = getResetPeriodOptions(t)

  return (
    // 桌面居中窗口 / 手机侧出抽屉，且头尾固定只有正文滚 —— 全部由外壳负责。
    // 提交按钮落在 footer（不随正文滚走），靠 HTML 原生的 form 属性跨出 <form>
    // 触发提交，所以表单主体不需要挪位置。
    <QyResponsiveDialog
      open={open}
      onOpenChange={(v) => {
        onOpenChange(v)
        if (!v) {
          form.reset()
        }
      }}
      // 移动分支的抽屉默认只有 sm:max-w-sm(384px)，而表单内部大量
      // `sm:grid-cols-2` 在 640px 就已经生效 —— useIsMobile 的阈值是 768，
      // 两个断点错开的那一档（640~767px）会变成"384px 宽的抽屉里塞两列"，
      // 标签折成三行、数字输入框只剩几十像素。放宽到 md 让两者重新对齐。
      sheetClassName='sm:max-w-md'
      title={isEdit ? t('Update plan info') : t('Create new subscription plan')}
      description={
        isEdit
          ? t('Modify existing subscription plan configuration')
          : t('Fill in the following info to create a new subscription plan')
      }
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
          >
            {t('Close')}
          </Button>
          <Button
            form='subscription-form'
            type='submit'
            disabled={isSubmitting || unlockGroups.restrictedWithoutBinding}
          >
            {isSubmitting ? t('Saving...') : t('Save changes')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id='subscription-form'
          onSubmit={form.handleSubmit(onSubmit)}
          className='flex flex-col gap-6 py-2'
        >
          {/* Basic Info */}
          <SideDrawerSection>
            <h3 className='flex items-center gap-2 text-sm font-medium'>
              <IconBadge tone='info' size='xs'>
                <Settings2 />
              </IconBadge>
              {t('Basic Info')}
            </h3>

            <FormField
              control={form.control}
              name='title'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Plan Title')}</FormLabel>
                  <FormControl>
                    <Input {...field} placeholder={t('e.g. Basic Plan')} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='subtitle'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Plan Subtitle')}</FormLabel>
                  <FormControl>
                    {/* Textarea, not Input: the field holds up to 255 chars
                          and the user-facing views render its line breaks. */}
                    <Textarea
                      {...field}
                      rows={3}
                      maxLength={255}
                      placeholder={t('e.g. Suitable for light usage')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='price_amount'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Plan Price')}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        type='number'
                        step='0.01'
                        min={0}
                        onChange={(e) =>
                          field.onChange(Number.parseFloat(e.target.value) || 0)
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Amount the user pays to purchase this plan; the actual currency depends on the payment gateway.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='total_amount'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>
                      {t('Quota ({{currency}})', { currency: currencyLabel })}
                    </FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        type='number'
                        min={0}
                        step={tokensOnly ? 1 : 0.01}
                        placeholder={
                          tokensOnly
                            ? t('Enter quota in tokens')
                            : t('Enter quota in {{currency}}', {
                                currency: currencyLabel,
                              })
                        }
                        onChange={(e) =>
                          field.onChange(Number.parseFloat(e.target.value) || 0)
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Total quota included in the plan, usable per billing period. 0 means unlimited.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            {/*
              「解锁模型分组」。它取代的是上游的「升级分组 / 降级分组」两个下拉。

              替换而不是并列：那两个下拉写的是购买时**改写 users.group** 的目标，
              而用户分组与模型分组分离之后，买套餐只该多几个模型分组，不该把人
              从一个用户分组搬到另一个（会连带换掉可用范围、倍率与自动分组）。
              两列的存量处置见 lib/plan-form.ts 里 upgrade_group: '' 那段。

              这一格与总名额那一格同构：落扩展库、与套餐本体不是同一次写入、
              读不到时禁用而不是当成空。余额使用范围（通用 / 仅限）与实际倍率表
              留在行操作的完整弹窗里 —— 那两块要摆影响面与现算倍率，塞进这里会
              把一个已经很长的表单再撑长一屏，而它们的改动频率远低于这一格。
            */}
            {unlockGroups.state !== 'hidden' && (
              <div className='border-border/60 flex flex-col gap-2 rounded-md border p-3'>
                <div>
                  <h4 className='text-sm font-medium'>
                    {t('qy_plan_unlock_groups_label')}
                  </h4>
                  <p className='text-muted-foreground mt-1 text-xs leading-5'>
                    {t('qy_plan_unlock_groups_hint')}
                  </p>
                </div>

                {unlockGroups.state === 'loading' && (
                  <p className='text-muted-foreground text-xs'>
                    {t('Loading...')}
                  </p>
                )}

                {/* 读失败：格子留着但空着，且保存时**不写**。不说这一句的话，
                    界面上那个空清单看起来就是"这个套餐没解锁任何分组"。 */}
                {unlockGroups.state === 'error' && (
                  <p className='text-destructive text-xs'>
                    {t(
                      'Unlocked model groups could not be loaded; saving now leaves them untouched.'
                    )}
                  </p>
                )}

                {/* 新建：套餐还没有 id，解锁绑定挂在 plan_id 上，没有对象可挂。
                    不能沉默地画一个空清单 —— 那看起来就是"可以在这里选，只是一个
                    分组都没有"，而管理员勾不动任何东西也得不到任何解释。 */}
                {unlockGroups.state === 'ready' && !isEdit && (
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Model group unlocks can be configured once the plan exists — save it first, then reopen it.'
                    )}
                  </p>
                )}

                {unlockGroups.state === 'ready' &&
                  isEdit &&
                  (unlockGroups.candidates.length === 0 &&
                  unlockGroups.orphans.length === 0 ? (
                    <p className='text-muted-foreground text-xs'>
                      {t('qy_plan_unlock_groups_empty')}
                    </p>
                  ) : (
                    <div className='grid max-h-56 gap-1 overflow-auto rounded-md border p-2 sm:grid-cols-2'>
                      {unlockGroups.candidates.map((group) => (
                        <label
                          key={group}
                          className='hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-xs'
                        >
                          <Checkbox
                            checked={unlockGroups.selected.includes(group)}
                            onCheckedChange={() => unlockGroups.toggle(group)}
                          />
                          <Boxes
                            aria-hidden='true'
                            className='text-muted-foreground size-3 shrink-0'
                          />
                          <span className='truncate'>{group}</span>
                        </label>
                      ))}
                      {/* 已绑定、但已经从分组倍率表里消失的名字。必须渲染出来
                          并且可以取消勾选：只画一条提示的话，这个名字在界面上
                          不可见也摘不掉，而每次保存都会把它原样提交回去。 */}
                      {unlockGroups.orphans.map((group) => (
                        <label
                          key={group}
                          className='hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-xs'
                        >
                          <Checkbox
                            checked
                            onCheckedChange={() => unlockGroups.toggle(group)}
                          />
                          <TriangleAlert
                            aria-hidden='true'
                            className='text-destructive size-3 shrink-0'
                          />
                          <span className='truncate line-through'>{group}</span>
                        </label>
                      ))}
                    </div>
                  ))}

                {/* 「仅限」的套餐被摘光绑定 = 一份任何请求都用不上的死钱。
                    后端会 400，但按钮就该在这里禁用并说明原因。 */}
                {unlockGroups.restrictedWithoutBinding && (
                  <p className='text-destructive text-xs'>
                    {t('qy_plan_balance_scope_need_binding')}
                  </p>
                )}
              </div>
            )}

            {/*
              两个限购维度必须放在同一个框里并排显示，且各自说清楚"数的是什么"。
              拆开摆、或者两格都只写一句"0 表示不限"，运营就会把"全站只招 100 人"
              填进左边那格，实际生效的是"每人可买 100 次" —— 一个卖爆一个卖不动，
              而且两种填错都不会报错，只能等出事了才发现。
            */}
            {/* 名额格子不显示时（扩展关闭 / 接口不可用），这圈边框、标题与
                "左边管…右边管…"的说明必须一起消失：只剩左边一格却还留着一句
                描述两格的说明，运营的合理推断是"右边那格被合并进左边了"，
                于是把"全站只招 100 人"填进"限购"，实际生效的是"每人可买 100 次"，
                全站不限量 —— 而两种填法都不会报错。 */}
            <div
              className={cn(
                'flex flex-col gap-3',
                seatCapState !== 'hidden' &&
                  'border-border/60 rounded-md border p-3'
              )}
            >
              {seatCapState !== 'hidden' && (
                <div>
                  <h4 className='text-sm font-medium'>
                    {t('qy_plan_limits_title')}
                  </h4>
                  <p className='text-muted-foreground mt-1 text-xs leading-5'>
                    {t('qy_plan_limits_desc')}
                  </p>
                </div>
              )}

              <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='max_purchase_per_user'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t('Purchase Limit')}
                        {seatCapState !== 'hidden' && (
                          <span className='text-muted-foreground ml-1 text-xs font-normal'>
                            {t('qy_plan_scope_per_user')}
                          </span>
                        )}
                      </FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='number'
                          min={0}
                          onChange={(e) =>
                            field.onChange(
                              Number.parseInt(e.target.value, 10) || 0
                            )
                          }
                        />
                      </FormControl>
                      <FormDescription>
                        {/* 上游原文，7 个语种都有译文。换成 qy_ 前缀的自造 key
                            会让法/日/越/俄/繁中这 5 个语种退回英文 —— 而这是
                            上游既有字段的既有文案，不是本轮新增的东西。
                            两个上限的区分由上面的标题与 · 按人次 / · 按全站人数
                            这两个 scope 标签负责，不靠这一句。 */}
                        {t('0 means unlimited')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                {seatCapState !== 'hidden' && (
                  <FormField
                    control={form.control}
                    name='max_total_users'
                    render={({ field }) => {
                      // 占用数拿现存数据、名额拿输入框的当前值，这样把名额往下调到
                      // 低于已占用时当场就能看见（后端不会因此清退已有订阅，只是从此
                      // 卖不出去，界面不说的话没人会发现这一格填小了）。
                      const capValue = Number(field.value || 0)
                      const overCap =
                        seatUsage != null &&
                        capValue > 0 &&
                        seatUsage.used_seats > capValue
                      return (
                        <FormItem>
                          <FormLabel>
                            {t('qy_plan_seat_cap_label')}
                            <span className='text-muted-foreground ml-1 text-xs font-normal'>
                              {t('qy_plan_scope_site_wide')}
                            </span>
                          </FormLabel>
                          <FormControl>
                            <Input
                              {...field}
                              type='number'
                              min={0}
                              disabled={seatCapState !== 'ready'}
                              onChange={(e) =>
                                field.onChange(
                                  Number.parseInt(e.target.value, 10) || 0
                                )
                              }
                            />
                          </FormControl>
                          <FormDescription>
                            {t('qy_plan_seat_cap_hint')}
                          </FormDescription>
                          {seatCapState === 'loading' && (
                            <p className='text-muted-foreground text-xs'>
                              {t('qy_plan_seat_cap_loading')}
                            </p>
                          )}
                          {seatCapState === 'error' && (
                            <p className='text-destructive text-xs'>
                              {t('qy_plan_seat_cap_unavailable')}
                            </p>
                          )}
                          {seatUsage != null && (
                            <p
                              className={cn(
                                'text-xs tabular-nums',
                                overCap
                                  ? 'text-destructive'
                                  : 'text-muted-foreground'
                              )}
                            >
                              {t('qy_plan_seat_usage', {
                                used: seatUsage.used_seats,
                                cap:
                                  capValue > 0
                                    ? capValue
                                    : t('qy_plan_seat_unlimited'),
                              })}
                              {overCap ? ` ${t('qy_plan_seat_over_cap')}` : ''}
                            </p>
                          )}
                          <FormMessage />
                        </FormItem>
                      )
                    }}
                  />
                )}
              </div>
            </div>

            <FormField
              control={form.control}
              name='sort_order'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Sort Order')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='number'
                      onChange={(e) =>
                        field.onChange(Number.parseInt(e.target.value, 10) || 0)
                      }
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <div className='flex flex-col gap-3'>
              <FormField
                control={form.control}
                name='enabled'
                render={({ field }) => (
                  <FormItem className={sideDrawerSwitchItemClassName()}>
                    <FormLabel className='!mt-0'>
                      {t('Enabled Status')}
                    </FormLabel>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='allow_balance_pay'
                render={({ field }) => (
                  <FormItem className={sideDrawerSwitchItemClassName()}>
                    <FormLabel className='!mt-0'>
                      {t('Allow balance redemption')}
                    </FormLabel>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='allow_wallet_overflow'
                render={({ field }) => (
                  <FormItem className={sideDrawerSwitchItemClassName()}>
                    <FormLabel className='!mt-0'>
                      {t('Allow wallet balance after quota used up')}
                    </FormLabel>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
            </div>
          </SideDrawerSection>

          {/* Duration Settings */}
          <SideDrawerSection>
            <h3 className='flex items-center gap-2 text-sm font-medium'>
              <IconBadge tone='chart-4' size='xs'>
                <CalendarClock />
              </IconBadge>
              {t('Duration Settings')}
            </h3>

            <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='duration_unit'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Duration Unit')}</FormLabel>
                    <Select
                      items={durationUnitOpts.map((o) => ({
                        value: o.value,
                        label: o.label,
                      }))}
                      onValueChange={field.onChange}
                      value={field.value}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          {durationUnitOpts.map((o) => (
                            <SelectItem key={o.value} value={o.value}>
                              {o.label}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />

              {durationUnit === 'custom' ? (
                <FormField
                  control={form.control}
                  name='custom_seconds'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Custom Seconds')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='number'
                          min={1}
                          onChange={(e) =>
                            field.onChange(
                              Number.parseInt(e.target.value, 10) || 0
                            )
                          }
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ) : (
                <FormField
                  control={form.control}
                  name='duration_value'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Duration Value')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='number'
                          min={1}
                          onChange={(e) =>
                            field.onChange(
                              Number.parseInt(e.target.value, 10) || 0
                            )
                          }
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}
            </div>
          </SideDrawerSection>

          {/* Quota Reset */}
          <SideDrawerSection>
            <h3 className='flex items-center gap-2 text-sm font-medium'>
              <IconBadge tone='success' size='xs'>
                <RefreshCw />
              </IconBadge>
              {t('Quota Reset')}
            </h3>

            <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='quota_reset_period'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Reset Cycle')}</FormLabel>
                    <Select
                      items={resetPeriodOpts.map((o) => ({
                        value: o.value,
                        label: o.label,
                      }))}
                      onValueChange={field.onChange}
                      value={field.value}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          {resetPeriodOpts.map((o) => (
                            <SelectItem key={o.value} value={o.value}>
                              {o.label}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='quota_reset_custom_seconds'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Custom Seconds')}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        type='number'
                        min={0}
                        disabled={resetPeriod !== 'custom'}
                        onChange={(e) =>
                          field.onChange(
                            Number.parseInt(e.target.value, 10) || 0
                          )
                        }
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          </SideDrawerSection>

          {/* Payment Config */}
          <SideDrawerSection>
            <h3 className='flex items-center gap-2 text-sm font-medium'>
              <IconBadge tone='warning' size='xs'>
                <CreditCard />
              </IconBadge>
              {t('Third-party Payment Config')}
            </h3>

            <FormField
              control={form.control}
              name='stripe_price_id'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Stripe Price ID</FormLabel>
                  <FormControl>
                    <Input {...field} placeholder='price_...' />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='creem_product_id'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Creem Product ID</FormLabel>
                  <FormControl>
                    <Input {...field} placeholder='prod_...' />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='waffo_pancake_product_id'
              render={({ field }) => {
                // Raw-ID fallback for IDs not yet in the catalog.
                const items = pancakeProducts.map((p) => ({
                  value: p.id,
                  label: `${p.name} (${p.id})`,
                }))
                if (
                  field.value &&
                  !pancakeProducts.some((p) => p.id === field.value)
                ) {
                  items.push({ value: field.value, label: field.value })
                }
                return (
                  <FormItem>
                    <FormLabel>Waffo Pancake Product ID</FormLabel>
                    <div className='flex gap-2'>
                      <Select
                        items={items}
                        value={field.value || ''}
                        onValueChange={(v) => field.onChange(v)}
                        disabled={items.length === 0}
                      >
                        <SelectTrigger className='w-full flex-1'>
                          <SelectValue placeholder={t('Select a product')} />
                        </SelectTrigger>
                        <SelectContent>
                          {items.map((item) => (
                            <SelectItem key={item.value} value={item.value}>
                              {item.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <Button
                        type='button'
                        variant='outline'
                        onClick={handleCreatePancakeProduct}
                        disabled={creatingPancakeProduct || !pancakeCreateReady}
                        className='shrink-0'
                      >
                        {creatingPancakeProduct
                          ? t('Creating...')
                          : `+ ${t('Create')}`}
                      </Button>
                    </div>
                    <FormDescription>
                      {t(
                        'Creates a Pancake product in the saved store using this plan’s title and price. Requires Waffo Pancake to be fully configured in Payment settings first.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )
              }}
            />
          </SideDrawerSection>
        </form>
      </Form>
    </QyResponsiveDialog>
  )
}
