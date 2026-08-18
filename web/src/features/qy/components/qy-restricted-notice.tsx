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
import { Link } from '@tanstack/react-router'
import { LifeBuoy, Lock, ShieldAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Markdown } from '@/components/ui/markdown'

import { useQyConfig } from '../hooks/use-qy-config'
import {
  useQyRestrictedNotice,
  type QyRestrictedNotice,
} from '../lib/restricted-notice'

/**
 * 受限账号的两块说明：常驻横幅 + 落地页。
 *
 * 放在同一个文件里，因为它们说的是**同一件事**，只是详略不同：横幅是"你现在
 * 的处境"，落地页是"你刚才点的那个地方为什么打不开、该去哪"。拆成两个文件的
 * 直接后果是两段文案各自演化，用户在两处读到不一样的规则。
 *
 * 文案必须具体到"哪些事不能做"。"您的账号异常"这种说法会把每一个受限用户都
 * 变成一张工单，而工单正是他此刻唯一能用的东西。
 *
 * ## 站点自定义公告落在**横幅**上，只落在横幅上
 *
 * 需求原文是"落地页与顶部说明条"，但这两块**永远同时在场**（见
 * {@link QyRestrictedGate}）：落地页出现时横幅就在它上面。两边各渲染一份的
 * 实际效果是同一段话在一屏里印两遍 —— 这正是上一轮实测后被钉住的形状
 * （`qy-restricted-gate.test.tsx` 的「不重复横幅已经说过的话」）。
 *
 * 选横幅而不是落地页，是因为横幅的覆盖面严格更大：受限账号打开白名单内的页面
 * （工单页、违规记录页）时**只有横幅**，落地页不渲染。而工单页恰恰是他去申诉的
 * 地方，"怎么申诉"这段话在那里必须还在。放落地页的话，用户点进工单页的那一刻
 * 公告就消失了。
 */

/** 工单入口是否真的存在（扩展开着且 ticket 功能开着）。 */
function useQyTicketEntryAvailable(): boolean {
  const config = useQyConfig()
  return config.enabled && config.features.ticket
}

/**
 * 站点自定义公告的正文块：标题 + Markdown。
 *
 * ## 为什么必须是 `untrusted`
 *
 * 这段内容由**管理员**填写、给**受限用户**看，信任方向与站点公告
 * （`console_setting.announcements`，管理员写给所有人看）看起来一样，
 * 但受众不同：受限用户刚被封号，正在找出口，是全站最容易点进一个
 * 「验证身份以解除限制」表单的人。默认那份净化档放行 `<form>` / `<input>` /
 * 任意 `style` / 外链图片，凑起来就是一个铺满视口的假登录框 —— 一个被拿下的
 * 管理员账号（或一次管理端 XSS）由此直接升级成对全体受限用户的钓鱼。
 *
 * 所以复用**工单那一档**（`untrusted`）：显式白名单、默认拒绝，与
 * `QyTicketThread` 逐字节同一份配置。仓库里只有这一份净化实现
 * （marked → DOMPurify），这里绝不引第二套，也绝不 `dangerouslySetInnerHTML`。
 *
 * `breaks` 开着：运营在一个多行文本框里写联系方式，按回车就该换行。
 */
function QyRestrictedAnnouncement(props: {
  notice: QyRestrictedNotice
  className?: string
}) {
  return (
    <div className={props.className}>
      <p className='font-medium'>{props.notice.title}</p>
      <Markdown breaks untrusted className='mt-1'>
        {props.notice.body}
      </Markdown>
    </div>
  )
}

/**
 * 常驻横幅。**不可关闭**：受限是一个持续状态，不是一次性通知；给它一个关闭
 * 按钮等于让用户把自己唯一的解释关掉，然后在别处反复撞 403。
 */
export function QyRestrictedBanner() {
  const { t } = useTranslation()
  const hasTickets = useQyTicketEntryAvailable()
  const notice = useQyRestrictedNotice()

  /*
    站点公告**替换的是"怎么申诉"那一句**，不是整条横幅。

    标题（「你的账号已被限制」）与不可用清单是站点无关的事实，任何一个站点都
    需要它们，而且运营写的公告不会重复这些 —— 让公告顶掉它们，结果是运营在
    自己那段文案里再抄一遍，抄漏就少一条。真正因站而异的只有出口：申诉走工单、
    走 QQ 群、还是走一个封禁政策页。

    没配 / 关闭 / 读取失败时一律回落到原来那两句固定文案，永远不会是空白
    （三态收敛在 `useQyRestrictedNotice` 里）。
  */
  let appeal = <>{t('qy_restricted_no_channel')}</>
  if (notice.enabled) {
    appeal = <QyRestrictedAnnouncement notice={notice} className='mt-1' />
  } else if (hasTickets) {
    appeal = <>{t('qy_restricted_appeal_hint')}</>
  }

  return (
    <div className='px-4 pt-4'>
      <Alert variant='destructive'>
        <Lock />
        <AlertTitle>{t('qy_restricted_banner_title')}</AlertTitle>
        <AlertDescription>
          {t('qy_restricted_blocked')}
          <br />
          {appeal}
        </AlertDescription>
      </Alert>
    </div>
  )
}

/**
 * 落地页：受限账号直接输 URL 到白名单之外时看到的东西。
 *
 * 刻意**不是** 403 报错页，也不是空白：用户没有做错什么，他只是点到了一个
 * 现在对他关闭的地方。所以这里给的是"发生了什么 + 现在能做什么"，
 * 并且把仅有的那两个出口摆在按钮上。
 */
export function QyRestrictedLanding() {
  const { t } = useTranslation()
  const config = useQyConfig()
  const hasTickets = config.enabled && config.features.ticket
  const hasViolations = config.enabled && config.features.violation

  return (
    <div className='flex min-h-0 flex-1 items-center justify-center overflow-auto p-6'>
      <div className='w-full max-w-xl space-y-5 text-center'>
        <div className='bg-muted text-muted-foreground mx-auto flex size-12 items-center justify-center rounded-full'>
          <Lock className='size-6' />
        </div>

        <div className='space-y-2'>
          <h1 className='text-xl font-semibold'>
            {t('qy_restricted_landing_title')}
          </h1>
          <p className='text-muted-foreground text-sm'>
            {t('qy_restricted_landing_desc')}
          </p>
        </div>

        {/*
          这里只说横幅**没有**说过的那件事 ——「你的东西还在」。
          「哪些事不能做」与「怎么申诉」由常驻横幅承担,而横幅与落地页永远同时
          在场(见 QyRestrictedGate),把它们再抄一遍的结果是同一屏上同样两段话
          出现两次,真正的增量信息反而被埋掉。已在浏览器里实测过那个形状。
        */}
        <div className='text-muted-foreground space-y-2 rounded-lg border p-4 text-start text-sm'>
          <p>{t('qy_restricted_kept')}</p>
        </div>

        <div className='flex flex-wrap items-center justify-center gap-3'>
          {hasTickets && (
            <Button render={<Link to='/qy/tickets' />}>
              <LifeBuoy className='size-4' />
              {t('qy_restricted_go_tickets')}
            </Button>
          )}
          {hasViolations && (
            <Button variant='outline' render={<Link to='/qy/violations' />}>
              <ShieldAlert className='size-4' />
              {t('qy_restricted_go_violations')}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
