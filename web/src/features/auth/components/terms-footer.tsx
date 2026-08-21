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
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import type { SystemStatus } from '../types'

interface TermsFooterProps {
  variant?: 'sign-in' | 'sign-up'
  className?: string
  status?: SystemStatus | null
}

export function TermsFooter({
  variant = 'sign-in',
  className,
  status,
}: TermsFooterProps) {
  const { t } = useTranslation()
  // 这三段原本是硬编码英文，而同一段里的 t('and') 走了 i18n —— 中文界面上
  // 因此出现"我已阅读并同意 用户协议。"紧跟一句英文协议行。登录/注册页是全站
  // 唯一一个匿名用户必经的页面，6 个非英文语种全都看得见。
  const text =
    variant === 'sign-in'
      ? t('By clicking sign in, you agree to our')
      : t('By creating an account, you agree to our')

  const hasUserAgreement = Boolean(status?.user_agreement_enabled)
  const hasPrivacyPolicy = Boolean(status?.privacy_policy_enabled)

  if (!hasUserAgreement && !hasPrivacyPolicy) {
    return null
  }

  const agreementLink = {
    label: t('User Agreement'),
    href: '/user-agreement',
  }
  const privacyLink = {
    label: t('Privacy Policy'),
    href: '/privacy-policy',
  }

  const activeLinks =
    hasUserAgreement || hasPrivacyPolicy
      ? ([
          hasUserAgreement ? agreementLink : null,
          hasPrivacyPolicy ? privacyLink : null,
        ].filter(Boolean) as Array<{ label: string; href: string }>)
      : [agreementLink, privacyLink]

  const [firstLink, secondLink] = activeLinks

  return (
    <p className={cn('text-muted-foreground text-center text-xs', className)}>
      {text}{' '}
      {firstLink && (
        <a
          href={firstLink.href}
          className='hover:text-primary underline underline-offset-4'
        >
          {firstLink.label}
        </a>
      )}
      {secondLink && (
        <>
          {' '}
          {t('and')}{' '}
          <a
            href={secondLink.href}
            className='hover:text-primary underline underline-offset-4'
          >
            {secondLink.label}
          </a>
        </>
      )}
      .
    </p>
  )
}
