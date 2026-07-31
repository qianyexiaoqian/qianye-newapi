# 前端整体接入:路由/菜单/i18n/权限

# 前端整体接入方案（qy 扩展）

> 本章节只负责「把新页面挂进去」的统一机制与共享基建。各功能页面内部设计（表单字段、图表、列定义）由对应模块负责，本章节为它们提供路由骨架、API 客户端、i18n、权限、共享组件的**强制契约**。

---

## 0. 本模块无新增数据库表

本模块不落任何 GORM 表。与「输出要求 1（表结构）」对应的是本模块唯一拥有的后端契约——**扩展能力探测端点**，它决定前端菜单与路由是否点亮，必须由 qy 后端实现且**必须无条件可用（即使新库不可用也要返回 200）**。

### 0.1 `GET /api/qy/config` —— 前端引导端点（本模块定义，后端实现）

| 项 | 值 |
|---|---|
| Method / Path | `GET /api/qy/config` |
| 权限 | 已登录用户（挂在原项目 `userAuth` 之后即可）；**不要求管理员** |
| 缓存 | 前端 `staleTime 5min` / `gcTime 30min` / `retry: false` |
| 硬性要求 | 只要 `qianye.Enabled() == true` 就必须返回 200；新库不可用时 `db_healthy:false` 但仍返回 200（否则菜单会整体消失，用户以为功能被删了） |

响应体（沿用原项目 `{success,message,data}` 信封）：

```jsonc
{
  "success": true,
  "data": {
    "enabled": true,
    "db_healthy": true,              // 新库连通性；false 时前端全局显示降级横幅
    "features": {                    // 与 YAML 开关一一对应，前端据此裁剪菜单项
      "transfer": true,              // 余额划转
      "affiliate": true,             // 邀请返佣
      "withdraw_quota": true,        // 站内额度兑换（D1 路径 A）
      "withdraw_fiat": true,         // 线下法币打款（D1 路径 B）
      "violation": true,             // 违规检测
      "availability": true           // 可用率监控
    },
    "limits": {                      // 前端表单预校验用，服务端仍需二次校验
      "transfer_min_quota": 5000,
      "transfer_max_quota": 50000000,
      "transfer_daily_max_quota": 100000000,
      "withdraw_min_quota": 2500000
    },
    "fiat": {                        // 法币提现展示用
      "currency": "CNY",
      "symbol": "¥",
      "current_rate": "7.30"         // string，禁止 number（见 §5.4）
    }
  }
}
```

**未启用时的行为（不需要后端做任何事）**：`qianye.RegisterRoutes` 未调用 → 请求落到 `router/web-router.go:29` 的 `NoRoute` → 因路径前缀是 `/api` 走 `controller.RelayNotFound`（`controller/relay.go:462-472`）→ **HTTP 404 + JSON `{"error":{...}}`，无 `success` 字段**。前端以此判定 `disabled`，无需任何额外协议。

---

## 1. 新页面挂载的标准做法

### 1.1 路由体系判定：**文件式路由**

证据：`web/rsbuild.config.ts:89-100` 注册了 `tanstackRouter({ target:'react', autoCodeSplitting: isProd })` 到 rspack plugins；`web/src/routeTree.gen.ts` 由该插件在 `rsbuild dev` / `rsbuild build` 时**重写**；`web/src/main.tsx:43` 只 `import { routeTree } from './routeTree.gen'`。

结论：**新增路由 = 新建文件，零手写路由表，零改 `routeTree.gen.ts`**。

### 1.2 qy 的路由树骨架（全部新建，不改任何原文件）

```
web/src/routes/_authenticated/qy/
├── route.tsx                     # 工作区布局路由：扩展启用守卫（唯一一处）
├── index.tsx                     # /qy → redirect 到默认页
├── affiliate/index.tsx           # /qy/affiliate
├── invitees/index.tsx            # /qy/invitees
├── transfer/index.tsx            # /qy/transfer
├── transfer-logs/index.tsx       # /qy/transfer-logs
├── withdraw/index.tsx            # /qy/withdraw
├── withdrawals/index.tsx         # /qy/withdrawals
├── violations/index.tsx          # /qy/violations
└── admin/
    ├── route.tsx                 # 管理区布局路由：ROLE.ADMIN 守卫（唯一一处）
    ├── index.tsx                 # /qy/admin → redirect
    ├── commission/index.tsx
    ├── commission-review/index.tsx
    ├── withdrawals/index.tsx
    ├── violation-rules/index.tsx
    ├── violations/index.tsx
    └── availability/index.tsx
```

**关键设计：管理员守卫只写一次。** 原项目 8 处路由的 `if (!auth.user || auth.user.role < ROLE.ADMIN) throw redirect({to:'/403'})` 是复制粘贴的（`redemption-codes/index.tsx:35-43`、`users/index.tsx:45`、`channels/index.tsx:40`、`models/index.tsx:29`、`models/$section.tsx:47`、`subscriptions/index.tsx:28`、`system-info/index.tsx:29`、`system-settings/route.tsx:29`）。qy 把它收进 `qy/admin/route.tsx` 一个布局路由，6 个管理页零重复。**不要去重构原有 8 处**（制造无谓冲突）。

### 1.3 可复制模板（4 个文件）

#### ① 工作区布局路由 `web/src/routes/_authenticated/qy/route.tsx`（新建，仅此一份）

```tsx
/* <AGPL 版权头，由 `bun run copyright` 自动补> */
import { createFileRoute, Outlet, redirect } from '@tanstack/react-router'

import { qyConfigQueryOptions } from '@/features/qy/lib/qy-config-query'

export const Route = createFileRoute('/_authenticated/qy')({
  beforeLoad: async ({ context }) => {
    // 只在「确定性地关闭」时拦截；网络错误/503 一律放行，由页面内错误态承担
    try {
      const cfg = await context.queryClient.ensureQueryData(qyConfigQueryOptions())
      if (!cfg.enabled) throw redirect({ to: '/404' })
    } catch (error) {
      if (isRedirect(error)) throw error
      // 未知态：放行
    }
  },
  component: Outlet,
})
```

> `context.queryClient` 来自根路由 `web/src/routes/__root.tsx:142` 的 `createRootRouteWithContext<{ queryClient: QueryClient }>()`，无需任何改造。
> `isRedirect` 从 `@tanstack/react-router` 导入——**必须**重新抛出，否则 `redirect` 会被 catch 吞掉（这是 TanStack 的经典坑）。

#### ② 管理区布局路由 `web/src/routes/_authenticated/qy/admin/route.tsx`（新建，仅此一份）

```tsx
import { createFileRoute, Outlet } from '@tanstack/react-router'

import { requireQyAdmin } from '@/features/qy/lib/qy-guards'

export const Route = createFileRoute('/_authenticated/qy/admin')({
  beforeLoad: requireQyAdmin,
  component: Outlet,
})
```

#### ③ 叶子页路由 `web/src/routes/_authenticated/qy/transfer-logs/index.tsx`（每页一份）

```tsx
import { createFileRoute } from '@tanstack/react-router'
import z from 'zod'

import { QyTransferLogs } from '@/features/qy/transfer-logs'
import { QY_TRANSFER_STATUS_VALUES } from '@/features/qy/transfer-logs/constants'

const searchSchema = z.object({
  page: z.number().optional().catch(1),
  pageSize: z.number().optional().catch(undefined),
  filter: z.string().optional().catch(''),
  status: z.array(z.enum(QY_TRANSFER_STATUS_VALUES)).optional().catch([]),
  direction: z.enum(['all', 'in', 'out']).optional().catch('all'),
  startTime: z.number().optional(),
  endTime: z.number().optional(),
})

export const Route = createFileRoute('/_authenticated/qy/transfer-logs/')({
  validateSearch: searchSchema,
  component: QyTransferLogs,
})
```

> 无 `beforeLoad`：登录由 `_authenticated/route.tsx:24-36` 保证，扩展启用由 `qy/route.tsx` 保证，管理员由 `qy/admin/route.tsx` 保证。**叶子路由永远不写守卫**——这是本方案的硬约定。

#### ④ 页面组件 `web/src/features/qy/transfer-logs/index.tsx`

