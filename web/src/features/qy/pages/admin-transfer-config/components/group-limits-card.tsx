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
import { Info, Plus, ShieldAlert, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import {
  qyUserGroupOptionLabel,
  type QyUserGroupOption,
} from '../../../lib/group-options'
import { qyKeys } from '../../../lib/query-keys'
import {
  qyFormatQuotaAsUsd,
  qyUsdScale,
  type QyUsdScale,
} from '../../../lib/quota-usd'
import {
  qyAdminTransferGroupLimitsQuery,
  qyDeleteTransferGroupLimit,
  qyPutTransferGroupLimit,
} from '../group-limits-api'
import {
  QY_TIERABLE_KEYS,
  type QyTierableKey,
  type QyTierOverrides,
  type QyTransferGroupLimit,
} from '../group-limits-types'
import { qyTransferFieldMeta } from '../lib/fields'
import {
  qyTierDraftCoversAny,
  qyTierDraftFrom,
  qyTierDraftHasInvalid,
  qyTierDraftValue,
  qyTierOverrideOf,
  type QyTierFieldDraft,
} from '../lib/tier-draft'

/**
 * 「按用户分组覆盖门槛」卡片。
 *
 * # 这一页要回答的两个问题
 *
 *  1. **每一档现在生效的是什么** —— 而且必须逐项说清「这个数字是这一档配的,
 *     还是从全站门槛漂过来的」。少了后者,运营在全站门槛变动之后完全无法预判
 *     哪些档会跟着变。
 *  2. **勾掉一项覆盖会回到哪里** —— 全站兜底的值必须就摆在旁边,
 *     否则取消勾选这个动作等于闭眼跳。
 *
 * # 分档的键是用户分组
 *
 * 下拉来自 `user_group_options`(`users.group`),与划转分组限制页同一个命名
 * 空间。**不是模型分组** —— 那一度是本模块最隐蔽的一处错位,详见
 * `features/qy/lib/group-options.ts` 的 `QyUserGroupOption`。
 *
 * # 覆盖项是三态,不是「填了/没填」
 *
 * 每一项前面有一个独立的复选框:勾上才叫覆盖。取消勾选 = 回落全站,
 * 填 0 = 这一档把这道闸门关掉。两者方向完全相反,不能由「输入框是不是空的」
 * 来推断,理由见 `lib/tier-draft.ts`。
 */
export function QyGroupLimitsCard() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminTransferGroupLimitsQuery())
  const queryClient = useQueryClient()
  const data = query.data

  const quotaPerUnit = useSystemConfigStore(
    (state) => state.config.currency.quotaPerUnit
  )
  const scale = useMemo(() => qyUsdScale(quotaPerUnit), [quotaPerUnit])

  const [editing, setEditing] = useState<QyTransferGroupLimit | null>(null)
  const [creating, setCreating] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<string | null>(null)

  const invalidate = async () => {
    await queryClient.invalidateQueries({
      queryKey: qyKeys.adminTransferGroupLimits(),
    })
    // 分档改动会同时让用户端的 /transfer/limits 过期。
    await queryClient.invalidateQueries({ queryKey: qyKeys.all })
  }

  const deleteMutation = useMutation({
    mutationFn: qyDeleteTransferGroupLimit,
    onSuccess: async () => {
      setPendingDelete(null)
      toast.success(t('qy_tl_deleted'))
      await invalidate()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const items = data?.items ?? []
  const atCap = data != null && items.length >= data.max_tier_count

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_tl_title')}</CardTitle>
        <CardDescription>{t('qy_tl_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <Alert>
          <Info />
          <AlertTitle>{t('qy_tl_note_title')}</AlertTitle>
          <AlertDescription>{t('qy_tl_note_desc')}</AlertDescription>
        </Alert>

        {data != null && !data.global_valid && (
          <Alert variant='destructive'>
            <ShieldAlert />
            <AlertTitle>{t('qy_tl_global_invalid_title')}</AlertTitle>
            <AlertDescription>
              {t('qy_tl_global_invalid_desc')}
            </AlertDescription>
          </Alert>
        )}

        {data != null && data.unknown_groups.length > 0 && (
          <Alert>
            <ShieldAlert />
            <AlertTitle>{t('qy_tl_unknown_title')}</AlertTitle>
            <AlertDescription>
              {t('qy_tl_unknown_desc', {
                groups: data.unknown_groups.join('、'),
              })}
            </AlertDescription>
          </Alert>
        )}

        {items.length === 0 && (
          <p className='text-muted-foreground text-sm'>{t('qy_tl_empty')}</p>
        )}

        <div className='space-y-3'>
          {items.map((item) => (
            <TierRow
              key={item.user_group}
              item={item}
              scale={scale}
              onEdit={() => setEditing(item)}
              onDelete={() => setPendingDelete(item.user_group)}
            />
          ))}
        </div>

        <Button
          variant='outline'
          disabled={atCap || query.isLoading}
          onClick={() => setCreating(true)}
        >
          <Plus aria-hidden='true' />
          {t('qy_tl_add')}
        </Button>
        {atCap && (
          <p className='text-muted-foreground text-xs'>
            {t('qy_tl_at_cap', { max: data?.max_tier_count ?? 0 })}
          </p>
        )}
      </CardContent>

      {(creating || editing != null) && data != null && (
        <TierDialog
          open
          onOpenChange={(open) => {
            if (open) return
            setCreating(false)
            setEditing(null)
          }}
          item={editing}
          global={data.global}
          bounds={data.bounds}
          groupOptions={data.user_group_options}
          scale={scale}
          onSaved={async () => {
            setCreating(false)
            setEditing(null)
            await invalidate()
          }}
        />
      )}

      <QyConfirmDialog
        open={pendingDelete != null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
        title={t('qy_tl_delete_title')}
        description={t('qy_tl_delete_desc', { group: pendingDelete ?? '' })}
        confirmText={t('qy_common_delete')}
        isLoading={deleteMutation.isPending}
        onConfirm={() => {
          if (pendingDelete != null) deleteMutation.mutate(pendingDelete)
        }}
      />
    </Card>
  )
}

/** 一档的只读摘要行:七项生效值 + 每一项的来源。 */
function TierRow(props: {
  item: QyTransferGroupLimit
  scale: QyUsdScale
  onEdit: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const { item } = props

  return (
    <div className='rounded-md border p-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <span className='font-medium'>{item.user_group}</span>
        {!item.enabled && (
          <Badge variant='outline'>{t('qy_tl_row_disabled')}</Badge>
        )}
        {!item.valid && (
          <Badge variant='destructive'>{t('qy_tl_row_invalid')}</Badge>
        )}
        {item.remark !== '' && (
          <span className='text-muted-foreground text-xs'>{item.remark}</span>
        )}
        <div className='ms-auto flex gap-2'>
          <Button variant='outline' size='sm' onClick={props.onEdit}>
            {t('qy_common_edit')}
          </Button>
          <Button variant='outline' size='sm' onClick={props.onDelete}>
            <Trash2 aria-hidden='true' />
          </Button>
        </div>
      </div>

      {!item.valid && (
        <p className='text-destructive mt-2 text-xs'>
          {t('qy_tl_row_invalid_desc')}
        </p>
      )}

      <dl className='mt-2 grid gap-x-4 gap-y-1 text-xs sm:grid-cols-2'>
        {QY_TIERABLE_KEYS.map((key) => (
          <div key={key} className='flex items-center justify-between gap-2'>
            <dt className='text-muted-foreground'>
              {t(qyTransferFieldMeta(key)?.labelKey ?? key)}
            </dt>
            <dd className='flex items-center gap-1.5 tabular-nums'>
              {displayTierValue(key, item.effective[key], props.scale, t)}
              {/* 来源必须逐项标出来:少了它,运营完全无法预判全站门槛变动
                  之后哪些档会跟着变。 */}
              <Badge
                variant={item.sources[key] === 'group' ? 'default' : 'outline'}
              >
                {t(
                  item.sources[key] === 'group'
                    ? 'qy_tl_src_group'
                    : 'qy_tl_src_global'
                )}
              </Badge>
            </dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

/** 新建 / 编辑一档。 */
function TierDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `null` 表示新建。 */
  item: QyTransferGroupLimit | null
  global: Record<string, number>
  bounds: Record<string, { min: number; max: number }>
  groupOptions: QyUserGroupOption[]
  scale: QyUsdScale
  onSaved: () => Promise<void> | void
}) {
  const { t } = useTranslation()
  const { item } = props

  const [group, setGroup] = useState(item?.user_group ?? '')
  const [enabled, setEnabled] = useState(item?.enabled ?? false)
  const [remark, setRemark] = useState(item?.remark ?? '')
  const [drafts, setDrafts] = useState<Record<string, QyTierFieldDraft>>(() => {
    const next: Record<string, QyTierFieldDraft> = {}
    for (const key of QY_TIERABLE_KEYS) {
      next[key] = qyTierDraftFrom(
        item?.[key],
        props.scale,
        isUsdField(key, props.scale)
      )
    }
    return next
  })

  const values = QY_TIERABLE_KEYS.map((key) =>
    qyTierDraftValue(
      drafts[key] ?? { covered: false, text: '' },
      props.scale,
      isUsdField(key, props.scale)
    )
  )
  const outOfBounds = QY_TIERABLE_KEYS.some((key, i) => {
    const value = values[i]
    const bound = props.bounds[key]
    if (value.kind !== 'value' || bound == null) return false
    return value.quota < bound.min || value.quota > bound.max
  })
  const invalid =
    group.trim() === '' ||
    qyTierDraftHasInvalid(values) ||
    outOfBounds ||
    !qyTierDraftCoversAny(values)

  const saveMutation = useMutation({
    mutationFn: qyPutTransferGroupLimit,
    onSuccess: async () => {
      toast.success(t('qy_tl_saved'))
      await props.onSaved()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const submit = () => {
    // 七个键**全部**显式带上,没覆盖的写 null:后端是整行替换,
    // 缺席与 null 同义,而这里把它写出来是为了让请求体自己说清
    // 「这一次运营到底想覆盖哪几项」。
    const overrides: QyTierOverrides = {}
    QY_TIERABLE_KEYS.forEach((key, i) => {
      overrides[key] = qyTierOverrideOf(values[i])
    })
    saveMutation.mutate({
      user_group: group.trim(),
      enabled,
      remark,
      overrides,
    })
  }

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={item == null ? t('qy_tl_create') : t('qy_tl_edit')}
      description={t('qy_tl_form_desc')}
      contentClassName='sm:max-w-xl'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('qy_common_cancel')}
          </Button>
          <Button disabled={invalid || saveMutation.isPending} onClick={submit}>
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-1.5'>
          <Label>{t('qy_tl_field_group')}</Label>
          {/* 分组名是主键,改名等于换一档。编辑时锁死,免得运营以为自己在改名
              而实际造出了第二档、原来那档还在生效。 */}
          {item == null ? (
            <ComboboxInput
              options={props.groupOptions.map((option) => ({
                value: option.name,
                label: qyUserGroupOptionLabel(option, t),
              }))}
              value={group}
              onValueChange={setGroup}
              allowCustomValue
              emptyText='qy_tl_group_picker_empty'
              placeholder={t('qy_tl_field_group_ph')}
            />
          ) : (
            <Input value={group} readOnly />
          )}
          <p className='text-muted-foreground text-xs'>
            {t('qy_tl_field_group_desc')}
          </p>
        </div>

        <div className='flex items-center justify-between gap-3 rounded-md border p-3'>
          <div>
            <Label htmlFor='qy-tl-enabled'>{t('qy_tl_field_enabled')}</Label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_tl_field_enabled_desc')}
            </p>
          </div>
          <Switch
            id='qy-tl-enabled'
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>

        {QY_TIERABLE_KEYS.map((key) => (
          <TierField
            key={key}
            fieldKey={key}
            draft={drafts[key] ?? { covered: false, text: '' }}
            fallback={props.global[key] ?? 0}
            bound={props.bounds[key] ?? null}
            scale={props.scale}
            onChange={(next) => setDrafts((prev) => ({ ...prev, [key]: next }))}
          />
        ))}

        <div className='space-y-1.5'>
          <Label htmlFor='qy-tl-remark'>{t('qy_tl_field_remark')}</Label>
          <Input
            id='qy-tl-remark'
            value={remark}
            maxLength={255}
            onChange={(event) => setRemark(event.target.value)}
          />
        </div>

        {!qyTierDraftCoversAny(values) && (
          <p className='text-destructive text-sm'>
            {t('qy_tl_err_empty_tier')}
          </p>
        )}
      </div>
    </QyResponsiveDialog>
  )
}

/** 一项覆盖:复选框(覆不覆盖)+ 输入框(覆盖成多少)+ 兜底值旁注。 */
function TierField(props: {
  fieldKey: QyTierableKey
  draft: QyTierFieldDraft
  /** 全站兜底值(额度整数)。取消勾选之后会回到这里。 */
  fallback: number
  bound: { min: number; max: number } | null
  scale: QyUsdScale
  onChange: (next: QyTierFieldDraft) => void
}) {
  const { t } = useTranslation()
  const meta = qyTransferFieldMeta(props.fieldKey)
  const asUsd = isUsdField(props.fieldKey, props.scale)
  const value = qyTierDraftValue(props.draft, props.scale, asUsd)
  const outOfBounds =
    value.kind === 'value' &&
    props.bound != null &&
    (value.quota < props.bound.min || value.quota > props.bound.max)

  return (
    <div className='space-y-1.5'>
      <div className='flex items-center gap-2'>
        <Checkbox
          id={`qy-tl-cover-${props.fieldKey}`}
          checked={props.draft.covered}
          onCheckedChange={(checked) =>
            props.onChange({ ...props.draft, covered: checked === true })
          }
        />
        <Label htmlFor={`qy-tl-cover-${props.fieldKey}`}>
          {meta == null ? props.fieldKey : t(meta.labelKey)}
        </Label>
      </div>

      {props.draft.covered ? (
        <div className='flex items-center gap-2'>
          <Input
            inputMode={asUsd ? 'decimal' : 'numeric'}
            value={props.draft.text}
            aria-invalid={value.kind === 'invalid' || outOfBounds}
            onChange={(event) =>
              props.onChange({ ...props.draft, text: event.target.value })
            }
          />
          <span
            className='text-muted-foreground shrink-0 text-sm'
            aria-hidden='true'
          >
            {asUsd ? 'USD' : meta != null && t(`qy_tc_unit_${meta.unit}`)}
          </span>
        </div>
      ) : (
        // 不勾选时把输入框整个撤掉而不是禁用:留一个灰着的框,
        // 下一个人打开这一档时仍会以为里面的数字算数。
        <p className='text-muted-foreground text-xs'>
          {t('qy_tl_uses_global', {
            value: displayTierValue(
              props.fieldKey,
              props.fallback,
              props.scale,
              t
            ),
          })}
        </p>
      )}

      {props.draft.covered && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_tl_fallback_hint', {
            value: displayTierValue(
              props.fieldKey,
              props.fallback,
              props.scale,
              t
            ),
          })}
          {props.bound != null && (
            <>
              {' '}
              {t('qy_tc_range', {
                min: displayTierValue(
                  props.fieldKey,
                  props.bound.min,
                  props.scale,
                  t
                ),
                max: displayTierValue(
                  props.fieldKey,
                  props.bound.max,
                  props.scale,
                  t
                ),
              })}
            </>
          )}
        </p>
      )}
    </div>
  )
}

/** 该字段是否按 USD 录入。与全站门槛页同一条判据。 */
function isUsdField(key: string, scale: QyUsdScale): boolean {
  return scale.usable && qyTransferFieldMeta(key)?.unit === 'quota'
}

/**
 * 把一个额度渲染成界面上该有的样子。
 *
 * 0 一律附上「不限」二字:分档页上 0 出现得比全站页频繁得多(整档存在的理由
 * 常常就是给某一组人放开一道闸门),而一个孤零零的 0 会被读成「一分都不能转」。
 */
function displayTierValue(
  key: string,
  quota: number,
  scale: QyUsdScale,
  t: (key: string) => string
): string {
  const text = isUsdField(key, scale)
    ? qyFormatQuotaAsUsd(quota, scale)
    : String(quota)
  if (quota === 0 && qyTransferFieldMeta(key)?.zeroMeansUnlimited === true) {
    return `${text} (${t('qy_tl_unlimited')})`
  }
  return text
}
