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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Mail, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import { isQyError, qyErrorMessage } from '../../../lib/api'
import {
  qyResetPayPasswordByEmail,
  qySendPayPasswordRecoverCode,
} from '../../../lib/pay-password'
import { qyKeys } from '../../../lib/query-keys'
import { qyPayPasswordSchema } from '../lib/schema'

/**
 * 邮箱找回支付密码。
 *
 * # 这里刻意**没有**邮箱输入框
 *
 * 裁决 1 的红线：未绑定邮箱时只提示用户去绑定，绝不在这条路径上代为绑定。
 * 界面上给一个"填个邮箱来接收验证码"的输入框，就是在实现那条被禁止的路径 ——
 * 拿到会话的攻击者填自己的邮箱、收码、改掉支付密码，全套保护当场归零。
 *
 * 所以验证码只会发到**服务端记录的**绑定邮箱，前端连它是什么都不知道，
 * 只从响应里拿到一个脱敏串用来告诉用户"去哪个信箱找"。
 */
export function PayPasswordRecoverCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [maskedEmail, setMaskedEmail] = useState<string | null>(null)
  const [emailUnbound, setEmailUnbound] = useState(false)

  const sendMutation = useMutation({
    mutationFn: qySendPayPasswordRecoverCode,
    onSuccess: (data) => {
      setEmailUnbound(false)
      setMaskedEmail(data.email_masked)
      toast.success(t('qy_pp_recover_sent'))
    },
    onError: (error) => {
      setEmailUnbound(
        isQyError(error) && error.code === 'qy_pay_pwd_email_unbound'
      )
      toast.error(qyErrorMessage(error, t))
    },
  })

  const schema = z.object({
    code: z.string().trim().min(1, 'qy_pp_err_code_required'),
    password: qyPayPasswordSchema,
  })
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { code: '', password: '' },
  })

  const resetMutation = useMutation({
    mutationFn: qyResetPayPasswordByEmail,
    onSuccess: async () => {
      toast.success(t('qy_pp_recover_ok'))
      form.reset({ code: '', password: '' })
      setMaskedEmail(null)
      // 找回顺带解锁，状态整体变了。
      await queryClient.invalidateQueries({ queryKey: qyKeys.payPassword() })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_pp_recover_title')}</CardTitle>
        <CardDescription>{t('qy_pp_recover_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        {emailUnbound && (
          <Alert variant='destructive'>
            <TriangleAlert />
            <AlertTitle>{t('qy_pp_recover_unbound_title')}</AlertTitle>
            <AlertDescription>
              {t('qy_pp_recover_unbound_desc')}
            </AlertDescription>
          </Alert>
        )}

        <div className='flex flex-wrap items-center gap-3'>
          <Button
            variant='outline'
            onClick={() => sendMutation.mutate()}
            disabled={sendMutation.isPending}
          >
            <Mail aria-hidden='true' />
            {t('qy_pp_recover_send')}
          </Button>
          {maskedEmail != null && (
            <span className='text-muted-foreground text-sm'>
              {t('qy_pp_recover_sent_to', { email: maskedEmail })}
            </span>
          )}
        </div>

        <Form {...form}>
          <form
            className='space-y-4'
            onSubmit={form.handleSubmit((data) => resetMutation.mutate(data))}
          >
            <FormField
              control={form.control}
              name='code'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_pp_recover_code_label')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      autoComplete='one-time-code'
                      inputMode='text'
                      placeholder={t('qy_pp_recover_code_ph')}
                      disabled={resetMutation.isPending}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='password'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_pp_new_label')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='password'
                      autoComplete='new-password'
                      disabled={resetMutation.isPending}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <Button type='submit' disabled={resetMutation.isPending}>
              {t('qy_pp_recover_submit')}
            </Button>
          </form>
        </Form>
      </CardContent>
    </Card>
  )
}