```tsx
import { getRouteApi } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import { QyPageBoundary } from '@/features/qy/components/qy-page-boundary'
import { qyKeys } from '@/features/qy/lib/qy-keys'

import { getQyTransferLogs } from './api'
import { useQyTransferLogColumns } from './components/columns'

const route = getRouteApi('/_authenticated/qy/transfer-logs/')

export function QyTransferLogs() {
  const { t } = useTranslation()
  const { pagination, onPaginationChange, globalFilter, onGlobalFilterChange,
          columnFilters, onColumnFiltersChange, ensurePageInRange } =
    useTableUrlState({
      search: route.useSearch(),
      navigate: route.useNavigate(),
      pagination: { defaultPage: 1, defaultPageSize: 20 },
      globalFilter: { enabled: true, key: 'filter' },
      columnFilters: [{ columnId: 'status', searchKey: 'status', type: 'array' }],
    })

  const params = { p: pagination.pageIndex + 1, page_size: pagination.pageSize }
  const query = useQuery({
    queryKey: qyKeys.transferLogs(params),
    queryFn: () => getQyTransferLogs(params),
    placeholderData: (prev) => prev,
  })

  const { table } = useDataTable({ /* ... manualPagination/manualFiltering ... */ })

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('qy_tr_logs_title')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <QyPageBoundary query={query} emptyIcon={ArrowLeftRight}>
          <DataTablePage table={table} columns={columns} isLoading={query.isLoading} />
        </QyPageBoundary>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
```

#### ⑤ 数据获取 `web/src/features/qy/transfer-logs/api.ts`

```ts
import { qyGet } from '@/features/qy/lib/qy-http'
import type { QyPage, QyTransferLog } from './types'

export function getQyTransferLogs(params: { p: number; page_size: number }) {
  return qyGet<QyPage<QyTransferLog>>('/transfer/logs', params)
}
```

### 1.4 新增一个 qy 页面的完整清单

| 动作 | 文件 |
|---|---|
| 新建 | `web/src/routes/_authenticated/qy/<name>/index.tsx`（或 `qy/admin/<name>/index.tsx`） |
| 新建 | `web/src/features/qy/<name>/{index.tsx,api.ts,types.ts,constants.ts,components/,lib/}` |
| 新增 key | `web/src/i18n/qy/en.json` + `zh.json`（**不碰 `locales/`**） |
| 加菜单 | `web/src/features/qy/nav.ts` 里的数组加一项（**qy 自己的文件，非原项目文件**） |
| 自动重写 | `web/src/routeTree.gen.ts` |

**新增第 2..N 个 qy 页面时，原项目文件改动数 = 0。** 全部 6 行原文件改动只在首次接入时一次性付出（见 §4）。

---

## 2. `routeTree.gen.ts` 冲突治理

### 2.1 事实基线

- 文件已提交入库（`web/.gitignore` 未忽略它），`.gitattributes` 已标注 `web/src/routeTree.gen.ts linguist-generated`。
- `web/.oxlintrc.json` 的 `ignorePatterns` 已含 `src/routeTree.gen.ts`，lint 不会碰。
- 生成时机：`@tanstack/router-plugin/rspack` 在 rspack 配置阶段扫描 `src/routes/**` 后**写盘**。因此 `bun run dev` 与 `bun run build` 都会重生成。
- **该文件的冲突内容 100% 无信息量**——它是 `src/routes/**` 的纯函数。任何手工解决冲突的行为都是在浪费时间且引入风险。

### 2.2 合并流程文档（原文，请整段抄进 fork 的 `MERGE.md`）

```markdown
## 合并上游时：routeTree.gen.ts 冲突处理

`web/src/routeTree.gen.ts` 是 TanStack Router 插件从 `web/src/routes/**` 
自动生成的产物。**它的冲突内容没有任何信息量，禁止手工合并。**

标准流程：

    # 1. 合并上游
    git fetch upstream
    git merge upstream/main

    # 2. routeTree.gen.ts 冲突时，先把它踢出冲突集（内容不重要，下一步重生成）
    git checkout --theirs web/src/routeTree.gen.ts   # 取上游版本作为基底
    #  等价做法：rm web/src/routeTree.gen.ts

    # 3. 先解决其他真实冲突（路由源文件、i18n、sidebar 等），
    #    确认 web/src/routes/** 下上游新增/删除的路由文件都已正确合并
    git status --short -- web/src/routes

    # 4. 重生成（二选一，插件在编译前写盘）
    cd web
    bun install
    bun run build          # 稳，约 1~3 分钟
    # 或：bun run dev      # 快，看到首次编译完成后 Ctrl-C 即可

    # 5. 校验：确认新旧路由都在，且 TS 通过
    git diff --stat -- src/routeTree.gen.ts
    grep -c "createFileRoute" src/routeTree.gen.ts
    bun run typecheck
    bun run lint

    # 6. 提交
    cd ..
    git add web/src/routeTree.gen.ts
    git merge --continue

### 常见错误
- ❌ 手工挑选冲突块 → 必然漏掉上游新路由，运行时 404 且 typecheck 可能仍通过
- ❌ `git checkout --ours` 后不重生成 → 上游新增的路由全部丢失
- ❌ 只跑 `bun run typecheck` 不跑 build → tsgo 不触发插件，文件不会被重生成
```

### 2.3 可选加速项（不推荐默认开启）

在 `.gitattributes` 追加一行 `web/src/routeTree.gen.ts merge=ours` 并本地 `git config merge.ours.driver true`，可让 git 静默保留我方版本、不再产生冲突。**代价：失去"必须重生成"的提醒，上游新增路由会静默丢失。**建议**不要**开启——保留冲突正是提醒你执行第 4 步的机制。

### 2.4 顺带治理：`i18n/locales/*.json` 与 `_reports/`

qy 的 i18n key **完全不进 `web/src/i18n/locales/`**（见 §6），因此上游 7 个 5232 行的大 JSON 与 `_reports/_sync-report.json`、`_extras/*.extras.json` 与我们零交集。

**硬规定：qy 相关改动禁止运行 `bun run i18n:sync`。** 该脚本（`web/scripts/sync-i18n.mjs`）会重写全部 7 个 locale 文件做格式归一化，制造巨量无意义 diff，且我们的 key 不在它的扫描范围（`LOCALES_DIR = path.resolve('src/i18n/locales')`）。

---

## 3. 菜单 / 侧边栏：收敛成 1 行

### 3.1 挂载点选型

原项目侧边栏是**两层机制**：

- **角色过滤**（`web/src/hooks/use-sidebar-view.ts:54-65`）：`group.id === 'admin'` 整组按 `role >= ROLE.ADMIN` 过滤；单项按 `item.requiredRole` 过滤。
- **后台开关过滤**（`web/src/hooks/use-sidebar-config.ts:168-177`）：**未在 `URL_TO_CONFIG_MAP` 中的 URL 默认可见**，所以 `/qy/*` 不需要改 `use-sidebar-config.ts`。

### 3.2 确切实现：`use-sidebar-data.ts` 改 2 行（1 import + 1 数组行）

**插入位置在 `navGroups` 数组末尾（第 160 行 `      },` 之后、第 161 行 `    ],` 之前）**，这是数组尾部追加，与上游任何一次「新增菜单项」的 diff 都不重叠：

```ts
// web/src/hooks/use-sidebar-data.ts
import { type SidebarData } from '@/components/layout/types'
import { useQySidebarGroups } from '@/features/qy/nav'   // ← 新增（第 40 行，按字典序落在此处）
import { ROLE } from '@/lib/roles'

export function useSidebarData(): SidebarData {
  const { t } = useTranslation()
  return {
    navGroups: [
      { id: 'chat',     /* ... */ },
      { id: 'general',  /* ... */ },
      { id: 'personal', /* ... */ },
      { id: 'admin',    /* ... */ },
      ...useQySidebarGroups(),                          // ← 新增（第 161 行）
    ],
  }
}
```

**为什么在 `navGroups` 层级而不是 `items` 层级插入**：
1. `items` 层级插入只能落进某一个既有分组，管理员入口和用户入口无法分置 → 必须插 2 处。
2. `navGroups` 层级插入 1 行即可返回 0~1 个完整分组，分组内自己控制项与角色，**是唯一能做到"字面 1 行"的位置**。
3. 数组尾部追加，冲突面最小。

### 3.3 `web/src/features/qy/nav.ts`（新建，本方案核心）

```ts
import { type TFunction } from 'i18next'
import { ArrowLeftRight, ShieldAlert, Users2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import type { NavGroup } from '@/components/layout/types'
import { ROLE } from '@/lib/roles'

import { useQyConfig } from './hooks/use-qy-config'
import { getQyConfigSnapshot } from './lib/qy-config-snapshot'

/** 根侧边栏入口：一个用户入口 + 一个管理员入口，收在同一个新分组里 */
export function useQySidebarGroups(): NavGroup[] {
  const { t } = useTranslation()
  const { status, features } = useQyConfig()
  if (status !== 'enabled') return []          // 未启用 / 未知 → 菜单整体消失

  const items: NavGroup['items'] = []
  if (features.transfer || features.affiliate) {
    items.push({ title: t('qy_nav_workspace'), url: '/qy/affiliate',
                 activeUrls: ['/qy'], icon: Users2 })
  }
  items.push({ title: t('qy_nav_admin_workspace'), url: '/qy/admin/commission',
               activeUrls: ['/qy/admin'], icon: ShieldAlert,
               requiredRole: ROLE.ADMIN })     // ← 复用原项目既有的单项角色过滤
  return items.length > 0
    ? [{ id: 'qy', title: t('qy_nav_group'), items }]
    : []
}
```

**管理员可见 / 普通用户可见的判定，完全复用原项目机制，不自造轮子：**

| 需求 | 实现 | 依据 |
|---|---|---|
| 管理员入口仅 role≥10 可见 | 菜单项加 `requiredRole: ROLE.ADMIN` | `use-sidebar-view.ts:60-62` |
| 分组 id **不能**叫 `admin` | 用 `id: 'qy'` | `use-sidebar-view.ts:58` 会把 `id==='admin'` 整组对普通用户隐藏，我们的分组里有用户项 |
| 后台模块开关不误杀 | 无需任何操作 | `use-sidebar-config.ts:173-176` 未映射 URL 默认可见 |
| ⌘K 命令面板自动出现 | 无需任何操作 | `command-menu.tsx:51` 复用 `useSidebarData()` |
| 折叠态 / 移动端抽屉 | 无需任何操作 | 同一数据源 |

### 3.4 二级工作区（drill-in）：`sidebar-view-registry.ts` 改 2 行

进入 `/qy/*` 后，根侧边栏被替换为 qy 工作区侧栏（含 `← 返回控制台`），12 个子页面全部挂在这里，**不占用根侧边栏任何一行**。

```ts
// web/src/components/layout/lib/sidebar-view-registry.ts
import { QY_WORKSPACE_VIEW } from '../config/qy-workspace.config'      // ← 新增（第 21 行）
import { SYSTEM_SETTINGS_VIEW } from '../config/system-settings.config'

const SIDEBAR_VIEWS: readonly SidebarView[] = [
  SYSTEM_SETTINGS_VIEW,
  QY_WORKSPACE_VIEW,                                                   // ← 新增（第 34 行）
]
```

`app-sidebar.tsx:43-44` 的源码注释明确承诺：「Adding a new nested view only requires registering a SidebarView in the registry; this component requires no changes.」

### 3.5 ⚠️ 高危陷阱：嵌套视图**不经过**角色过滤

`use-sidebar-view.ts:67-75` 的嵌套分支是：

```ts
const view = resolveSidebarView(pathname)
if (view) {
  return { key: view.id, view, navGroups: view.getNavGroups(t) }   // ← 原样返回，零过滤
}
```

`rootNavGroups` 的角色过滤（`:54-65`）**只作用于根分支**。也就是说，**如果把管理页塞进 qy 工作区的 `getNavGroups` 而不自己过滤，普通用户进入 `/qy/affiliate` 后会在侧栏看到"佣金审核""提现审核""违规记录"等全部管理入口名称**——这与本项目「修复无权分组名称泄漏」（D2）的初衷正面冲突。

**必须在 `getQyNavGroups` 内部自行过滤：**

```ts
// web/src/components/layout/config/qy-workspace.config.ts（新建）
import { type TFunction } from 'i18next'
import { ArrowLeftRight, /* ... */ } from 'lucide-react'

