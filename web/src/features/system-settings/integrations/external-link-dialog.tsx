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
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Dialog } from '@/components/dialog'
import { ReactIconByName } from '@/components/react-icon-by-name'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
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

import { isSafeExternalLinkUrl } from './utils'

const createExternalLinkDialogSchema = (t: (key: string) => string) =>
  z.object({
    amount: z
      .string()
      .refine(
        (value) => Number(value.trim()) > 0,
        t('Bind the link to a top-up amount greater than 0')
      ),
    name: z.string().min(1, t('Link name is required')),
    url: z
      .string()
      .refine(
        isSafeExternalLinkUrl,
        t('Enter a full URL starting with http:// or https://')
      ),
    icon: z.string().optional(),
  })

type ExternalLinkDialogFormValues = z.infer<
  ReturnType<typeof createExternalLinkDialogSchema>
>

const EXTERNAL_LINK_FORM_ID = 'topup-external-link-form'

export type ExternalLinkData = {
  amount: number
  name: string
  url: string
  icon?: string
}

type ExternalLinkDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (data: ExternalLinkData) => void
  editData?: ExternalLinkData | null
  /** The configured preset top-up amounts, offered as the picker's options. */
  amountOptions: number[]
}

export function ExternalLinkDialog(props: ExternalLinkDialogProps) {
  const { t } = useTranslation()
  const isEditMode = !!props.editData

  const form = useForm<ExternalLinkDialogFormValues>({
    resolver: zodResolver(createExternalLinkDialogSchema(t)),
    defaultValues: {
      amount: '',
      name: '',
      url: '',
      icon: '',
    },
  })

  const iconValue = form.watch('icon')

  useEffect(() => {
    form.reset({
      amount: props.editData ? String(props.editData.amount) : '',
      name: props.editData?.name ?? '',
      url: props.editData?.url ?? '',
      icon: props.editData?.icon ?? '',
    })
  }, [props.editData, form, props.open])

  const handleSubmit = (values: ExternalLinkDialogFormValues) => {
    const data: ExternalLinkData = {
      amount: Number(values.amount.trim()),
      name: values.name.trim(),
      url: values.url.trim(),
    }
    if (values.icon && values.icon.trim() !== '') {
      data.icon = values.icon.trim()
    }
    props.onSave(data)
    form.reset()
    props.onOpenChange(false)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={isEditMode ? t('Edit external link') : t('Add external link')}
      description={t(
        'Bound to one top-up amount. Users who pick that amount get this link next to the payment methods; other amounts only show the payment methods.'
      )}
      contentClassName='sm:max-w-[500px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={EXTERNAL_LINK_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={EXTERNAL_LINK_FORM_ID}
          onSubmit={form.handleSubmit(handleSubmit)}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='amount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Bound top-up amount')}</FormLabel>
                <FormControl>
                  <Combobox
                    options={props.amountOptions.map((amount) => ({
                      label: String(amount),
                      value: String(amount),
                    }))}
                    value={field.value}
                    onValueChange={(value) => field.onChange(value ?? '')}
                    placeholder={t('Select a preset amount')}
                    searchPlaceholder={t('Search amounts...')}
                    allowCustomValue
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Pick one of the preset top-up amounts. An amount outside that list only surfaces when a user types it as a custom amount.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('e.g., Buy a $50 redemption code')}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t('Button label shown to users on the wallet page.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='url'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Target URL')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder='https://shop.example.com/item/50'
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Point it straight at the redemption code amount you want users to land on.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='icon'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Icon')}</FormLabel>
                <FormControl>
                  <div className='flex items-center gap-2'>
                    <Input
                      placeholder={t('e.g., LuExternalLink')}
                      {...field}
                      className='flex-1'
                    />
                    {iconValue && (
                      <ReactIconByName
                        name={iconValue}
                        className='text-muted-foreground size-5 shrink-0'
                        title={iconValue}
                      />
                    )}
                  </div>
                </FormControl>
                <FormDescription>
                  {t(
                    'Enter a react-icons component name. Invalid names show no icon.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
