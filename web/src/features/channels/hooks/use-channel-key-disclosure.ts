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
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useSecureVerification } from '@/features/auth/secure-verification'

import { getChannelKey } from '../api'

/**
 * 渠道编辑器「查看密钥」的二次验证流程。
 *
 * 上游这个 hook 建在 45c3fbe8a(验证凭据绑定会话与动作)那套新的
 * requestVerification 上,本仓没有合那次安全重构,仍是 withVerification +
 * proof token。这里保持与上游相同的返回形状(channelKey / isChannelKeyLoading /
 * handleRevealKey / verification.isActive / verification.dialogProps),
 * 内部沿用本仓旧编辑器的流程,让新编辑器原样使用。
 */
export function useChannelKeyDisclosure(
  open: boolean,
  channelId: number | null
) {
  const { t } = useTranslation()
  const {
    open: verificationOpen,
    methods,
    state,
    executeVerification,
    withVerification,
    cancel,
    setCode,
    switchMethod,
  } = useSecureVerification()
  const [disclosedKey, setDisclosedKey] = useState<{
    channelId: number
    key: string
  } | null>(null)
  const [isChannelKeyLoading, setIsChannelKeyLoading] = useState(false)

  useEffect(() => {
    setDisclosedKey(null)
    setIsChannelKeyLoading(false)
  }, [open, channelId])

  const fetchChannelKey = useCallback(
    async (proofToken?: string) => {
      if (!channelId) {
        throw new Error('Channel is not selected')
      }
      setIsChannelKeyLoading(true)
      try {
        const res = await getChannelKey(channelId, proofToken)
        if (!res.success) {
          throw new Error(res.message || t('Failed to fetch channel key'))
        }
        setDisclosedKey({ channelId, key: res.data?.key ?? '' })
        toast.success(t('Channel key unlocked'))
        return res
      } finally {
        setIsChannelKeyLoading(false)
      }
    },
    [channelId, t]
  )

  const handleRevealKey = useCallback(async () => {
    if (!channelId || !open) return
    try {
      await withVerification(fetchChannelKey, {
        scope: 'channel.key.read',
        preferredMethod: 'passkey',
        title: t('Verify to view channel key'),
        description: t(
          'Use Passkey or 2FA to confirm your identity before revealing this channel key.'
        ),
      })
    } catch (error) {
      if (error instanceof Error) {
        toast.error(error.message)
      }
    }
  }, [channelId, open, withVerification, fetchChannelKey, t])

  const channelKey =
    open && disclosedKey?.channelId === channelId ? disclosedKey.key : null
  return {
    channelKey,
    isChannelKeyLoading,
    handleRevealKey,
    verification: {
      isActive: verificationOpen,
      dialogProps: {
        open: verificationOpen,
        onOpenChange: (next: boolean) => {
          if (!next) cancel()
        },
        methods,
        state,
        onVerify: async (
          method: Parameters<typeof executeVerification>[0],
          code?: string
        ) => {
          await executeVerification(method, code)
        },
        onCancel: cancel,
        onCodeChange: setCode,
        onMethodChange: switchMethod,
      },
    },
  }
}