import { getQyConfigSnapshot } from '@/features/qy/lib/qy-config-snapshot'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import type { NavGroup, SidebarView } from '../types'

function getQyNavGroups(t: TFunction): NavGroup[] {
  const role = useAuthStore.getState().auth.user?.role ?? ROLE.GUEST
  const { features } = getQyConfigSnapshot()
  const groups: NavGroup[] = []

  const personal: NavGroup['items'] = []
  if (features.affiliate) {
    personal.push({ title: t('qy_nav_affiliate'),  url: '/qy/affiliate' })
    personal.push({ title: t('qy_nav_invitees'),   url: '/qy/invitees' })
  }
  if (features.transfer) {
    personal.push({ title: t('qy_nav_transfer'),      url: '/qy/transfer' })
    personal.push({ title: t('qy_nav_transfer_logs'), url: '/qy/transfer-logs' })
  }
  if (features.withdraw_quota || features.withdraw_fiat) {
    personal.push({ title: t('qy_nav_withdraw'),    url: '/qy/withdraw' })
    personal.push({ title: t('qy_nav_withdrawals'), url: '/qy/withdrawals' })
  }
  if (features.violation) {
    personal.push({ title: t('qy_nav_my_violations'), url: '/qy/violations' })
  }
  if (personal.length > 0) {
    groups.push({ id: 'qy-personal', title: t('qy_nav_group_personal'), items: personal })
  }

  if (role >= ROLE.ADMIN) {                                  // ★ 必须，见 §3.5
    const admin: NavGroup['items'] = []
    if (features.affiliate) {
      admin.push({ title: t('qy_nav_a_commission'),        url: '/qy/admin/commission' })
      admin.push({ title: t('qy_nav_a_commission_review'), url: '/qy/admin/commission-review' })
      admin.push({ title: t('qy_nav_a_withdrawals'),       url: '/qy/admin/withdrawals' })
    }
    if (features.violation) {
      admin.push({ title: t('qy_nav_a_violation_rules'), url: '/qy/admin/violation-rules' })
      admin.push({ title: t('qy_nav_a_violations'),      url: '/qy/admin/violations' })
    }
    if (features.availability) {
      admin.push({ title: t('qy_nav_a_availability'), url: '/qy/admin/availability' })
    }
    if (admin.length > 0) {
      groups.push({ id: 'qy-admin', title: t('qy_nav_group_admin'), items: admin })
    }
  }
  return groups
}

