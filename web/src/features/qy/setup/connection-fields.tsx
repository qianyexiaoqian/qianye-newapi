import type { UseFormReturn } from 'react-hook-form'
/*
扩展库连接表单的字段与探测结果展示。

从 database-step.tsx 拆出来只为一件事:那个文件是**流程**(自动登录、测试、
写盘、转到重启屏),这里是**表单**。两者混在一起时单文件 350 行,而改一个
输入框的人不该先读完一整套会话与死循环的推导。
*/
import { useTranslation } from 'react-i18next'

import { PasswordInput } from '@/components/password-input'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import type { QyProbeReport } from './api'
import type { QyDatabaseFormValues } from './lib/schema'

export function ConnectionFields(props: {
  form: UseFormReturn<QyDatabaseFormValues>
}) {
  const { t } = useTranslation()
  const form = props.form
  return (
    <Form {...form}>
      {/* 这里刻意是 div 而不是 form:向导外层(setup-wizard.tsx)已经包了一个
          <form>,再嵌一个是非法 HTML,React 会报 hydration 错误。字段靠上面的
          <Form> provider 拿到 RHF 上下文,不需要自己是个表单元素;提交也不走
          submit —— 按钮各自调 handleTest / handleApply。 */}
      <div className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={form.control}
          name='host'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('qy_setup_f_host')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder='127.0.0.1' autoComplete='off' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='port'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('qy_setup_f_port')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder='3306' autoComplete='off' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='database'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('qy_setup_f_database')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder='qianye' autoComplete='off' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='user'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('qy_setup_f_user')}</FormLabel>
              <FormControl>
                <Input {...field} autoComplete='off' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='password'
          render={({ field }) => (
            <FormItem className='sm:col-span-2'>
              <FormLabel>{t('qy_setup_f_password')}</FormLabel>
              <FormControl>
                <PasswordInput {...field} autoComplete='off' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>
    </Form>
  )
}

export function ProbeSummary(props: { report: QyProbeReport }) {
  const { t } = useTranslation()
  return (
    <Alert
      role='status'
      className='border-emerald-200 bg-emerald-50 dark:border-emerald-900/60 dark:bg-emerald-950/40'
    >
      <AlertTitle>{t('qy_setup_probe_ok')}</AlertTitle>
      <AlertDescription>
        <p className='font-mono text-xs'>
          MySQL {props.report.version} · {props.report.charset}
        </p>
        {!props.report.empty && (
          <p className='mt-2 text-xs'>{t('qy_setup_probe_not_empty')}</p>
        )}
      </AlertDescription>
    </Alert>
  )
}
