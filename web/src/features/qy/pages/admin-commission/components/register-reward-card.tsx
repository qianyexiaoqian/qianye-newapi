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
import { ShieldAlert } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import {
  qyAdminStardustConfigQuery,
  updateQyStardustConfig,
} from '../../admin-stardust-config/api'
import {
  qySdEffectiveValue,
  qySdParseDraft,
} from '../../admin-stardust-config/lib/draft'
import { qySdAdminErrorMessage } from '../../admin-stardust-config/lib/errors'
import type { QyStardustAdminConfig } from '../../admin-stardust-config/types'

/** 星屑运营参数里的注册奖键。 */
export const QY_CM_REGISTER_KEY = 'invite_register_stardust'

/**
 * 「下线注册奖」—— 每邀请一个人注册，一次性返给上线多少星屑。
 *
 * 它是给**上线**的返利，与下线充值 / 消费 / 兑换码 / 买套餐的佣金放在同一页，
 * 运营才不用在两页之间找同一件事。键仍存在星屑的运营参数里（`invite_register_stardust`，
 * 发放走星屑账本），所以读写都走星屑配置接口；星屑配置页不再渲染这一格。
 *
 * 合规门：支付合规声明未确认时后端对正值 400、生效值按 0 —— 保存之前就说出来，
 * 并禁用输入框。
 */
export function QyCmRegisterRewardCard() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminStardustConfigQuery())
  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_cm_reg_title')}</CardTitle>
        <CardDescription>{t('qy_cm_reg_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        <QyPageBoundary query={query}>
          {query.data != null && <RegisterRewardEditor config={query.data} />}
        </QyPageBoundary>
      </CardContent>
    </Card>
  )
}

function RegisterRewardEditor(props: { config: QyStardustAdminConfig }) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const id = useId()
  const queryClient = useQueryClient()
  const { config } = props
  const current = String(qySdEffectiveValue(config, QY_CM_REGISTER_KEY))
  const [draft, setDraft] = useState(current)
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别人改过之后重新取到）时重置草稿。
  useEffect(() => setDraft(current), [current])

  const save = useMutation({
    mutationFn: (value: number) =>
      updateQyStardustConfig({ [QY_CM_REGISTER_KEY]: value }),
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_cm_saved'))
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminStardustConfig(),
      })
    },
    onError: (error) => toast.error(qySdAdminErrorMessage(error, t)),
  })

  const compliance = config.yaml_readonly.compliance_confirmed
  const parsed = qySdParseDraft(config, QY_CM_REGISTER_KEY, draft)
  const next = typeof parsed === 'number' ? parsed : null
  const bound = config.bounds[QY_CM_REGISTER_KEY] ?? null
  const changed = next != null && String(next) !== current

  return (
    <div className='space-y-3'>
      {!compliance && (
        <Alert variant='destructive'>
          <ShieldAlert />
          <AlertTitle>{t('qy_sdadm_cfg_compliance_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_sdadm_cfg_compliance_desc')}
          </AlertDescription>
        </Alert>
      )}
      <div className='space-y-1.5'>
        <Label htmlFor={id}>
          {t('qy_sdadm_cfg_k_invite_register_stardust')}
        </Label>
        <div className='flex items-center gap-2'>
          <Input
            id={id}
            inputMode='numeric'
            value={draft}
            disabled={!compliance}
            aria-invalid={next == null}
            onChange={(event) =>
              setDraft(event.target.value.replaceAll(/\D/g, ''))
            }
          />
          <span
            className='text-muted-foreground shrink-0 text-sm'
            aria-hidden='true'
          >
            {unit}
          </span>
        </div>
        <div className='text-muted-foreground space-y-0.5 text-xs'>
          <p>{t('qy_sdadm_cfg_h_invite_register_stardust')}</p>
          {bound != null && (
            <p className={next == null ? 'text-destructive' : undefined}>
              {t('qy_common_amount_range', {
                min: formatSdWithUnit(bound.lo, unit),
                max: formatSdWithUnit(bound.hi, unit),
              })}
            </p>
          )}
          {config.overrides[QY_CM_REGISTER_KEY] != null && (
            <p>{t('qy_cm_overridden')}</p>
          )}
        </div>
      </div>
      <Button
        disabled={!compliance || !changed || save.isPending}
        onClick={() => setConfirmOpen(true)}
      >
        {t('qy_cm_save')}
      </Button>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_cm_confirm_title')}
        description={t('qy_cm_confirm_desc')}
        confirmText={t('qy_cm_save')}
        isLoading={save.isPending}
        details={
          <p className='flex justify-between gap-3 text-sm'>
            <span className='text-muted-foreground'>
              {t('qy_sdadm_cfg_k_invite_register_stardust')}
            </span>
            <span className='tabular-nums'>
              {formatSdWithUnit(Number(current), unit)} →{' '}
              <strong>{formatSdWithUnit(next ?? 0, unit)}</strong>
            </span>
          </p>
        }
        onConfirm={() => {
          if (next != null) save.mutate(next)
        }}
      />
    </div>
  )
}
