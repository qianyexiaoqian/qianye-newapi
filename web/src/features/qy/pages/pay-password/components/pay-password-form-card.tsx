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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

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
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import { qyErrorMessage } from '../../../lib/api'
import {
  QY_PAY_PWD_MAX_BYTES,
  QY_PAY_PWD_MIN_BYTES,
  qyChangePayPassword,
  qySetPayPassword,
} from '../../../lib/pay-password'
import { qyKeys } from '../../../lib/query-keys'
import { qyPayPasswordSchema } from '../lib/schema'

type PayPasswordFormCardProps = {
  /** 已设置时渲染"修改"（要求旧密码），未设置时渲染"首次设置"。 */
  isSet: boolean
  /** 已锁定时禁用整张卡：改密要先验旧密码，锁定期内必然失败。 */
  locked: boolean
}

/**
 * 设置 / 修改支付密码。
 *
 * 两种形态合成一张卡而不是两个组件：它们的字段只差一个"原支付密码"，
 * 而校验规则、成功后的收尾、错误分流完全相同。拆成两份的话，下一次给强度
 * 规则加一条时必然只改到其中一份 —— 那正是本仓反复出现的拷贝漂移。
 *
 * 「修改」要求旧密码是硬要求:不验旧密码就能改，等于拿到会话就能改支付密码，
 * 支付密码相对登录密码的额外保护就归零了。
 */
export function PayPasswordFormCard(props: PayPasswordFormCardProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const schema = z
    .object({
      old_password: z.string(),
      password: qyPayPasswordSchema,
      confirm: z.string(),
    })
    .refine((v) => v.password === v.confirm, {
      path: ['confirm'],
      message: 'qy_pp_err_confirm_mismatch',
    })
    .refine((v) => !props.isSet || v.old_password.length > 0, {
      path: ['old_password'],
      message: 'qy_pp_err_old_required',
    })
    // 新旧相同时后端会拒（qy_pay_pwd_same_as_old），这里提前挡掉少一次往返。
    .refine((v) => !props.isSet || v.password !== v.old_password, {
      path: ['password'],
      message: 'qy_pp_err_same_as_old',
    })

  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { old_password: '', password: '', confirm: '' },
  })

  const mutation = useMutation({
    mutationFn: (data: z.infer<typeof schema>) =>
      props.isSet
        ? qyChangePayPassword({
            old_password: data.old_password,
            password: data.password,
          })
        : qySetPayPassword({ password: data.password }),
    onSuccess: async () => {
      toast.success(t(props.isSet ? 'qy_pp_change_ok' : 'qy_pp_set_ok'))
      form.reset({ old_password: '', password: '', confirm: '' })
      // 状态里的 is_set / fail_count / locked 都变了，必须回服务端取真值。
      await queryClient.invalidateQueries({ queryKey: qyKeys.payPassword() })
    },
    onError: async (error) => {
      toast.error(qyErrorMessage(error, t))
      // 输错旧密码会让错误计数 +1，剩余次数与锁定状态都得重取 ——
      // 不重取的话用户会在"还剩 2 次"的提示下被直接锁死。
      await queryClient.invalidateQueries({ queryKey: qyKeys.payPassword() })
    },
  })

  const disabled = props.locked || mutation.isPending

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>
          {t(props.isSet ? 'qy_pp_change_title' : 'qy_pp_set_title')}
        </CardTitle>
        <CardDescription>
          {t(props.isSet ? 'qy_pp_change_desc' : 'qy_pp_set_desc')}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Form {...form}>
          <form
            className='space-y-4'
            onSubmit={form.handleSubmit((data) => mutation.mutate(data))}
          >
            {props.isSet && (
              <FormField
                control={form.control}
                name='old_password'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_pp_old_label')}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        type='password'
                        autoComplete='one-time-code'
                        disabled={disabled}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

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
                      disabled={disabled}
                    />
                  </FormControl>
                  <FormDescription className='text-xs'>
                    {t('qy_pp_strength_help', {
                      min: QY_PAY_PWD_MIN_BYTES,
                      max: QY_PAY_PWD_MAX_BYTES,
                    })}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='confirm'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_pp_confirm_label')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='password'
                      autoComplete='new-password'
                      disabled={disabled}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <Button type='submit' disabled={disabled}>
              {t(props.isSet ? 'qy_pp_change_submit' : 'qy_pp_set_submit')}
            </Button>
          </form>
        </Form>
      </CardContent>
    </Card>
  )
}