export const QY_WORKSPACE_VIEW: SidebarView = {
  id: 'qy-workspace',
  pathPattern: /^\/qy(\/|$)/,
  parent: { to: '/dashboard/overview', label: 'qy_nav_back' },   // label 是 i18n key，由 sidebar-view-header.tsx:53,66 的 t() 翻译
  getNavGroups: getQyNavGroups,
}
```

### 3.6 为什么 `getState()` 在这里是安全（且响应式）的

`getQyNavGroups` 不是 hook，用 `useAuthStore.getState()` 与模块级 snapshot 读取。响应式来自一条**不显眼但关键的链路**：

`use-sidebar-view.ts:50-52` **无条件**调用了 `useAuthStore((s)=>s.auth.user?.role)` 与 `useSidebarData()`（后者内部调 `useQySidebarGroups()` → `useQyConfig()` 的 `useQuery`），**即使当前走的是嵌套视图分支**。因此：

- 角色变化 → 第 50 行订阅触发重渲染 → `getNavGroups(t)` 重新执行 → 拿到新 role。✅
- `/api/qy/config` 返回 → `useQyConfig` 的 query 触发重渲染 → `getNavGroups(t)` 重新执行 → 拿到新 features。✅

这条链路是本设计成立的前提，**任何人试图把 `useQySidebarGroups()` 改成条件调用都会打破它**，务必在代码注释中写明。

---

## 4. 原项目改动清单（前端，共 **4 个文件 / 6 行**）

| # | 文件:行号 | 插入/修改的确切代码 | 类型 | 冲突风险 |
|---|---|---|---|---|
| F1 | `web/src/hooks/use-sidebar-data.ts:39` 后 | `import { useQySidebarGroups } from '@/features/qy/nav'` | 只增 | **低** |
| F2 | `web/src/hooks/use-sidebar-data.ts:160` 后 | `      ...useQySidebarGroups(),` | 只增（数组尾） | **低** |
| F3 | `web/src/components/layout/lib/sidebar-view-registry.ts:20` 后 | `import { QY_WORKSPACE_VIEW } from '../config/qy-workspace.config'` | 只增 | **低** |
| F4 | `web/src/components/layout/lib/sidebar-view-registry.ts:33` | `const SIDEBAR_VIEWS: readonly SidebarView[] = [SYSTEM_SETTINGS_VIEW, QY_WORKSPACE_VIEW]` | **修改**（唯一一处） | **低**（该数组自建立以来只有 1 个元素，上游变更频率极低） |
| F5 | `web/src/i18n/config.ts:30` 后 | `import { registerQyResources } from './qy'` | 只增 | **低** |
| F6 | `web/src/i18n/config.ts:62` 后（`.init({...})` 之后，`export default i18n` 之前） | `registerQyResources(i18n)` | 只增 | **低** |
| — | `web/src/routeTree.gen.ts` | 无需手改 | 自动重写 | **高（但可自动化，见 §2）** |
| — | `web/src/features/wallet/index.tsx` | 由钱包模块负责；本方案只提供入口组件 `QyWalletEntryCard`（`@/features/qy/components/qy-wallet-entry-card`），钱包模块 import + 渲染 = 2 行 | 只增 | **中** |

**注意 import 排序**：`web/.oxfmtrc.json` 开启了 `sortImports`。F1 的 `@/features/qy/nav` 按字典序落在 `@/components/layout/types`（第 39 行）与 `@/lib/roles`（第 40 行）之间；F3 的 `../config/qy-workspace.config` 按字典序落在 `../config/system-settings.config`（第 21 行）**之前**；F5 的 `./qy` 落在 `./locales/zh.json`（第 30 行）**之后**。若插错位置，`bun run format` 会自动纠正，但会让 diff 变脏——按上表位置插入即可一次到位。

**明确不需要改的原文件**（逐个确认过）：
`use-sidebar-config.ts`（未映射 URL 默认可见，`:173-176`）、`app-sidebar.tsx`（源码注释承诺）、`nav-group.tsx`、`command-menu.tsx`、`authenticated-layout.tsx`、`main.tsx`、`__root.tsx`、`_authenticated/route.tsx`、`http-client.ts`、`lib/api.ts`、`i18n/static-keys.ts`、`i18n/locales/*.json`（7 个）、`.oxlintrc.json`、`package.json`、`rsbuild.config.ts`、`.gitattributes`。

---

## 5. API 客户端

### 5.1 不新建 axios 实例

原项目 `web/src/lib/http-client.ts:44-50` 的 `api` 实例已附带：`withCredentials`、请求拦截器注入 `Authorization: Bearer`（`:144-150`）、401 自动 `refreshAuthentication()` 重试一次并跳 `/sign-in`（`:105-130`）、GET in-flight 去重（`:52-69`）。**新建实例 = 把这四样全部重写一遍**。qy 一律复用 `api`，只在其上包一层错误语义层。

### 5.2 `web/src/features/qy/lib/qy-http.ts`（新建）

```ts
import { api, type ApiRequestConfig } from '@/lib/api'

export const QY_PREFIX = '/api/qy'

export type QyEnvelope<T> = { success: boolean; message?: string; data?: T }
export type QyPage<T> = { items: T[]; total: number; page: number; page_size: number }

export type QyFailureKind =
  | 'disabled'     // 404 / 非 JSON：扩展未启用，前端应静默隐藏入口
  | 'unavailable'  // 503：新库不可用，非热路径返回 503（与后端降级契约一致）
  | 'forbidden'    // 403
  | 'conflict'     // 409：单据已被他人处理
  | 'business'     // 200 + success:false
  | 'network'      // 其他

export class QyError extends Error {
  constructor(
    readonly kind: QyFailureKind,
    readonly i18nKey: string,
    readonly rawMessage: string | null,
    readonly status: number | null
  ) {
    super(rawMessage ?? i18nKey)
    this.name = 'QyError'
  }
}

function isEnvelope(v: unknown): v is QyEnvelope<unknown> {
  return (
    typeof v === 'object' && v !== null &&
    typeof (v as { success?: unknown }).success === 'boolean'
  )
}

function toQyError(e: unknown): QyError {
  const status =
    (e as { response?: { status?: number } })?.response?.status ?? null
  const raw =
    (e as { response?: { data?: { message?: string } } })?.response?.data?.message ?? null
  if (status === 404) return new QyError('disabled', 'qy_err_disabled', raw, status)
  if (status === 503) return new QyError('unavailable', 'qy_err_unavailable', raw, status)
  if (status === 403) return new QyError('forbidden', 'qy_err_forbidden', raw, status)
  if (status === 409) return new QyError('conflict', 'qy_err_conflict', raw, status)
  return new QyError('network', 'qy_err_network', raw, status)
}

function unwrap<T>(data: unknown, status: number): T {
  // 扩展未注册时 NoRoute 兜底也可能回非 JSON（FRONTEND_BASE_URL 重定向场景）
  if (!isEnvelope(data)) throw new QyError('disabled', 'qy_err_disabled', null, status)
  if (!data.success) {
    throw new QyError('business', 'qy_err_unknown', data.message ?? null, status)
  }
  return data.data as T
}

/** GET：走 api.get 以继承 in-flight 去重 */
export async function qyGet<T>(
  path: string,
  params?: Record<string, unknown>,
  config?: ApiRequestConfig
): Promise<T> {
  try {
    const res = await api.get(`${QY_PREFIX}${path}`, {
      params,
      skipErrorHandler: true,   // 错误文案由 qy 层统一决定，不让全局拦截器抢先 toast
      skipBusinessError: true,
      ...config,
    })
    return unwrap<T>(res.data, res.status)
  } catch (e) {
    throw e instanceof QyError ? e : toQyError(e)
  }
}

/** 变更类：POST/PUT/DELETE */
export async function qyMutate<T>(
  method: 'post' | 'put' | 'delete',
  path: string,
  body?: unknown,
  config?: ApiRequestConfig
): Promise<T> {
  try {
    const res = await api.request({
      method, url: `${QY_PREFIX}${path}`, data: body,
      skipErrorHandler: true, skipBusinessError: true, ...config,
    })
    return unwrap<T>(res.data, res.status)
  } catch (e) {
    throw e instanceof QyError ? e : toQyError(e)
  }
}
```

> **为什么设 `skipBusinessError: true`**：全局响应拦截器（`http-client.ts:86-97`）在 `success === false` 时会直接 `toast.error(response.data.message)`，那是后端原始英文/中文文案，无法 i18n、也无法按 `kind` 分流。qy 层接管后由 `useQyMutation` 统一按 `QyError.kind` 出文案。

### 5.3 react-query key 规范

```ts
// web/src/features/qy/lib/qy-keys.ts
export const qyKeys = {
  all: ['qy'] as const,
  config: () => [...qyKeys.all, 'config'] as const,

  transferQuota:  ()             => [...qyKeys.all, 'transfer', 'quota'] as const,
  transferLogs:   (p: unknown)   => [...qyKeys.all, 'transfer', 'logs', p] as const,
  affiliateSum:   ()             => [...qyKeys.all, 'affiliate', 'summary'] as const,
  invitees:       (p: unknown)   => [...qyKeys.all, 'affiliate', 'invitees', p] as const,
  commissions:    (p: unknown)   => [...qyKeys.all, 'commission', 'list', p] as const,
  withdrawBal:    ()             => [...qyKeys.all, 'withdraw', 'balance'] as const,
  withdrawals:    (p: unknown)   => [...qyKeys.all, 'withdraw', 'list', p] as const,
  myViolations:   (p: unknown)   => [...qyKeys.all, 'violation', 'mine', p] as const,

  adminCommissionCfg:    ()           => [...qyKeys.all, 'admin', 'commission-config'] as const,
  adminCommissionReview: (p: unknown) => [...qyKeys.all, 'admin', 'commission-review', p] as const,
  adminWithdrawals:      (p: unknown) => [...qyKeys.all, 'admin', 'withdrawals', p] as const,
  adminViolationRules:   ()           => [...qyKeys.all, 'admin', 'violation-rules'] as const,
  adminViolations:       (p: unknown) => [...qyKeys.all, 'admin', 'violations', p] as const,
  adminAvailability:     (p: unknown) => [...qyKeys.all, 'admin', 'availability', p] as const,
} as const
```

**强制约定**：所有 qy key 以 `'qy'` 开头，使得任一资金操作后可用 `queryClient.invalidateQueries({ queryKey: qyKeys.all })` 一次性失效全部 qy 缓存——跨库两阶段下前端无法判断哪些视图受影响，全量失效是唯一安全策略。

### 5.4 资金变更后的统一收尾（必须调用）

划转 / 佣金兑现 / 提现到账都会**改主库 `users.quota`**，而主库余额不在 qy 的 query 缓存里，它在 Zustand `auth-store` 与钱包页的组件本地 state 里。原项目没有任何全局「刷新用户余额」机制（`features/wallet/hooks/use-affiliate.ts:72` 只是 `await getSelf()` 后**连 store 都不更新**）。qy 必须自己补：

```ts
// web/src/features/qy/hooks/use-qy-after-money-change.ts
export function useQyAfterMoneyChange() {
  const queryClient = useQueryClient()
  return useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: qyKeys.all })     // qy 全量
    try {
      const res = await getSelf()                                     // 主库余额
      if (res?.success && res.data) {
        useAuthStore.getState().auth.setUser(res.data as AuthUser)    // auth-store.ts:116-120
      }
    } catch { /* 余额刷新失败不阻断主流程，用户下次进页面会拿到 */ }
    await queryClient.invalidateQueries({ queryKey: ['status'] })
  }, [queryClient])
}
```

### 5.5 扩展未启用时的优雅隐藏

```ts
// web/src/features/qy/hooks/use-qy-config.ts
const LS_KEY = 'qy_enabled'          // '1' | '0'

