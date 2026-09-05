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

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { getAdminPlans } from '@/features/subscriptions/api'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdInput } from '../../../components/qy-sd-input'
import { qyErrorMessage } from '../../../lib/api'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import {
  qyLotFromLocalInput,
  qyLotToLocalInput,
} from '../../admin-lottery/lib/datetime'
import { qyMallKindKey } from '../../mall/lib/product'
import type { QyMallProductKind } from '../../mall/types'
import { createQyMallProduct, updateQyMallProduct } from '../api'
import type { QyMallAdminProduct, QyMallProductInput } from '../types'
import { QyMallCoverField, type QyMallCoverValue } from './product-cover-field'

/** 三种形态的固定顺序：创建时的单选组与列表筛选共用。 */
const KINDS: readonly QyMallProductKind[] = ['code', 'physical', 'plan']

/** 与后端 `product.go` 的 maxTitleRunes / maxDescriptionRunes 同值。 */
const MAX_TITLE_RUNES = 128
const MAX_DESCRIPTION_RUNES = 2000

type Draft = {
  kind: QyMallProductKind
  title: string
  description: string
  cover: QyMallCoverValue
  price: number
  unlimitedStock: boolean
  stock: number
  per_user_limit: number
  sale_start_at: number
  sale_end_at: number
  enabled: boolean
  sort_order: number
  plan_id: number
}

function draftOf(product: QyMallAdminProduct | null): Draft {
  if (product == null) {
    return {
      kind: 'code',
      title: '',
      description: '',
      cover: { cover_ref: '', cover_url: '' },
      price: 0,
      unlimitedStock: true,
      stock: 0,
      per_user_limit: 0,
      sale_start_at: 0,
      sale_end_at: 0,
      enabled: true,
      sort_order: 0,
      plan_id: 0,
    }
  }
  // code 类的 stock 列恒为 -1（库存由码表算），视图里的 stock 是 unused 数 ——
  // 回填时不能把那个数当成"限量库存"写回去。
  const limited = product.kind !== 'code' && product.stock >= 0
  return {
    kind: product.kind,
    title: product.title,
    description: product.description,
    cover: { cover_ref: product.cover_ref ?? '', cover_url: product.cover_url },
    price: product.price,
    unlimitedStock: !limited,
    stock: limited ? product.stock : 0,
    per_user_limit: product.per_user_limit,
    sale_start_at: product.sale_start_at,
    sale_end_at: product.sale_end_at,
    enabled: product.enabled,
    sort_order: product.sort_order,
    plan_id: product.plan_id,
  }
}

/**
 * 商品的新建 / 编辑。
 *
 * ## kind 创建后不可改
 *
 * 三种商品的履行方式完全不同：一件已经发过码的商品改成实物，它名下的订单没有
 * 任何一条能解释。后端直接 400，这里在编辑态把单选组禁用并说明原因。
 *
 * ## 套餐商品在 outbox 关闭时不能上架
 *
 * 套餐订单的失败退款判据是主库 outbox 探针；探针关着时每一笔失败都会变成
 * held 交人工，所以后端对 `kind=plan` 的创建、以及编辑时 `enabled=true`，一律
 * 400 `qy_ml_plan_needs_outbox`。前端不知道探针开没开（它在 YAML 里），所以
 * 不预判，只把那条 400 的文案原样显示出来 —— 它已登记在 `QY_ERROR_CODE_I18N`。
 *
 * ## cover_ref 每次都原样带回
 *
 * 后端把请求体里的 `cover_ref` 直接写进列（空串 = 清掉封面），编辑时**不带**
 * 就等于把现有封面删了。所以草稿从 `product.cover_ref` 起草，保存时无条件带上。
 */
