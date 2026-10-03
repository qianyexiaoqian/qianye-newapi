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
import { ArrowUpRight } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import {
  sanitizeQyGroupJoinUrl,
  sanitizeQyGroupNumber,
} from '@/features/qy/lib/group-contact'

/**
 * 首屏右侧的群聊二维码卡，占位与模型信号板同一格。
 *
 * 后台「系统设置 → 站点 → 系统信息」里配了入群链接或群号之后才出现；两项皆空
 * （或值被判非法）时首屏渲染的是 `<QyLandingSignalBoard />`，判据在
 * `readQyGroupContact`。
 *
 * ── 三种形态 ──
 *
 *   只配链接：二维码 + 加群按钮，不画群号那一格。
 *   只配群号：**绝不出现二维码** —— 拿群号去编一张码，扫出来是一串无意义纯文本，
 *             比没有更糟；此时群号放大独撑一张卡，脚注也跟着换。
 *   两项都配：完整形态。
 *
 * 脚注必须跟着形态换，这条纪律与信号板的两句 caption 同源：说明文字说的是
 * 「怎么加入」，形态变了却不换，它就成了一句不会报错的假话。
 *
 * 组件内部再净化一次，是为了即使被别处以未净化的值调用，也不会把伪协议放进 href。
 */
export function QyLandingGroupQrCard(props: {
  joinUrl: string
  number: string
  className?: string
}) {
  const { t } = useTranslation()
  const joinUrl = sanitizeQyGroupJoinUrl(props.joinUrl)
  const number = sanitizeQyGroupNumber(props.number)

  if (!joinUrl && !number) return null

  return (
    <div className={props.className}>
      <div className='qy-lp-panel overflow-hidden'>
        <div className='qy-lp-panel-head flex items-center justify-between gap-3 px-5 py-3.5 sm:px-6'>
          <span className='qy-lp-micro qy-lp-muted'>
            {t('qy_home_group_title')}
          </span>
          <span className='qy-lp-micro qy-lp-code qy-lp-mist'>
            {t('qy_home_group_stamp')}
          </span>
        </div>

        <div className='flex flex-col items-center gap-5 px-5 py-6 sm:px-6'>
          {joinUrl ? (
            <div className='qy-lp-qr-plate'>
              <QRCodeSVG
                value={joinUrl}
                size={180}
                level='M'
                marginSize={2}
                title={t('qy_home_group_qr_title')}
              />
            </div>
          ) : null}

          {number ? (
            <div className='qy-lp-chip flex items-center gap-2 px-4 py-2'>
              <span className='qy-lp-micro qy-lp-muted'>
                {t('qy_home_group_number_label')}
              </span>
              <span className='qy-lp-code text-foreground text-lg'>
                {number}
              </span>
              <CopyButton
                value={number}
                size='sm'
                aria-label={t('qy_home_group_copy')}
              />
            </div>
          ) : null}

          {joinUrl ? (
            <a
              href={joinUrl}
              target='_blank'
              rel='noopener noreferrer'
              referrerPolicy='no-referrer'
              className='qy-lp-chip text-foreground flex items-center gap-1.5 px-4 py-2 text-sm'
            >
              {t('qy_home_group_join')}
              <ArrowUpRight className='size-4' />
            </a>
          ) : null}
        </div>

        <p className='qy-lp-readout qy-lp-panel-foot px-5 py-3.5 sm:px-6'>
          {joinUrl
            ? t('qy_home_group_foot_scan')
            : t('qy_home_group_foot_number')}
        </p>
      </div>
    </div>
  )
}