export type QyStatus = 'enabled' | 'disabled' | 'unknown'

export function useQyConfig() {
  const q = useQuery({
    ...qyConfigQueryOptions(),
    placeholderData: readSnapshotFromLocalStorage(),  // 冷启动零闪烁
  })
  // ...
}

export function qyConfigQueryOptions() {
  return queryOptions({
    queryKey: qyKeys.config(),
    queryFn: async () => {
      try {
        const data = await qyGet<QyConfig>('/config')
        localStorage.setItem(LS_KEY, '1')
        writeSnapshot(data)                            // 供非 hook 环境（§3.5）读取
        return { status: 'enabled' as const, ...data }
      } catch (e) {
        if (e instanceof QyError && e.kind === 'disabled') {
          localStorage.setItem(LS_KEY, '0')
          writeSnapshot(DISABLED_SNAPSHOT)
          return { status: 'disabled' as const, ...DISABLED_SNAPSHOT }
        }
        throw e                                        // 503/网络 → 保持 unknown
      }
    },
    retry: false,                                      // ★ 未启用时不要重试 4 次
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
    refetchOnWindowFocus: false,
  })
}
```

隐藏策略矩阵：

| 后端状态 | `status` | 侧边栏入口 | 直接访问 `/qy/*` | 页面内 |
|---|---|---|---|---|
| 200 `enabled:true` | `enabled` | 显示 | 正常 | 正常 |
| 404 / 非 JSON | `disabled` | **隐藏** | `beforeLoad` → `/404` | — |
| 200 `db_healthy:false` | `enabled` | 显示 | 正常进入 | 全局降级横幅 + 写操作按钮禁用（`qy_err_db_down`） |
| 503 | `unknown` | 沿用 localStorage 上次值 | 放行 | `ErrorState` + 重试按钮 |
| 网络错误 | `unknown` | 同上 | 放行 | 同上 |

> `localStorage` 兜底照抄原项目 `use-status.ts:28-38` 的模式（`getInitialStatus()` 从 localStorage 读 `status` 作 `placeholderData`），保证刷新页面时菜单不闪烁。

---

## 6. i18n

### 6.1 键名规范：`qy_` 前缀 + 全下划线扁平键

```
qy_<domain>_<name>
```

`domain` 白名单：`nav` `common` `err` `tr`(transfer) `aff`(affiliate) `wd`(withdraw) `cm`(commission) `vio`(violation) `avl`(availability) `cfg`。

示例：
```jsonc
"qy_nav_group":              "推广与结算",
"qy_nav_workspace":          "我的推广",
"qy_nav_admin_workspace":    "推广管理",
"qy_nav_back":               "返回控制台",
"qy_common_amount":          "金额",
"qy_common_status":          "状态",
"qy_tr_confirm_desc":        "确认向 {{user}}（ID {{uid}}）转账 {{amount}}？该操作不可撤销。",
"qy_err_disabled":           "该功能未启用",
"qy_err_unavailable":        "服务暂时不可用，请稍后重试",
"qy_err_conflict":           "该申请已被其他管理员处理，请刷新后查看"
```

**硬性禁令：**

1. **禁止点号**。`web/src/i18n/config.ts:45-62` 未设置 `keySeparator`，i18next 默认 `keySeparator: '.'` 生效，`"qy.tr.title"` 会被当作三层嵌套对象查找而落空。
2. **禁止大写与连字符**，保持全小写 + 下划线，便于 grep（`grep -o "qy_[a-z0-9_]*"` 可一次性提取全部键做覆盖率校验）。
3. **传 `count` 的键不要自带 `_one`/`_other`/`_zero`/`_few`/`_many` 后缀**。i18next 默认 `pluralSeparator: '_'`，与我们的分隔符同字符；让 i18next 自己拼后缀，我们只写基础键（如 `qy_aff_invitee_count`，i18next 会查 `qy_aff_invitee_count_other`）。
4. 冒号安全（`nsSeparator: false`，`config.ts:50`），但仍不建议用。

### 6.2 独立 resource bundle：验证与确切做法

**验证结论**：项目使用 `i18next@^26.3.4`（`web/package.json:48`），`i18n.addResourceBundle(lng, ns, resources, deep, overwrite)` 是自 v2 起的稳定 API，v26 仍在。`web/src/i18n/config.ts:42-62` 的 `init()` 传入的是**静态 resources 对象且无 backend**，资源仓库在 `init()` 调用栈内**同步**填充（返回的 Promise 只是给异步 backend 用的），因此紧随 `init()` 之后调用 `addResourceBundle` 是安全的。

**文件组织（全部新建，与上游 7 个 5232 行大 JSON 零交集）：**

```
web/src/i18n/qy/
├── index.ts        # registerQyResources(i18n)
├── en.json         # 必需（fallbackLng）
├── zh.json         # 必需
├── zh-TW.json      # 可选
├── fr.json ja.json ru.json vi.json    # 可选
```

```ts
// web/src/i18n/qy/index.ts
import type { i18n as I18nInstance } from 'i18next'

import en from './en.json'
import fr from './fr.json'
import ja from './ja.json'
import ru from './ru.json'
import vi from './vi.json'
import zhTW from './zh-TW.json'
import zhCN from './zh.json'

// key 必须与 config.ts:32-40 的 resources key 完全一致
const QY_BUNDLES: Record<string, Record<string, string>> = {
  en, zhCN, fr, ru, ja, vi, zhTW,
}

/**
 * 把 qy 扩展的翻译并入 'translation' 命名空间。
 * 必须在 i18n.init() 之后调用（init 传静态 resources 时是同步完成的）。
 * deep=true 深合并、overwrite=true 允许 qy 覆盖同名键（正常不会有同名，qy_ 前缀已隔离）。
 */
export function registerQyResources(i18n: I18nInstance): void {
  for (const [lng, bundle] of Object.entries(QY_BUNDLES)) {
    i18n.addResourceBundle(lng, 'translation', bundle, true, true)
  }
}
```

```ts
// web/src/i18n/config.ts —— 只增 2 行
import zhCN from './locales/zh.json'
import { registerQyResources } from './qy'          // ← F5（第 31 行）

i18n.use(LanguageDetector).use(initReactI18next).init({ /* 未改动 */ })

registerQyResources(i18n)                            // ← F6（第 64 行）

export default i18n
```

**JSON 文件内容形状**：**扁平的 `{key: value}`，不要包 `{"translation": {...}}`**——命名空间由 `addResourceBundle` 的第二个参数 `'translation'` 指定。

### 6.3 减少合并冲突的收益

| 项 | 传统做法（往 locales 插） | 本方案 |
|---|---|---|
| 上游合并冲突面 | 7 个文件 × 每次都在 5232 行里插行 → **高频行级冲突** | **0**（新目录，上游永远不碰） |
| `bun run i18n:sync` 影响 | 会重排全部 7 个文件、抽 `_extras`、写 `_reports` → 巨量噪声 diff | **0**（脚本 `LOCALES_DIR = src/i18n/locales`，扫不到 `src/i18n/qy`） |
| `static-keys.ts` 登记 | 非 `t('字面量')` 形式的键必须登记（575 行数组，中冲突） | **不需要**（登记的唯一目的是让 sync 脚本识别，而 sync 不管我们） |
| 翻译缺失 | 需补齐 7 份 | **只需 `en.json` + `zh.json`**；`fallbackLng: 'en'` + `load:'currentOnly'`（`config.ts:47-48`）保证其余 5 语种自动回落英文 |

### 6.4 覆盖率自检（建议加进 CI 或 pre-commit）

```bash
# 提取代码里用到的 qy_ 键 vs en.json 里定义的键，找差集
cd web
grep -rhoE "qy_[a-z0-9_]+" src --include=*.ts --include=*.tsx | sort -u > /tmp/used.txt
node -e "console.log(Object.keys(require('./src/i18n/qy/en.json')).join('\n'))" | sort -u > /tmp/defined.txt
comm -23 /tmp/used.txt /tmp/defined.txt    # 输出非空 = 有键未翻译
```

---

## 7. 权限

### 7.1 三层判定（全部复用原项目）

| 层 | 位置 | 实现 |
|---|---|---|
| 登录 | `web/src/routes/_authenticated/route.tsx:24-36` | 已有，qy 路由全部挂在 `_authenticated` 下自动继承 |
| 扩展启用 | `qy/route.tsx`（新建，唯一一处） | `ensureQueryData(qyConfigQueryOptions())` → `enabled === false` 才 redirect `/404` |
| 管理员 | `qy/admin/route.tsx`（新建，唯一一处） | `requireQyAdmin()` |

### 7.2 `web/src/features/qy/lib/qy-guards.ts`（新建，纯 `.ts` 无 JSX）

```ts
import { redirect } from '@tanstack/react-router'

