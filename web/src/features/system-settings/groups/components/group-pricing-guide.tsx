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
import { HelpCircle } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

/**
 * 「一次调用是怎么被定价的」导览。
 *
 * ── 为什么这份导览要在三页上都能打开 ──
 *
 * 拆页之后，一次调用的价格由**三页各出一半**决定：模型分组给兜底倍率、
 * 用户分组给充值折扣、两者的交叉格给覆盖倍率。把导览只留在其中一页，
 * 运营在另外两页上就只能看到自己那一格，而算错价恰恰发生在跨页的地方
 * （最典型的一条：把「用户分组的兜底倍率」当成个人折扣）。
 *
 * 正文原样搬自被拆掉的 `models/group-ratio-form.tsx`，逐句沿用它原有的
 * 「英文原文即键名」文案 —— 那些键在 7 个 locale 里都已存在，搬家不该顺手
 * 制造一批只有中英文的新键。只有两处**指路**的句子换成了 qy 键：
 * 它们原本指向「用户分组」这一页，而交叉倍率现在在另一页上。
 */
export function GroupPricingGuideButton() {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button variant='outline' size='sm' onClick={() => setOpen(true)}>
        <HelpCircle className='mr-2 h-4 w-4' />
        {t('Usage guide')}
      </Button>
      <GroupPricingGuide open={open} onOpenChange={setOpen} />
    </>
  )
}

function GuideCodeBlock({ children }: { children: string }) {
  return (
    <pre className='bg-muted/60 overflow-x-auto rounded-lg border px-3 py-2 text-xs leading-6 whitespace-pre-wrap'>
      {children}
    </pre>
  )
}

function GuideStepRow({
  chip,
  children,
}: {
  chip: string
  children: ReactNode
}) {
  return (
    <div className='flex items-start gap-2.5 text-sm leading-6'>
      <span className='bg-muted text-muted-foreground mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-xs font-medium'>
        {chip}
      </span>
      <span className='text-muted-foreground min-w-0'>{children}</span>
    </div>
  )
}

type GroupPricingGuideProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