export function QyMallProductFormDialog(props: {
  open: boolean
  product: QyMallAdminProduct | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const ids = {
    title: useId(),
    description: useId(),
    plan: useId(),
    price: useId(),
    stock: useId(),
    limit: useId(),
    sort: useId(),
    enabled: useId(),
    unlimited: useId(),
  }
  const editing = props.product != null
  const [draft, setDraft] = useState<Draft>(() => draftOf(props.product))

  // 每次打开都从服务端那一份重新起草：弹窗关掉不代表改动被保存。
  useEffect(() => {
    if (!props.open) return
    setDraft(draftOf(props.product))
  }, [props.open, props.product])

  const patch = (next: Partial<Draft>) =>
    setDraft((prev) => ({ ...prev, ...next }))

  // 套餐候选清单来自上游的套餐管理接口，只在选了 kind=plan 时才拉。
  const plansQuery = useQuery({
    queryKey: qyKeys.adminMallPlanOptions(),
    queryFn: async () => {
      const res = await getAdminPlans()
      return res.success ? qyArray(res.data).map((row) => row.plan) : []
    },
    enabled: props.open && draft.kind === 'plan',
    staleTime: 60_000,
  })

  const save = useMutation({
    mutationFn: () => {
      const body: QyMallProductInput = {
        kind: draft.kind,
        title: draft.title.trim(),
        description: draft.description.trim(),
        cover_ref: draft.cover.cover_ref,
        price: draft.price,
        // code 类由码库存决定，列值恒为 -1；其余按"不限 / 限量"二选一。
        stock: draft.kind === 'code' || draft.unlimitedStock ? -1 : draft.stock,
        per_user_limit: draft.per_user_limit,
        sale_start_at: draft.sale_start_at,
        sale_end_at: draft.sale_end_at,
        enabled: draft.enabled,
        sort_order: draft.sort_order,
        plan_id: draft.kind === 'plan' ? draft.plan_id : undefined,
      }
      return props.product == null
        ? createQyMallProduct(body)
        : updateQyMallProduct(props.product.product_no, body)
    },
    onSuccess: async () => {
      toast.success(t('qy_mladm_saved'))
      props.onClose()
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const titleLength = [...draft.title.trim()].length
  const descriptionLength = [...draft.description.trim()].length
  const windowInvalid =
    draft.sale_start_at > 0 &&
    draft.sale_end_at > 0 &&
    draft.sale_start_at >= draft.sale_end_at
  const canSave =
    !save.isPending &&
    titleLength > 0 &&
    titleLength <= MAX_TITLE_RUNES &&
    descriptionLength <= MAX_DESCRIPTION_RUNES &&
    draft.price >= 1 &&
    (draft.kind !== 'plan' || draft.plan_id > 0) &&
    (draft.kind === 'code' || draft.unlimitedStock || draft.stock >= 0) &&
    draft.per_user_limit >= 0 &&
    !windowInvalid

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={editing ? t('qy_mladm_edit_title') : t('qy_mladm_create_title')}
      description={props.product?.product_no}
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={!canSave}
            onClick={() => save.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-2'>
          <Label>{t('qy_ml_kind')}</Label>
          <RadioGroup
            value={draft.kind}
            disabled={editing}
            onValueChange={(value) => {
              const kind = KINDS.find((item) => item === value)
              if (kind != null) patch({ kind })
            }}
            className='gap-2'
          >
            {KINDS.map((kind) => (
              <label
                key={kind}
                className='hover:bg-muted/40 flex cursor-pointer items-start gap-3 rounded-lg border p-3'
              >
                <RadioGroupItem value={kind} className='mt-0.5' />
                <span className='min-w-0 flex-1'>
                  <span className='block text-sm font-medium'>
                    {t(qyMallKindKey(kind))}
                  </span>
                  <span className='text-muted-foreground mt-0.5 block text-xs'>
                    {t(`qy_mladm_kind_desc_${kind}`)}
                  </span>
                </span>
              </label>
            ))}
          </RadioGroup>
          {editing && (
            <p className='text-muted-foreground text-xs'>
              {t('qy_mladm_kind_locked')}
            </p>
          )}
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={ids.title}>{t('qy_mladm_col_title')}</Label>
          <Input
            id={ids.title}
            value={draft.title}
            maxLength={MAX_TITLE_RUNES}
            aria-invalid={titleLength > MAX_TITLE_RUNES}
            onChange={(event) => patch({ title: event.target.value })}
          />
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={ids.description}>{t('qy_mladm_description')}</Label>
          <Textarea
            id={ids.description}
            rows={4}
            value={draft.description}
            aria-invalid={descriptionLength > MAX_DESCRIPTION_RUNES}
            onChange={(event) => patch({ description: event.target.value })}
          />
          <p className='text-muted-foreground text-end text-xs tabular-nums'>
            {t('qy_common_rune_counter', {
              used: descriptionLength,
              max: MAX_DESCRIPTION_RUNES,
            })}
          </p>
        </div>

        {draft.kind === 'plan' && (
          <div className='space-y-1.5'>
            <Label htmlFor={ids.plan}>{t('qy_mladm_plan')}</Label>
            <NativeSelect
              id={ids.plan}
              className='w-full'
              value={draft.plan_id > 0 ? String(draft.plan_id) : ''}
              onChange={(event) =>
                patch({ plan_id: Number(event.target.value || 0) })
              }
            >
              <NativeSelectOption value=''>
                {plansQuery.isPending
                  ? t('qy_mladm_plan_loading')
                  : t('qy_mladm_plan_pick')}
              </NativeSelectOption>
              {(plansQuery.data ?? []).map((plan) => (
                <NativeSelectOption key={plan.id} value={String(plan.id)}>
                  #{plan.id} {plan.title}
                  {plan.enabled ? '' : ` (${t('qy_common_off')})`}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className='text-muted-foreground text-xs'>
              {t('qy_mladm_plan_hint')}
            </p>
          </div>
        )}

        <div className='space-y-1.5'>
          <Label htmlFor={ids.price}>{t('qy_ml_price')}</Label>
          <QySdInput
            id={ids.price}
            value={draft.price}
            min={1}
            onChange={(price) => patch({ price })}
          />
        </div>

        {draft.kind === 'code' ? (
          <p className='text-muted-foreground text-xs'>
            {t('qy_mladm_stock_by_codes')}
          </p>
        ) : (
          <div className='space-y-2'>
            <Label htmlFor={ids.unlimited} className='text-sm font-normal'>
              <Checkbox
                id={ids.unlimited}
                checked={draft.unlimitedStock}
                onCheckedChange={(checked) =>
                  patch({ unlimitedStock: checked === true })
                }
              />
              {t('qy_mladm_stock_unlimited')}
            </Label>
            {!draft.unlimitedStock && (
              <div className='space-y-1.5'>
                <Label htmlFor={ids.stock}>{t('qy_mladm_stock')}</Label>
                <Input
                  id={ids.stock}
                  type='number'
                  min={0}
                  step={1}
                  value={draft.stock}
                  onChange={(event) =>
                    patch({
                      stock: Math.max(
                        0,
                        Math.trunc(Number(event.target.value))
                      ),
                    })
                  }
                />
                {editing && props.product != null && (
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_mladm_stock_sold', { count: props.product.sold })}
                  </p>
                )}
              </div>
            )}
          </div>
        )}

        <div className='space-y-1.5'>
          <Label htmlFor={ids.limit}>{t('qy_mladm_per_user_limit')}</Label>
          <Input
            id={ids.limit}
            type='number'
            min={0}
            step={1}
            value={draft.per_user_limit}
            onChange={(event) =>
              patch({
                per_user_limit: Math.max(
                  0,
                  Math.trunc(Number(event.target.value))
                ),
              })
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_mladm_per_user_limit_hint')}
          </p>
        </div>

        <div className='grid gap-3 sm:grid-cols-2'>
          <TimeField
            label={t('qy_ml_sale_start_at')}
            value={draft.sale_start_at}
            onChange={(value) => patch({ sale_start_at: value })}
          />
          <TimeField
            label={t('qy_ml_sale_end_at')}
            value={draft.sale_end_at}
            onChange={(value) => patch({ sale_end_at: value })}
          />
        </div>
        <p
          className={
            windowInvalid
              ? 'text-destructive text-xs'
              : 'text-muted-foreground text-xs'
          }
        >
          {windowInvalid
            ? t('qy_mladm_sale_window_invalid')
            : t('qy_mladm_sale_window_hint')}
        </p>

        <QyMallCoverField
          value={draft.cover}
          kind={draft.kind}
          disabled={save.isPending}
          onChange={(cover) => patch({ cover })}
        />

        <div className='grid gap-3 sm:grid-cols-2'>
          <div className='space-y-1.5'>
            <Label htmlFor={ids.sort}>{t('qy_mladm_sort_order')}</Label>
            <Input
              id={ids.sort}
              type='number'
              step={1}
              value={draft.sort_order}
              onChange={(event) =>
                patch({ sort_order: Math.trunc(Number(event.target.value)) })
              }
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_mladm_sort_order_hint')}
            </p>
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor={ids.enabled}>{t('qy_mladm_enabled')}</Label>
            <div className='flex h-9 items-center'>
              <Switch
                id={ids.enabled}
                checked={draft.enabled}
                onCheckedChange={(checked) => patch({ enabled: checked })}
              />
            </div>
            {draft.kind === 'plan' && (
              <p className='text-muted-foreground text-xs'>
                {t('qy_mladm_plan_outbox_hint')}
              </p>
            )}
          </div>
        </div>
      </div>
    </QyResponsiveDialog>
  )
}

/** `datetime-local` ↔ unix 秒，换算只在 `admin-lottery/lib/datetime.ts` 一处。 */
function TimeField(props: {
  label: string
  value: number
  onChange: (value: number) => void
}) {
  const id = useId()
  return (
    <div className='space-y-1.5'>
      <Label htmlFor={id}>{props.label}</Label>
      <Input
        id={id}
        type='datetime-local'
        value={qyLotToLocalInput(props.value)}
        onChange={(event) =>
          props.onChange(qyLotFromLocalInput(event.target.value))
        }
      />
    </div>
  )
}