import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

/** 管理员守卫。仅供 qy 自己的路由使用，不去重构原项目已有的 8 处 inline 守卫。 */
export function requireQyAdmin(): void {
  const { auth } = useAuthStore.getState()
  if (!auth.user || auth.user.role < ROLE.ADMIN) throw redirect({ to: '/403' })
}

/** 超管守卫，预留给「佣金配置」这类涉及资金规则的页面（见 §9 建议） */
export function requireQySuperAdmin(): void {
  const { auth } = useAuthStore.getState()
  if (auth.user?.role !== ROLE.SUPER_ADMIN) throw redirect({ to: '/403' })
}
```

### 7.3 组件内判定

```ts
import { useIsAdmin } from '@/hooks/use-admin'          // use-admin.ts:28-31
const isAdmin = useIsAdmin()
```

**不要**用 `web/src/lib/admin-permissions.ts` 的 `hasPermission(user, resource, action)`（`:75-83`）——它的资源目录由后端 `GET /api/authz/catalog` 提供，qy 的资源不在其中，`user.permissions.admin_permissions['qy_commission']` 恒为 `undefined` → 除超管外全部 `false`。若未来需要细粒度授权，必须先让后端把 qy 资源加进 catalog，属于二期。

### 7.4 前端权限只是 UX，不是安全边界

**所有 `/api/qy/*` 管理端点必须在后端独立鉴权**。前端守卫会被绕过（改 localStorage 里的 user.role 即可）。同时：**管理端列表接口返回的用户信息必须由后端脱敏**（用户名打码、邮箱打码），不要指望前端 `QyUserCell` 组件遮挡——前端遮挡在 devtools 里一览无余，与 D2「修复无权信息泄漏」的目标矛盾。

---

## 8. 共享组件清单：`web/src/features/qy/components/`

| 组件 | 文件 | 职责 | 底座 |
|---|---|---|---|
| `QyPageBoundary` | `qy-page-boundary.tsx` | **统一加载/错误/空态/未启用/新库降级**五态。`props.query` 传 `UseQueryResult`，内部按 `isLoading → Skeleton`、`error instanceof QyError → ErrorState(t(err.i18nKey))`、`data 为空 → EmptyState`、`db_healthy===false → 顶部 Alert` 分流 | `EmptyState`(`components/empty-state.tsx:43`)、`ErrorState`(`components/error-state.tsx:44`)、`auto-skeleton-react` |
| `QyAmountInput` | `qy-amount-input.tsx` | 金额输入。用户输显示币种 → 内部 `parseQuotaFromDollars()` 转 quota → **在输入框下方实时回显换算后的整数 quota 与反算金额**，防四舍五入误解。禁负、限位数、贴 min/max | `format.ts:83`、`Input` |
| `QyQuotaText` | `qy-quota-text.tsx` | 站内额度展示。`variant='ledger'`（明细，不缩写）/ `variant='hero'`（概览，缩写）。负数自动 `text-destructive` 并加 `-` | `formatQuotaWithCurrency`(`currency.ts:496`) |
| `QyFiatText` | `qy-fiat-text.tsx` | **法币金额展示（string 入参）**。带冻结汇率 tooltip：「按 1 USD = ¥7.30 于 2026-03-01 冻结」 | 自建 `Intl.NumberFormat`，**绝不走 `formatCurrencyFromUSD`**（见 §9.2） |
| `QyUserCell` | `qy-user-cell.tsx` | 用户脱敏展示：`#1024 · zh***ng`，点击 Popover 显示可复制的完整 ID；**永不展示邮箱/手机** | `MaskedValueDisplay`(`components/masked-value-display.tsx:43`)、`CopyButton` |
| `QyStatusBadge` | `qy-status-badge.tsx` | 统一状态色板（见下表），`copyable={false}` | `StatusBadge`(`components/status-badge.tsx:138`) |
| `QyReviewBar` | `qy-review-bar.tsx` | 审核操作条：通过 / 拒绝。**拒绝必须填理由（zod `.min(2)`）**，通过需二次确认；提交中禁用双击 | `ConfirmDialog`(`components/confirm-dialog.tsx:48`)、RHF + Zod |
| `QyDangerConfirm` | `qy-danger-confirm.tsx` | 资金二次确认弹窗：复述「收款人 ID + 脱敏名 + 金额 + 换算后 quota」，要求用户手输金额或勾选确认框才能提交 | `ConfirmDialog` + `destructive` |
| `QyTimeline` | `qy-timeline.tsx` | 单据时间线：提交 → 审核 → 打款/到账 → 完成/驳回。空节点灰显，当前节点高亮 | 自建（Tailwind） |
| `QyRefreshButton` | `qy-refresh-button.tsx` | 带 `isFetching` 转圈的刷新按钮，统一 `invalidateQueries` | `Button` |
| `QyDbDownBanner` | `qy-db-down-banner.tsx` | `db_healthy===false` 时的全局降级横幅，挂在 `qy/route.tsx` 的布局里 | `Alert` |
| `QyWalletEntryCard` | `qy-wallet-entry-card.tsx` | **给钱包页用的入口卡片**（划转 / 我的推广 / 提现三个跳转）。钱包模块只需 import + 渲染，把钱包页的 diff 压到 2 行 | `Card` + `Link` |

统一状态色板（写死在 `qy-status-badge.tsx`，全部页面必须复用）：

| 状态 | `StatusVariant` | i18n key |
|---|---|---|
| `pending` 待处理/审核中 | `warning` + `pulse` | `qy_common_st_pending` |
| `processing` 处理中（跨库中间态） | `info` + `pulse` | `qy_common_st_processing` |
| `success` 成功 | `success` | `qy_common_st_success` |
| `failed` 失败 | `danger` | `qy_common_st_failed` |
| `rejected` 已驳回 | `danger` | `qy_common_st_rejected` |
| `reversed` 已冲正（退款冲销佣金） | `violet` | `qy_common_st_reversed` |
| `frozen` 冻结中 | `neutral` | `qy_common_st_frozen` |
| `paid` 已打款 | `success` | `qy_common_st_paid` |

配套 lib（`web/src/features/qy/lib/`）：`qy-http.ts`、`qy-keys.ts`、`qy-format.ts`、`qy-guards.ts`、`qy-config-snapshot.ts`、`qy-config-query.ts`。
配套 hooks（`web/src/features/qy/hooks/`）：`use-qy-config.ts`、`use-qy-after-money-change.ts`、`use-qy-mutation.ts`（统一 `QyError` → toast 文案 + `isPending` 防重）。

> **lint 注意**：`web/.oxlintrc.json` 的 `react/only-export-components` 有一份写死的 `allowExportNames` 白名单（含 `getSiteSectionNavItems` 等 26 个名字）。qy 若在**含 JSX 的 `.tsx` 文件**里同时导出组件和非组件函数会报 error，而修改 `.oxlintrc.json` 意味着多改一个原项目文件。**规避方式：任何 `.tsx` 只导出组件；工具函数/常量/hook 一律放同目录的 `.ts` 文件。** 本清单已按此拆分。

---

## 9. 关键流程

### 9.1 冷启动 → 菜单点亮

1. `main.tsx:41` `import './i18n/config'` → `i18n.init()` 同步完成 → `registerQyResources(i18n)` 注入 `qy_*` 键。
2. `AppSidebar` 渲染 → `useSidebarView()` → `useSidebarData()` → `useQySidebarGroups()`。
3. `useQyConfig()` 先用 `localStorage['qy_enabled']` 的 `placeholderData` 渲染（**零闪烁**），同时发起 `GET /api/qy/config`。
4. 200 → `status='enabled'`，写 snapshot + localStorage → 重渲染，菜单出现。
   404 → `status='disabled'`，写 `'0'` → 返回 `[]`，菜单永不出现，**且不弹任何 toast**（`skipErrorHandler: true`）。
   503/网络错误 → `status='unknown'`，沿用上次 localStorage 值。
5. `⌘K` 命令面板（`command-menu.tsx:51`）自动继承同一份数据，无需额外处理。

### 9.2 进入 `/qy/*` 工作区

1. `_authenticated/route.tsx:26-33` 校验登录 → 未登录 redirect `/sign-in`。
2. `qy/route.tsx` `beforeLoad` → `ensureQueryData(qyConfigQueryOptions())`。命中缓存则零网络开销。
   - `enabled === false` → `throw redirect({to:'/404'})`。
   - 抛出非 redirect 异常 → **必须 `isRedirect(e)` 重抛后再吞**，否则 redirect 被 catch 吞掉导致守卫失效。
3. 若进的是 `/qy/admin/*`，再过 `qy/admin/route.tsx` 的 `requireQyAdmin()` → role<10 redirect `/403`。
4. `resolveSidebarView(pathname)`（`sidebar-view-registry.ts:41-43`）匹配 `/^\/qy(\/|$)/` → `AppSidebar` 换成 qy 工作区侧栏 + `← 返回控制台`。
5. `getQyNavGroups(t)` 内部按 `role` 与 `features` 裁剪（§3.5），**普通用户看不到任何管理入口的名称**。

### 9.3 资金类 mutation 的前端事务边界

```
① 用户点提交
② 前端生成 client_request_id = nanoid()（幂等键，随 body 发送）
③ mutation.isPending = true → 按钮禁用（防双击）+ 表单只读
④ POST /api/qy/transfer  { to_user_id, quota, client_request_id }
⑤ 成功 (success:true):
     a. toast.success(t('qy_tr_ok'))
     b. await useQyAfterMoneyChange()      ← invalidate ['qy'] + getSelf() + setUser + invalidate ['status']
     c. 关闭抽屉，导航到 /qy/transfer-logs
⑥ 失败分流（按 QyError.kind）:
     unavailable(503) → toast(t('qy_err_unavailable'))；**不 invalidate**，表单保留，允许重试（同一 client_request_id）
     business         → toast(err.rawMessage ?? t('qy_err_unknown'))；invalidate ['qy']（后端状态可能已变）
     conflict(409)    → toast(t('qy_err_conflict'))；强制 invalidate + 关闭表单
     network          → toast(t('qy_err_network'))；**必须 invalidate ['qy']**，因为请求可能已在服务端成功
⑦ 无论成败，isPending = false
```

**⑥ 的 network 分支是本流程最关键的一条**：网络超时下前端无法区分「没到服务端」与「到了且成功了」。**唯一正确做法是 invalidate 后让用户从服务端真值重新读取**，并在文案里明确「若已扣款请勿重复提交，请查看划转记录」。

### 9.4 管理端审核流程（并发冲突）

```
① 管理员 A、B 同时打开「提现审核」列表，同一单 id=88 状态 pending
② A 点「通过」→ PUT /api/qy/admin/withdraw/88/approve { version: 3 }
③ 后端乐观锁 UPDATE ... WHERE id=88 AND version=3 → 成功，version→4
④ B 点「通过」→ 同样带 version:3 → RowsAffected=0 → 后端返回 409
⑤ B 的前端：QyError.kind === 'conflict'
     → toast.error(t('qy_err_conflict'))
     → queryClient.invalidateQueries({ queryKey: qyKeys.adminWithdrawals(params) })
     → 关闭抽屉，列表自动刷新为最新状态
```

**前端契约**：所有 qy 审核类接口的请求体**必须携带 `version`（或 `updated_at`）**，且列表接口必须把该字段返回。这是前端唯一能防「两个管理员重复打款」的手段。

---

## 10. 并发与边界

| 场景 | 风险 | 处理 |
|---|---|---|
| 双击提交 | 重复扣款 | `mutation.isPending` 禁用按钮 + 客户端 `client_request_id`（nanoid）幂等键，后端唯一索引兜底 |
| 网络超时后重试 | 重复扣款 | 重试**复用同一个** `client_request_id`（存在 `useRef`，只有表单重置时才重新生成） |
| 余额查询与提交竞态 | 前端显示可转 100，实际已被另一标签页转走 | 前端**永不本地加减余额**，提交后无条件 `invalidateQueries` 重取；后端 `WHERE quota >= ?` + `RowsAffected` 才是真正的防线 |
| GET 去重误伤 | 两个分页参数不同的请求被合并 | 不会——`http-client.ts:58-60` 的 key 含 `JSON.stringify(config.params)`；且 react-query 的 queryKey 也含参数 |
| 401 刷新期间的 qy 请求 | 提交丢失 | 自动继承 `http-client.ts:105-118` 的 refresh-and-retry；`authRetry` 标记防死循环 |
| **法币金额精度** | JSON `number` 解析 `decimal(18,2)` 丢精度 | **后端法币金额一律返回 string**；前端只做展示与字符串比较，禁止 `parseFloat` 后再运算。展示用 `QyFiatText` 内部一次性 `Number()` 仅用于 `Intl.NumberFormat` |
| **法币双重换算** | 用 `formatCurrencyFromUSD` 展示已冻结汇率的法币 → 再乘一次当前 `usdExchangeRate` | `web/src/lib/currency.ts:75` 的 "Critical Rules #1: Never double-convert" 明确警告。**qy 法币一律用 `QyFiatText`（自建 Intl），禁止 import `formatCurrencyFromUSD`**。可在 CI 加 grep 检查：`grep -r "formatCurrencyFromUSD" src/features/qy` 必须为空 |
| 站内额度精度 | quota 是 int，前端 number 是 double | 安全整数上限 9.007e15，quota 500000/USD → 180 亿美元才溢出。**但金额相加求和一律由后端返回，前端不做累加**（避免多页求和） |
| 金额输入四舍五入 | CNY 模式 `parseQuotaFromDollars`（`format.ts:83-99`）内部 `Math.round(usd * quotaPerUnit)`，用户输 ¥1.005 与 ¥1.006 可能转出同一 quota | `QyAmountInput` **必须回显换算后的整数 quota**，并在确认弹窗里同时展示「输入金额」与「实际划转 quota」 |
| 输入负数 / 0 / 超限 | 提交后端垃圾请求 | zod：`.positive()`、`.max(limits.transfer_max_quota)`；换算后再校验 `quota >= 1`；`type='number'` + `inputMode='decimal'` + `min` |
| 输入科学计数法 / `Infinity` | `Number('1e999') === Infinity` | `parseQuotaFromDollars` 已有 `Number.isFinite` 守卫（`format.ts:84`），前端 zod 再加 `.finite()` |
| `status` 为空/未知枚举值 | 徽章渲染 `undefined` | `QyStatusBadge` 对未知值回落 `neutral` + 原样显示字符串，**绝不 crash** |
| 时间戳 | 时区/单位混乱 | 后端统一秒级 int64；前端统一 `formatTimestampToDate()`（`format.ts:138`），`0`/`-1` 自动显示 `-` |
| 分页越界 | 删除后当前页为空 | `useTableUrlState` 已提供 `ensurePageInRange`，必须调用 |
| 移动端 | 新列不会自动出现在卡片视图 | `MobileCardList` / `DataTableCardGrid` 需要在 columns 的 `meta` 里显式配置，**每个页面自查** |
| 新库降级期间的写操作 | 用户点了没反应 | `db_healthy===false` 时全局横幅 + **所有写按钮 `disabled` 并带 tooltip**，不要让用户提交后才吃 503 |

---

## 11. 前端页面总清单

### 11.1 命名规范（强制）

- URL 前缀：`/qy/`，管理端 `/qy/admin/`。
- 路由文件：`web/src/routes/_authenticated/qy/<name>/index.tsx`。
- 业务模块：`web/src/features/qy/<name>/`，管理端目录名加 `admin-` 前缀避免与用户端同名。
- 组件命名：`Qy` 前缀 PascalCase（`QyTransferLogs`）；工具/类型文件 kebab-case。
- i18n：`qy_<domain>_<name>`。
- query key：`['qy', ...]`。

### 11.2 用户端（7 个）

| 页面 | 路由 | 路由文件 | 业务目录 | 权限 | 菜单位置 | feature flag |
|---|---|---|---|---|---|---|
| 我的推广（邀请看板） | `/qy/affiliate` | `routes/_authenticated/qy/affiliate/index.tsx` | `features/qy/affiliate/` | 登录 | 工作区 › 我的推广 **（工作区默认页）** | `affiliate` |
| 已邀请用户 | `/qy/invitees` | `.../qy/invitees/index.tsx` | `features/qy/invitees/` | 登录 | 工作区 › 我的推广 | `affiliate` |
| 余额划转 | `/qy/transfer` | `.../qy/transfer/index.tsx` | `features/qy/transfer/` | 登录 | 工作区 › 我的推广 | `transfer` |
| 划转记录 | `/qy/transfer-logs` | `.../qy/transfer-logs/index.tsx` | `features/qy/transfer-logs/` | 登录 | 工作区 › 我的推广 | `transfer` |
| 提现申请 | `/qy/withdraw` | `.../qy/withdraw/index.tsx` | `features/qy/withdraw/` | 登录 | 工作区 › 我的推广 | `withdraw_quota \|\| withdraw_fiat` |
| 提现记录 | `/qy/withdrawals` | `.../qy/withdrawals/index.tsx` | `features/qy/withdrawals/` | 登录 | 工作区 › 我的推广 | 同上 |
| 我的违规记录 | `/qy/violations` | `.../qy/violations/index.tsx` | `features/qy/violations/` | 登录 | 工作区 › 我的推广 | `violation` |

另：根侧边栏 `personal` 附近出现的**单一入口**「我的推广」→ `/qy/affiliate`（`activeUrls: ['/qy']`）；钱包页通过 `QyWalletEntryCard` 提供第二入口。

### 11.3 管理端（6 个）

| 页面 | 路由 | 路由文件 | 业务目录 | 权限 | 菜单位置 | feature flag |
|---|---|---|---|---|---|---|
| 佣金配置 | `/qy/admin/commission` | `.../qy/admin/commission/index.tsx` | `features/qy/admin-commission/` | ADMIN（建议 SUPER_ADMIN，见 §12） | 工作区 › 推广管理 **（管理区默认页）** | `affiliate` |
| 佣金审核 | `/qy/admin/commission-review` | `.../qy/admin/commission-review/index.tsx` | `features/qy/admin-commission-review/` | ADMIN | 工作区 › 推广管理 | `affiliate` |
| 提现审核 | `/qy/admin/withdrawals` | `.../qy/admin/withdrawals/index.tsx` | `features/qy/admin-withdrawals/` | ADMIN | 工作区 › 推广管理 | `withdraw_*` |
| 违规规则配置 | `/qy/admin/violation-rules` | `.../qy/admin/violation-rules/index.tsx` | `features/qy/admin-violation-rules/` | ADMIN | 工作区 › 推广管理 | `violation` |
| 违规记录 | `/qy/admin/violations` | `.../qy/admin/violations/index.tsx` | `features/qy/admin-violations/` | ADMIN | 工作区 › 推广管理 | `violation` |
| 可用率监控 | `/qy/admin/availability` | `.../qy/admin/availability/index.tsx` | `features/qy/admin-availability/` | ADMIN | 工作区 › 推广管理 | `availability` |

根侧边栏第二个入口「推广管理」→ `/qy/admin/commission`（`requiredRole: ROLE.ADMIN`，`activeUrls: ['/qy/admin']`）。

### 11.4 重定向路由（2 个）

| 文件 | 行为 |
|---|---|
| `routes/_authenticated/qy/index.tsx` | `beforeLoad: () => { throw redirect({ to: '/qy/affiliate' }) }`（照抄 `system-settings/index.tsx:21-27`） |
| `routes/_authenticated/qy/admin/index.tsx` | `throw redirect({ to: '/qy/admin/commission' })` |

**总计新建路由文件 17 个，新建 feature 目录 13 个 + 1 个共享（`features/qy/{components,lib,hooks}`）。**

---

## 12. 我建议补充的（用户未提，但会返工）

### 12.1 【建议·高】佣金配置页提到 SUPER_ADMIN

「佣金比例」是直接决定平台出血速度的开关。原项目把「系统设置」整个工作区门槛设为 `ROLE.SUPER_ADMIN`（`system-settings/route.tsx:29`），而普通 ADMIN 只能管渠道/用户/兑换码。佣金配置在风险等级上等同于系统设置，建议 `requireQySuperAdmin`。若不采纳，至少要求配置页的保存操作走 `ConfirmDialog` + 记审计。

### 12.2 【建议·高】法币金额格式化独立，并加 CI 守卫

`web/src/lib/currency.ts` 的文档（`:75-79`）已明确列出 "Never double-convert" 为 Critical Rule #1，且项目里 `formatCurrencyFromUSD` / `formatLocalCurrencyAmount` 的误用是已被文档反复警告的坑。qy 的法币金额是**冻结汇率下的绝对值**，属于第三类（既不是 system USD 也不是 priceRatio 换算结果）。建议：

- 新建 `QyFiatText` + `formatQyFiat(amountStr, symbol, locale)`，内部**不 import `@/lib/currency` 的任何函数**。
- CI 加一条：`grep -rE "formatCurrencyFromUSD|formatQuotaWithCurrency" web/src/features/qy | grep -v qy-quota-text.tsx` 必须为空。

### 12.3 【建议·中】统一错误文案表（前置产出，别边写边编）

在 `web/src/i18n/qy/en.json` 里先把这 12 条定死，各页面禁止自造：

```
qy_err_disabled / qy_err_unavailable / qy_err_db_down / qy_err_forbidden
qy_err_conflict / qy_err_network / qy_err_unknown
qy_err_insufficient      余额不足
qy_err_limit_single      超过单笔限额
qy_err_limit_daily       超过当日累计限额
qy_err_self_transfer     不能转给自己
qy_err_recipient_invalid 收款人不存在或已禁用
```

### 12.4 【建议·中】空态文案清单（每页一条，避免出现裸「No Data」）

| 页面 | 空态文案 | Action |
|---|---|---|
| 我的推广 | 「还没有人通过你的链接注册」 | 复制邀请链接按钮 |
| 已邀请用户 | 「暂无邀请记录」 | 同上 |
| 划转记录 | 「暂无划转记录」 | 去划转 |
| 提现记录 | 「暂无提现申请」 | 去提现 |
| 我的违规记录 | 「没有违规记录 👍」 | 无（正面空态，用 `success` 语气而非中性） |
| 佣金审核 / 提现审核 | 「没有待处理的申请」 | 切到「全部」筛选 |
| 违规记录 | 「暂无违规记录」 | 去配置规则 |

### 12.5 【建议·中】路由级 `errorComponent`

`qy/route.tsx` 加 `errorComponent`，避免 qy 内部异常冒泡到 `__root.tsx:142-182` 的 `GeneralError`（那是全屏错误页，会让用户以为整站挂了）。qy 的错误应局限在工作区内容区，侧边栏保留可用。

### 12.6 【建议·中】防刷与限频（前端侧）

- 提现申请按钮成功后进入冷却（`useCountdown`，`web/src/hooks/use-countdown.ts` 已存在），60s 内禁止再次提交。
- 划转收款人**只允许按用户 ID 输入**，输入后调 `GET /api/qy/user/resolve?id=` 返回脱敏用户名做二次确认，**不提供用户名模糊搜索**——否则与 D2「消除信息泄漏」自相矛盾，等于开放用户枚举。这一点必须写进划转模块的需求，是本方案对它的硬约束。

### 12.7 【建议·低】开发流程检查项（写进 PR 模板）

```
- [ ] bun run copyright        # 新文件补 AGPL 头（scripts/add-copyright.mjs）
- [ ] bun run format           # oxfmt：无分号/单引号/printWidth 80/sortImports
- [ ] bun run lint             # 零 error
- [ ] bun run typecheck        # tsgo -b
- [ ] 未运行 bun run i18n:sync # qy 的键不在 locales/，跑了只会制造噪声
- [ ] routeTree.gen.ts 是 build 重生成的，不是手改的
- [ ] grep 检查：features/qy 内无 formatCurrencyFromUSD
- [ ] 新增 t() 键已加进 i18n/qy/en.json + zh.json
- [ ] knip 未报新增的孤儿文件
```

### 12.8 【建议·低】模块自述文档

新建 `web/src/features/qy/README.md`，把 §5.3（query key）、§6.1（i18n 键名）、§8（共享组件）、§10（金额精度红线）四张表贴进去。13 个页面由不同人写，没有这份约定必然各写各的。

---

## 13. 一页速查

```
路由体系          文件式（@tanstack/router-plugin/rspack，rsbuild.config.ts:92-97）
URL 前缀          /qy/*（用户）  /qy/admin/*（管理）
守卫              登录=_authenticated  启用=qy/route.tsx  管理=qy/admin/route.tsx （各一处，零重复）
根菜单            use-sidebar-data.ts navGroups 尾部 +1 行 ...useQySidebarGroups()
子菜单            sidebar-view-registry.ts SIDEBAR_VIEWS +1 元素，12 个子页面全挂这里
⚠️ 嵌套视图不做角色过滤，必须在 getQyNavGroups 内自行按 role 裁剪（use-sidebar-view.ts:67-75）
API               复用 @/lib/api 的 axios 实例，qyGet/qyMutate 包一层错误语义
query key         ['qy', ...] —— 资金操作后 invalidate ['qy'] 全量
未启用判定        GET /api/qy/config 返回 404（NoRoute→RelayNotFound）→ status='disabled' → 菜单返回 []
i18n              独立 src/i18n/qy/*.json + addResourceBundle，config.ts 只加 2 行，与 locales/ 零交集
键名              qy_<domain>_<name>，全小写下划线，禁点号（keySeparator 默认 '.'）
金额              站内额度走 formatQuotaWithCurrency；法币走自建 QyFiatText（禁双重换算）
原项目改动        4 个文件 / 6 行，全部只增（唯一 1 处修改是 SIDEBAR_VIEWS 数组）
routeTree.gen.ts  冲突即 checkout --theirs + cd web && bun run build 重生成，禁止手工合并
```