function GroupPricingGuide({ open, onOpenChange }: GroupPricingGuideProps) {
  const { t } = useTranslation()

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side='right'
        className={sideDrawerContentClassName('sm:max-w-2xl')}
      >
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Group pricing usage guide')}</SheetTitle>
          <SheetDescription>
            {t(
              'Understand how user groups, token groups, ratios, and special rules work together.'
            )}
          </SheetDescription>
        </SheetHeader>

        <div className={sideDrawerFormClassName('gap-5')}>
          <section className='space-y-2'>
            <h3 className='text-sm font-semibold'>
              {t('The two roles of a group')}
            </h3>
            <div className='text-muted-foreground space-y-2 text-sm leading-6'>
              <p>{t('qy_gs_two_roles_desc')}</p>
              <p>
                <span className='text-foreground font-medium'>
                  {t('Token group')}
                </span>
                {': '}
                {t(
                  'decides which channels are used and which base ratio applies.'
                )}
              </p>
              <p>
                <span className='text-foreground font-medium'>
                  {t('User group')}
                </span>
                {': '}
                {t(
                  'decides the top-up ratio, which groups the user can pick for tokens, and whether an override ratio applies.'
                )}
              </p>
            </div>
          </section>

          <section className='space-y-2'>
            <h3 className='text-sm font-semibold'>
              {t('How a call is priced')}
            </h3>
            <ol className='text-muted-foreground list-decimal space-y-2 pl-5 text-sm leading-6'>
              <li>
                <span className='text-foreground font-medium'>
                  {t('Find the billing group.')}
                </span>{' '}
                {t(
                  'Use the group set on the token. If the token has no group, use the user group. The auto group tries the auto assignment order from top to bottom.'
                )}
              </li>
              <li>
                <span className='text-foreground font-medium'>
                  {t('Find the ratio.')}
                </span>{' '}
                {t(
                  'Look for a special ratio rule matching this user group and this billing group. If one exists, use its ratio. Otherwise use the billing group base ratio from the pricing table.'
                )}
              </li>
              <li>
                <span className='text-foreground font-medium'>
                  {t('Charge.')}
                </span>{' '}
                {t(
                  'Cost = model price × that one ratio. Nothing else from the group settings enters the formula.'
                )}
              </li>
            </ol>
            <p className='text-muted-foreground text-sm leading-6'>
              {t(
                'Common pitfall: the user group base ratio is NOT a personal discount. It only applies when the user group itself is the billing group.'
              )}
            </p>
          </section>

          <section className='space-y-3'>
            <h3 className='text-sm font-semibold'>{t('Worked example')}</h3>
            <p className='text-muted-foreground text-sm leading-6'>
              {t(
                'The admin configured three groups and one special ratio rule:'
              )}
            </p>

            <div className='overflow-hidden rounded-lg border'>
              <div className='bg-muted/40 border-b px-3 py-1.5 text-xs font-medium'>
                {t('Pricing groups')}
              </div>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='text-muted-foreground border-b text-xs'>
                    <th className='px-3 py-1.5 text-left font-medium'>
                      {t('Group name')}
                    </th>
                    <th className='px-3 py-1.5 text-right font-medium'>
                      {t('Ratio')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <tr className='border-b'>
                    <td className='px-3 py-1.5'>default</td>
                    <td className='px-3 py-1.5 text-right'>1.0</td>
                  </tr>
                  <tr className='border-b'>
                    <td className='px-3 py-1.5'>premium</td>
                    <td className='px-3 py-1.5 text-right'>0.5</td>
                  </tr>
                  <tr>
                    <td className='px-3 py-1.5'>vip</td>
                    <td className='px-3 py-1.5 text-right'>0.8</td>
                  </tr>
                </tbody>
              </table>
            </div>

            <div className='overflow-hidden rounded-lg border'>
              <div className='bg-muted/40 border-b px-3 py-1.5 text-xs font-medium'>
                {t('Special ratio rules')}
              </div>
              <div className='p-3 text-sm leading-6'>
                {t('Users of vip, when billed as premium, pay ratio')}{' '}
                <span className='bg-primary/10 ring-primary/40 rounded px-1.5 py-0.5 font-semibold ring-1'>
                  0.3
                </span>{' '}
                <span className='text-muted-foreground text-xs'>
                  {t('(instead of {{ratio}})', { ratio: 0.5 })}
                </span>
                {/*
                  例子里这条规则仍然真实生效，但它已经不在这一页上编辑了。
                  少了这一句，读完例子的人会在本页翻找一个不存在的输入框。
                */}
                <p className='text-muted-foreground mt-2 text-xs leading-5'>
                  {t('qy_gs_where_cross_ratio')}
                </p>
              </div>
            </div>

            <p className='text-muted-foreground text-sm leading-6'>
              {t(
                'Three calls made by the same vip user. Assume the base price of one call is 10.'
              )}
            </p>

            <div className='space-y-3'>
              <div className='overflow-hidden rounded-lg border'>
                <div className='bg-muted/40 border-b px-3 py-2 text-sm font-medium'>
                  {t('Call 1: the token group is premium')}
                </div>
                <div className='space-y-2 p-3'>
                  <GuideStepRow chip='1'>
                    {t(
                      'Billing group = premium (the token has a group, so use it)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='2'>
                    {t(
                      'There is a rule for vip billed as premium → use its ratio 0.3'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='='>
                    <span className='text-foreground font-medium'>
                      {t('Cost = 10 × 0.3 = 3')}
                    </span>
                  </GuideStepRow>
                </div>
              </div>

              <div className='overflow-hidden rounded-lg border'>
                <div className='bg-muted/40 border-b px-3 py-2 text-sm font-medium'>
                  {t('Call 2: the token group is default')}
                </div>
                <div className='space-y-2 p-3'>
                  <GuideStepRow chip='1'>
                    {t(
                      'Billing group = default (the token has a group, so use it)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='2'>
                    {t(
                      'No rule for vip billed as default → use the base ratio of default, 1.0 (the 0.8 of vip is not used)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='='>
                    <span className='text-foreground font-medium'>
                      {t('Cost = 10 × 1.0 = 10')}
                    </span>
                  </GuideStepRow>
                </div>
              </div>

              <div className='overflow-hidden rounded-lg border'>
                <div className='bg-muted/40 border-b px-3 py-2 text-sm font-medium'>
                  {t('Call 3: the token has no group')}
                </div>
                <div className='space-y-2 p-3'>
                  <GuideStepRow chip='1'>
                    {t(
                      'Billing group = vip (the token has no group, so use the user group)'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='2'>
                    {t(
                      'No rule for vip billed as vip → use the base ratio of vip, 0.8'
                    )}
                  </GuideStepRow>
                  <GuideStepRow chip='='>
                    <span className='text-foreground font-medium'>
                      {t('Cost = 10 × 0.8 = 8')}
                    </span>
                  </GuideStepRow>
                </div>
              </div>
            </div>
          </section>

          {/*
            拆页之后「去哪儿改」本身就是这份导览最常被翻开的理由，所以它从
            折叠面板里提到正文末尾，三条一次说完。
          */}
          <section className='space-y-2'>
            <h3 className='text-sm font-semibold'>{t('qy_gs_where_title')}</h3>
            <ul className='text-muted-foreground list-disc space-y-1 pl-5 text-sm leading-6'>
              <li>{t('qy_gs_where_base_ratio')}</li>
              <li>{t('qy_gs_where_topup')}</li>
              <li>{t('qy_gs_where_cross_ratio')}</li>
              {/*
                全局「用户可选分组」清单是唯一搬去第三页的上游 option，漏掉它的
                表现是：运营在「模型分组」页看到那一列以只读形式出现，最可能的
                结论是「在这一页改，只是变灰了」，然后来问为什么不能编辑。
              */}
              <li>{t('qy_gs_where_usable_list')}</li>
            </ul>
            <GuideCodeBlock>{`["default", "vip"]`}</GuideCodeBlock>
            <p className='text-muted-foreground text-sm leading-6'>
              {t(
                'When a token uses the auto group, the system tries groups from top to bottom until it finds an available group.'
              )}
            </p>
          </section>
        </div>
      </SheetContent>
    </Sheet>
  )
}
