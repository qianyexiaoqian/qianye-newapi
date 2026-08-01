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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { KeyRound, Lock, TriangleAlert } from 'lucide-react'
import { useEffect, useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { formatTimestampToDate } from '@/lib/format'

import {
  qyPayPasswordStatusQuery,
  type QyPayPasswordStatus,
} from '../lib/pay-password'

type QyPayPasswordFieldProps = {
  value: string
  onChange: (value: string) => void
  /** 外部禁用（例如整个表单被降级禁用）。 */
  disabled?: boolean
  /**
   * 本格是否处于"提交一定失败"的状态（未设置 / 已锁定 / 状态读不到）。
   *
   * 调用方据此禁用提交按钮。**这不是权限判断** —— 真正说了算的是后端的
   * `paypass.Require`，这里只是不让用户白按一次。
   */
  onBlockedChange?: (blocked: boolean) => void
}

/**
 * 划转执行入口的支付密码输入格。
 *
 * # 它为什么长这样
 *
 * 支付密码有三种"输入框本身没意义"的状态，各自要引导用户去做完全不同的事：
 *   - **未设置** → 去设置（首次使用强制设置，后端返回 `qy_pay_pwd_not_set`）
 *   - **已锁定** → 等到期，或走邮箱找回
 *   - **状态读不到** → 扩展库不可用，提交必定 503
 * 把这三种都塞进一个 placeholder 是行不通的，所以这一格会整体切换形态。
 *
 * # 它**不**做的事
 *
 * 不校验密码、不做本地"是不是联系人"的判断、不提供任何跳过入口。
 * 裁决 1：联系人只是方便快速填写表，不构成任何豁免。这个组件的 props 里
 * 没有收款人，也没有联系人 —— 它压根不知道钱要转给谁，也就没有能力分流。
 */
export function QyPayPasswordField(props: QyPayPasswordFieldProps) {
  const { t } = useTranslation()
  const inputId = useId()
  const statusQuery = useQuery(qyPayPasswordStatusQuery())

  const status = statusQuery.data
  const blocked = status == null || !status.is_set || status.locked

  // 状态一变就把结论同步给调用方。写在 effect 里而不是渲染期直接调，
  // 是因为 onBlockedChange 通常会 setState，渲染期调用会触发 React 警告。
  const notify = props.onBlockedChange
  useEffect(() => {
    notify?.(blocked)
  }, [blocked, notify])

  if (statusQuery.isPending) {
    return (
      <p className='text-muted-foreground text-sm'>
        {t('qy_pp_status_loading')}
      </p>
    )
  }

  if (status == null) {
    return (
      <Alert variant='destructive'>
        <TriangleAlert />
        <AlertTitle>{t('qy_pp_status_unavailable')}</AlertTitle>
        <AlertDescription>
          {t('qy_pp_status_unavailable_desc')}
        </AlertDescription>
      </Alert>
    )
  }

  if (!status.is_set) {
    return <SetupPrompt />
  }

  if (status.locked) {
    return <LockedPrompt status={status} />
  }

  return (
    <div className='space-y-2'>
      <Label htmlFor={inputId}>{t('qy_pp_field_label')}</Label>
      <Input
        id={inputId}
        type='password'
        // one-time-code 而不是 current-password：让密码管理器别把支付密码
        // 当成登录密码存进去，两者不是同一个凭据。
        autoComplete='one-time-code'
        inputMode='text'
        value={props.value}
        onChange={(event) => props.onChange(event.target.value)}
        disabled={props.disabled}
        placeholder={t('qy_pp_field_ph')}
      />
      <p className='text-muted-foreground text-xs'>
        {t('qy_pp_field_help', {
          remaining: status.remaining_attempts,
          minutes: status.lock_minutes,
        })}
      </p>
    </div>
  )
}

/** 未设置：把用户送去设置页，而不是让他对着一个不存在的密码猜。 */
function SetupPrompt() {
  const { t } = useTranslation()
  return (
    <Alert>
      <KeyRound />
      <AlertTitle>{t('qy_pp_setup_required')}</AlertTitle>
      <AlertDescription className='space-y-2'>
        <span>{t('qy_pp_setup_required_desc')}</span>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/pay-password' />}
        >
          {t('qy_pp_go_setup')}
        </Button>
      </AlertDescription>
    </Alert>
  )
}

/** 已锁定：给出解锁时间与找回入口，别让用户继续试。 */
function LockedPrompt(props: { status: QyPayPasswordStatus }) {
  const { t } = useTranslation()
  return (
    <Alert variant='destructive'>
      <Lock />
      <AlertTitle>{t('qy_pp_locked_title')}</AlertTitle>
      <AlertDescription className='space-y-2'>
        <span>
          {t('qy_pp_locked_desc', {
            time: formatTimestampToDate(props.status.locked_until),
          })}
        </span>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/pay-password' />}
        >
          {t('qy_pp_go_recover')}
        </Button>
      </AlertDescription>
    </Alert>
  )
}
