# 前端架构与新页面接入

## 一、web/AGENTS.md 全文要点（前端约定）

文件: `C:\Users\Administrator\Desktop\qianye\qianye-newapi\web\AGENTS.md`（196 行，中文规范文档）

技术栈表（AGENTS.md:11-23）：Bun 包管理 / React 19 + TS / react-query + axios + Zustand / @tanstack/react-router / @tanstack/react-table + react-virtual / i18next / Day.js / **Base UI + Hugeicons + Tailwind + clsx + cva** / React Hook Form + Zod / @visactor/vchart。

关键强制约定：
- **3.1 i18n**：所有面向用户文案必须 `const { t } = useTranslation()`；非 React 环境用 `import { t } from 'i18next'`。常量里的 `SUCCESS_MESSAGES/ERROR_MESSAGES` 值本身就是 i18n key，展示时必须 `t(SUCCESS_MESSAGES.X)`。新增此类常量要在 `src/i18n/static-keys.ts` 登记。
- **3.2**：禁止 2 层以上嵌套三元；避免 `any`；`import type`；**改完 TS/TSX 必须跑 `bun run typecheck`**；**改动前必须对涉及文件跑 lint 且零 error**；**组件 props 非必要不解构，直接用 `props.xxx`**。
- **3.3**：单文件 > ~200 行考虑拆分。
- **3.5 状态**：Zustand `create`，用选择器订阅 `useAuthStore((s) => s.auth.user)`；store 放 `src/stores/`。
- **3.6 API**：`useQuery` 取数、`useMutation` 变更；`queryKey` 数组形式；`onSuccess` 里 `invalidateQueries`；统一用项目的 `api` axios 实例（含 `withCredentials: true`）；GET 默认去重。
- **3.8 路由**：TanStack Router，路由文件在 `src/routes/`，`createFileRoute` 定义；search 参数用 Zod + `validateSearch`；**`beforeLoad` 做认证与重定向**；嵌套用 `_authenticated` 前缀布局路由 + `<Outlet />`。
- **3.11 文件组织**：**功能模块放 `src/features/<feature>/`**，内含 `components/`、`lib/`、`hooks/`，以及按需 `api.ts`、`types.ts`、`constants.ts`、入口组件；通用组件 `src/components/`，通用工具 `src/lib/`。组件文件 PascalCase，工具/类型 kebab-case。
- **3.14 测试**：测试必须放模块专属 `__tests__/` 目录，禁止与源码平铺；Vitest + RTL。
- **3.16**：Rsbuild；脚本以 package.json 为准。

配套工具配置：
- `web/.oxfmtrc.json`：**无分号、单引号、JSX 单引号、printWidth 80、trailingComma es5、sortImports、sortTailwindcss**。
- `web/.oxlintrc.json`：`ignorePatterns` 含 `src/components/ui`、`src/routeTree.gen.ts`；`no-nested-ternary: error`、`import/no-cycle: error`、`no-console: warn`。
- `web/package.json:6-20` 脚本：`dev/build/typecheck(tsgo -b)/lint/format/copyright/i18n:sync/knip`。
- **每个 src 下新文件都要带 AGPL 版权头**（`scripts/add-copyright.mjs`，`bun run copyright` 自动加，`copyright:check` 校验）。

---

## 二、目录结构（web/src）

| 目录 | 职责 |
|---|---|
| `src/routes/` | **文件式路由**，每个文件一个 `createFileRoute`，只负责守卫 + search schema + 指向 feature 组件 |
| `src/routeTree.gen.ts` | **自动生成**（56KB，已提交到仓库），由 `@tanstack/router-plugin/rspack` 在 dev/build 时重写 |
| `src/features/<feature>/` | 业务模块：`index.tsx`(入口页组件)、`api.ts`、`types.ts`、`constants.ts`、`components/`、`lib/`、`hooks/`、`section-registry.tsx` |
| `src/components/ui/` | Base UI 封装的基础组件（60 个，shadcn 风格，**lint/format 忽略**） |
| `src/components/` | 项目自研通用组件（43 个 tsx） |
| `src/components/layout/` | 布局 + 侧边栏 + 顶栏 + 侧栏视图注册表 |
| `src/components/data-table/` | 通用表格体系（core/hooks/layout/static/toolbar） |
| `src/hooks/` | 全局 hooks（侧边栏、status、table url state、admin 判定等） |
| `src/stores/` | Zustand: `auth-store.ts`、`notification-store.ts`、`system-config-store.ts` |
| `src/lib/` | `api.ts`、`http-client.ts`、`roles.ts`、`admin-permissions.ts`、`utils.ts(cn)`、`format.ts`、`legacy-route.ts` 等 |
| `src/context/` | Provider：theme/font/direction/layout/search/theme-customization |
| `src/i18n/` | `config.ts`、`languages.ts`、`static-keys.ts`、`locales/*.json` |
| `src/styles/` | `index.css`、`theme.css`、`theme-presets.css` |
| `src/assets/`, `src/config/` | 图标/logo、字体配置 |

---

## 三、路由：文件式路由（File-Based Routing）

**是文件式**，不是代码式路由表。生成器配置在 `web/rsbuild.config.ts:89-100`：

```ts
tools: { rspack: { plugins: [ tanstackRouter({ target: 'react', autoCodeSplitting: isProd }) ] } }
```

Router 实例：`web/src/main.tsx:97-102`
```ts
const router = createRouter({ routeTree, context: { queryClient }, defaultPreload: 'intent', defaultPreloadStaleTime: 0 })
```

根路由 `web/src/routes/__root.tsx:142-182`：`createRootRouteWithContext<{ queryClient: QueryClient }>()`，`beforeLoad` 里做 legacy 路由重定向 + setup 检查 + `bootstrapAuthentication()`；`notFoundComponent: NotFoundError`、`errorComponent: GeneralError`。

登录布局路由 `web/src/routes/_authenticated/route.tsx:24-36`（**这是所有控制台页面的父路由**）：
```ts
export const Route = createFileRoute('/_authenticated')({
  beforeLoad: ({ location }) => {
    const { auth } = useAuthStore.getState()
    if (!auth.user || !auth.accessToken) {
      throw redirect({ to: '/sign-in', search: { redirect: location.href } })
    }
  },
  component: AuthenticatedLayout,
})
```

路由目录结构约定（`src/routes/`）：
- `_authenticated/xxx/index.tsx` → URL `/xxx`
- `_authenticated/xxx/$section.tsx` → URL `/xxx/:section`（项目用它做「页内分区/选项卡」）
- `(auth)/`、`(errors)/` 是 pathless group
- `_authenticated/system-settings/route.tsx` 是二级布局路由（`beforeLoad` 要求 `ROLE.SUPER_ADMIN`）

### 新增一个页面需要改哪些文件

| 文件 | 新增 or 改 |
|---|---|
| `src/routes/_authenticated/<name>/index.tsx` | **新增** |
| `src/features/<name>/**` | **新增** |
| `src/routeTree.gen.ts` | **自动重写**（无需手写；dev/build 时插件生成） |
| `src/hooks/use-sidebar-data.ts` | **改**（要出现在侧边栏时；唯一必须改的原有文件） |
| `src/i18n/locales/*.json` | **改**（7 个语言文件，或跑 `bun run i18n:sync` 自动补齐） |

### 完整例子：管理员页「兑换码」（路由 → 页面组件 → 数据获取）

**① 路由** `web/src/routes/_authenticated/redemption-codes/index.tsx:27-46`
```ts
const redemptionsSearchSchema = z.object({
  page: z.number().optional().catch(1),
  pageSize: z.number().optional().catch(10),
  filter: z.string().optional().catch(''),
  status: z.array(z.enum(REDEMPTION_FILTER_VALUES)).optional().catch([]),
})

export const Route = createFileRoute('/_authenticated/redemption-codes/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user || auth.user.role < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  validateSearch: redemptionsSearchSchema,
  component: Redemptions,
})
```

**② 页面组件** `web/src/features/redemption-codes/index.tsx:28-47`
```tsx
export function Redemptions() {
  const { t } = useTranslation()
  return (
    <RedemptionsProvider>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Redemption Codes')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions><RedemptionsPrimaryButtons /></SectionPageLayout.Actions>
        <SectionPageLayout.Content><RedemptionsTable /></SectionPageLayout.Content>
      </SectionPageLayout>
      <RedemptionsDialogs />
    </RedemptionsProvider>
  )
}
```

**③ 数据获取** `web/src/features/redemption-codes/components/redemptions-table.tsx:47`、`:62-153`
```ts
const route = getRouteApi('/_authenticated/redemption-codes/')   // 类型安全读 search
...
const { globalFilter, onGlobalFilterChange, columnFilters, onColumnFiltersChange,
        pagination, onPaginationChange, ensurePageInRange } = useTableUrlState({
  search: route.useSearch(), navigate: route.useNavigate(),
  pagination: { defaultPage: 1, defaultPageSize: isMobile ? 10 : 20 },
  globalFilter: { enabled: true, key: 'filter' },
  columnFilters: [{ columnId: 'status', searchKey: 'status', type: 'array' }],
})

const { data, isLoading, isFetching } = useQuery({
  queryKey: ['redemptions', pagination.pageIndex + 1, pagination.pageSize, globalFilter, statusFilterValue, refreshTrigger],
  queryFn: async () => { ... },
  placeholderData: (previousData) => previousData,
})

const { table } = useDataTable({ data, columns, manualPagination: true, manualFiltering: true, totalCount: data?.total ?? 0, ... })
return <DataTablePage table={table} columns={columns} isLoading={isLoading} ... />
```

**④ API** `web/src/features/redemption-codes/api.ts:19,35-41`
```ts
import { api } from '@/lib/api'
export async function getRedemptions(params: GetRedemptionsParams = {}): Promise<GetRedemptionsResponse> {
  const { p = 1, page_size = 10 } = params
  const res = await api.get(`/api/redemption/?p=${p}&page_size=${page_size}`)
  return res.data
}
```

**用户页版本**（无角色守卫）：`web/src/routes/_authenticated/keys/index.tsx:36-39` 只有 `validateSearch` + `component: ApiKeys`，无 `beforeLoad`。

---

## 四、侧边栏 / 菜单

### 菜单项定义文件（唯一）
`web/src/hooks/use-sidebar-data.ts:48-163`
```ts
export function useSidebarData(): SidebarData {
  const { t } = useTranslation()
  return { navGroups: [
    { id: 'chat',     title: t('Chat'),     items: [...] },   // :53-68
    { id: 'general',  title: t('General'),  items: [...] },   // :69-101
    { id: 'personal', title: t('Personal'), items: [...] },   // :102-117
    { id: 'admin',    title: t('Admin'),    items: [           // :118-160
        { title: t('Channels'), url: '/channels', icon: Radio },
        ...
        { title: t('System Info'), url: '/system-info', icon: ServerCog, requiredRole: ROLE.SUPER_ADMIN },  // :147-152
    ]},
  ]}
}
```

类型定义 `web/src/components/layout/types.ts`：
- `BaseNavItem`（:25-37）：`title`、`badge?`、`icon?: React.ElementType`、`activeUrls?`、`configUrls?`、**`requiredRole?: number`**（:36）
- `NavLink`（:42-46）= BaseNavItem + `url`
- `NavCollapsible`（:51-55）= BaseNavItem + `items[]`
- `NavChatPresets`（:60-64）= `type: 'chat-presets'`
- `NavGroup`（:74-78）= `{ id?, title, items: NavItem[] }`
- `SidebarView`（:120-129）= `{ id, pathPattern: RegExp, parent: {to,label}, getNavGroups: (t)=>NavGroup[] }`

### 管理员可见 vs 普通用户可见 —— 两层机制

**第一层：角色过滤**（`web/src/hooks/use-sidebar-view.ts:54-65`）
```ts
const role = userRole ?? ROLE.GUEST
const isAdmin = role >= ROLE.ADMIN
return configFilteredRoot
  .filter((group) => (group.id === 'admin' ? isAdmin : true))     // 整组按 id==='admin' 判定
  .map((group) => {
    const items = group.items.filter((item) => item.requiredRole === undefined || role >= item.requiredRole)
    ...
  })
```
即：**`id: 'admin'` 这个分组整体只对 role >= 10 可见**；单项可用 `requiredRole` 再收紧（如 SUPER_ADMIN=100）。

**第二层：后台开关过滤**（`web/src/hooks/use-sidebar-config.ts:275-311`）
`useSidebarConfig(navGroups)` 用「管理员 `status.SidebarModulesAdmin` × 用户 `auth.user.sidebar_modules`」双层 AND 过滤，映射表 `URL_TO_CONFIG_MAP`（:97-119）。
**关键：未在映射表中的 URL 默认可见**（:173-176）：
```ts
const mapping = URL_TO_CONFIG_MAP[url]
if (!mapping) {
  // No mapping config, default to visible (e.g. system settings and new features)
  return true
}
```
→ **新增页面不需要改 `use-sidebar-config.ts`**。

### 嵌套（drill-in）侧栏视图注册表
`web/src/components/layout/lib/sidebar-view-registry.ts:33`
```ts
const SIDEBAR_VIEWS: readonly SidebarView[] = [SYSTEM_SETTINGS_VIEW]
```
`resolveSidebarView(pathname)`（:41-43）按数组顺序匹配第一个 `pathPattern`。`AppSidebar` 注释明确说明（`app-sidebar.tsx:44-45`）：**「Adding a new nested view only requires registering a SidebarView in the registry; this component requires no changes.」**

示例视图 `web/src/components/layout/config/system-settings.config.ts:100-108`：
```ts
export const SYSTEM_SETTINGS_VIEW: SidebarView = {
  id: 'system-settings',
  pathPattern: /^\/system-settings(\/|$)/,
  parent: { to: '/dashboard/overview', label: 'Back to Dashboard' },
  getNavGroups: getSystemSettingsNavGroups,
}
```

**副作用红利**：命令面板 `web/src/components/command-menu.tsx:51` 直接复用 `useSidebarData()` + `getNavGroupsForPath()`，所以往 `use-sidebar-data.ts` 加一项，⌘K 搜索里自动出现，无需额外改动。

### 顶栏导航
`web/src/hooks/use-top-nav-links.ts`（后端 `status.HeaderNavModules` 驱动，硬编码 home/console/pricing/rankings/docs/about）；`web/src/components/layout/config/top-nav.config.ts:30` 的 `defaultTopNavLinks: TopNavLink[] = []` 是**空数组占位、注释明说「If you need fallback links, add them here」**——一个现成的顶栏扩展点。

---

## 五、API 调用约定

### axios 封装
`web/src/lib/http-client.ts:44-50`
```ts
export const api = axios.create({
  baseURL: '',
  withCredentials: true,
  headers: { 'Cache-Control': 'no-store' },
})
```
- **模块增强的自定义 config**（:31-40）：`skipBusinessError`、`skipErrorHandler`、`disableDuplicate`、`skipAuthRefresh`、`authRetry`、`acceptAuthRotation`
- **GET 去重**（:52-69）：按 `${sessionSID}:${url}?${params}` 做 in-flight 合并，可用 `disableDuplicate: true` 关闭
- **响应拦截器**（:80-142）：`success === false` 自动 `toast.error`；401 自动 `refreshAuthentication()` 重试一次，失败跳 `/sign-in`
- **请求拦截器**（:144-150）：自动注入 `Authorization: Bearer <accessToken>`

对外出口 `web/src/lib/api.ts:34` —— `export { api }`。**业务代码统一 `import { api } from '@/lib/api'`。**

后端约定的响应体形状：`{ success: boolean, message?: string, data?: T }`。

### react-query 全局配置
`web/src/main.tsx:53-94`
```ts
new QueryClient({
  defaultOptions: {
    queries: { retry: ..., refetchOnWindowFocus: false, staleTime: 10 * 1000 },
    mutations: { onError: (error) => { handleServerError(error); ... } },
  },
  queryCache: new QueryCache({ onError: (error) => { /* 500 → toast + navigate('/500') */ } }),
})
```
错误统一处理 `web/src/lib/handle-server-error.ts:25`：`export function handleServerError(error: unknown)`。

### 标准 GET 例子
```ts
// features/<feature>/api.ts
import { api } from '@/lib/api'
export async function getFoos(params: GetFoosParams): Promise<ApiResponse<FooPage>> {
  const res = await api.get('/api/foo/', { params })
  return res.data
}

// 组件里
const { data, isLoading, isFetching } = useQuery({
  queryKey: ['foos', page, pageSize, keyword],
  queryFn: () => getFoos({ p: page, page_size: pageSize, keyword }),
  placeholderData: (prev) => prev,
})
```

### 标准 POST / mutation 例子
`web/src/features/system-settings/auth/custom-oauth/hooks/use-custom-oauth-mutations.ts:31-57`
```ts
function useInvalidateOnSuccess() {
  const queryClient = useQueryClient()
  return { onSuccess: () => {
    queryClient.invalidateQueries({ queryKey: ['custom-oauth-providers'] })
    queryClient.invalidateQueries({ queryKey: ['status'] })
  }}
}

export function useCreateProvider() {
  const invalidate = useInvalidateOnSuccess()
  return useMutation({
    mutationFn: (data: Omit<CustomOAuthProvider, 'id'>) => createCustomOAuthProvider(data),
    onSuccess: (res) => { if (res.success) { toast.success(i18next.t('Provider created successfully')); invalidate.onSuccess() } },
    onError: (error: Error) => { toast.error(error.message || i18next.t('Failed to create provider')) },
  })
}
```

---

## 六、权限：前端如何判断管理员

角色常量 `web/src/lib/roles.ts:21-26`
```ts
export const ROLE = { GUEST: 0, USER: 1, ADMIN: 10, SUPER_ADMIN: 100 } as const
export type RoleValue = (typeof ROLE)[keyof typeof ROLE]
```
另有 `getRoleLabelKey(role?)`（:39）、`getRoleLabel(role?)`（:43）。

用户 store `web/src/stores/auth-store.ts`
- `AuthUser`（:29-56）：`id/username/display_name/email/role/status/group/quota/used_quota/...`、`sidebar_modules?: string`、`permissions?: UserPermissions`
- `UserPermissions`（:23-27）：`sidebar_settings?: boolean`、`sidebar_modules?`、`admin_permissions?: AdminCapabilities`
- `useAuthStore`（:95）：`state.auth.{ user, accessToken, accessExpiresAt, session, bootstrapState, setBundle, setUser, reset }`

三种判定方式：

1. **组件内 hook** `web/src/hooks/use-admin.ts:28-31`
```ts
export function useIsAdmin(): boolean {
  const { user } = useAuthStore((state) => state.auth)
  return (user?.role ?? 0) >= ROLE.ADMIN
}
```

2. **路由守卫**（非 hook 环境，用 `getState()`）—— 8 处现有用法，见 `redemption-codes/index.tsx:35-43`、`users/index.tsx:45`、`channels/index.tsx:40`、`models/index.tsx:29`、`models/$section.tsx:47`、`subscriptions/index.tsx:28`，以及 SUPER_ADMIN 级：`system-info/index.tsx:29`、`system-settings/route.tsx:29`。**注意：没有抽象出公共 guard 函数，8 处都是复制粘贴的 inline 代码。**

3. **细粒度能力** `web/src/lib/admin-permissions.ts:75-83`
```ts
export function hasPermission(user: AuthUser|null|undefined, resource: string, action: string): boolean {
  if (!user) return false
  if (user.role === ROLE.SUPER_ADMIN) return true
  return user.permissions?.admin_permissions?.[resource]?.[action] === true
}
```
资源/动作常量：`ADMIN_PERMISSION_RESOURCES = { CHANNEL: 'channel' }`（:26-28）、`ADMIN_PERMISSION_ACTIONS = { READ, OPERATE, WRITE, SENSITIVE_WRITE, SECRET_VIEW }`（:30-36）。目录（catalog）由后端 `GET /api/authz/catalog` 提供，前端**故意不重复定义**。

---

## 七、i18n 完整流程

- 初始化：`web/src/i18n/config.ts:42-62`，`supportedLngs: ['en','zhCN','fr','ru','ja','vi','zhTW']`，`fallbackLng: 'en'`，**`nsSeparator: false`**（key 里可以带冒号）。
- locales 目录：`web/src/i18n/locales/` — `en.json`、`zh.json`、`zh-TW.json`、`fr.json`、`ja.json`、`ru.json`、`vi.json`（每个 5232 行），加 `_reports/_sync-report.json`。
- **key 就是英文原文字面量**，扁平结构，全部在 `{ "translation": { ... } }` 下：
  ```jsonc
  // en.json:3675 / zh.json:3675
  "Redemption Codes": "Redemption Codes"
  "Redemption Codes": "兑换码"
  ```
  （AGENTS.md 3.1 建议用层级键名，但**实际代码库统一是英文原文当 key**，新代码应跟随实际做法。）

**新增文案标准流程**：
1. 组件里直接写 `t('My New Label')`。
2. 在 `src/i18n/locales/en.json` 加 `"My New Label": "My New Label"`，在 `zh.json` 加中文，其他 5 个语言可留待 sync 填充。
3. 若文案不是以 `t('...')` 字面量出现（常量、config、labelKey），**必须在 `web/src/i18n/static-keys.ts` 的 `STATIC_I18N_KEYS` 数组登记**（该数组 575 行，已有分区注释如 `// Sidebar views (drill-in workspaces)`、`// System settings sidebar`）。
4. 跑 `bun run i18n:sync`。

**`bun run i18n:sync` = `node scripts/sync-i18n.mjs` 做什么**（`web/scripts/sync-i18n.mjs`）：
- 自动挑选「叶子 key 最多」的 locale 作为 base（`main()` 内 `countLeafKeys` 排序）；
- `reorderLikeBase()` 把所有 locale 文件**按 base 的 key 顺序重排**，缺失的 key 用 `en` 的值填充；
- 目标文件里 base 没有的多余 key 抽到 `src/i18n/locales/_extras/<locale>.extras.json`；
- `isLikelyUntranslated()` 扫描「值与英文完全相同」的疑似未翻译项，输出到 `src/i18n/locales/_reports/<locale>.untranslated.json`（有 `BRAND_AND_LITERAL_KEYS` 白名单跳过品牌词）；
- 汇总写 `src/i18n/locales/_reports/_sync-report.json`；
- **会重写全部 7 个 locale 文件**（包括 en，做格式归一化）。

---

## 八、UI 组件库：Base UI + Tailwind

- `@base-ui/react` ^1.6.0（不是 Radix）。导入形如 `import { Tabs as TabsPrimitive } from '@base-ui/react/tabs'`。
- Base UI 的组合方式是 **`render={<X />}` prop**（替代 Radix 的 `asChild`），例：`<SidebarMenuButton render={<Link to={item.url} />}>`（`nav-group.tsx:130`）、`<DialogTrigger render={trigger} />`（`components/dialog.tsx:71`）。
- 样式：Tailwind v4（`@rsbuild/plugin-tailwindcss`），CSS 变量在 `src/styles/theme.css`；动态类名用 `cn()`（`@/lib/utils`）；变体用 `cva`（class-variance-authority）。
- shadcn 配置 `web/components.json`：`style: "base-nova"`、`iconLibrary: "hugeicons"`、别名 `@/components`、`@/components/ui`、`@/lib/utils`、`@/hooks`。
- 图标实际大量用 `lucide-react`（侧边栏全用它），也有 `@hugeicons/react`、`@lobehub/icons`。

### ✅ 现成的 Tabs 组件（**有**）
`web/src/components/ui/tabs.tsx:98`
```ts
export { Tabs, TabsList, TabsTrigger, TabsContent, tabsListVariants }
```
- `Tabs`（:24）：props = `TabsPrimitive.Root.Props`，额外支持 `orientation`（默认 `'horizontal'`，可 `'vertical'`），受控用 `value` + `onValueChange`
- `TabsList`（:57）：`variant?: 'default' | 'line'`（cva，:42-55）
- `TabsTrigger`（:72）= `TabsPrimitive.Tab`，用 `value` 属性
- `TabsContent`（:88）= `TabsPrimitive.Panel`

**实际用法示例**（`web/src/features/usage-logs/index.tsx:133-152`）：
```tsx
<Tabs value={viewScope} onValueChange={handleViewScopeChange}>
  <TabsList>
    <TabsTrigger value='all'>{t('All')}</TabsTrigger>
    <TabsTrigger value='self'>{t('Only Mine')}</TabsTrigger>
  </TabsList>
</Tabs>
```
另见 `features/auth/secure-verification/components/secure-verification-dialog.tsx:140-199`（Tabs+TabsContent 完整用法）、`features/channels/components/dialogs/fetch-models-dialog.tsx:437-470`。

> 注意：项目更常见的「选项卡」做法其实是 **URL 驱动的 `$section` 路由 + section-registry**（见下节），Tabs 只作为 UI 呈现层。

### ✅ 现成的 Modal / 弹窗组件（**有，共 4 套**）

| 组件 | 文件 | 说明 |
|---|---|---|
| **`Dialog`（高阶封装，推荐）** | `web/src/components/dialog.tsx:52` | **项目自研的一体化弹窗**，props：`title`、`description?`、`children`、`trigger?`、`footer?`、`contentHeight?`、`contentClassName/headerClassName/titleClassName/descriptionClassName/bodyClassName/footerClassName`、`initialFocus?`、`showCloseButton?`，其余透传给 Base UI Root（`open`/`onOpenChange`）。内置滚动区、最大高度、进出场动画 |
| 原子 Dialog | `web/src/components/ui/dialog.tsx:162-173` | `Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogOverlay, DialogPortal, DialogTitle, DialogTrigger` |
| **`ConfirmDialog`** | `web/src/components/confirm-dialog.tsx:48` | 确认框。props：`open`、`onOpenChange`、`title`、`desc`、`confirmText?`、`cancelBtnText?`、`destructive?`、`handleConfirm`、`isLoading?`、`disabled?`、`className?`、`children?` |
| `AlertDialog` 原子 | `web/src/components/ui/alert-dialog.tsx:192-205` | 12 个导出 |
| `Sheet`（侧滑抽屉） | `web/src/components/ui/sheet.tsx:157-166` | `Sheet, SheetTrigger, SheetClose, SheetContent, SheetHeader, SheetFooter, SheetTitle, SheetDescription`。项目的「新建/编辑」表单都用它（如 `redemptions-mutate-drawer.tsx`） |
| `Drawer`（vaul，移动端） | `web/src/components/ui/drawer.tsx` | — |

**抽屉布局工具** `web/src/components/drawer-layout.ts`：`sideDrawerContentClassName`(:24)、`sideDrawerHeaderClassName`(:30)、`sideDrawerFormClassName`(:36)、`sideDrawerFooterClassName`(:42)、`sideDrawerSectionClassName`(:48)、`sideDrawerSwitchItemClassName`(:54)、`SideDrawerSection`(:60)、`SideDrawerSectionHeader`(:71)。

**弹窗状态管理 hooks** `web/src/hooks/use-dialog.ts`：
- `useDialog(initialOpen=false): readonly [boolean, DialogHandlers]`（:65）
- `useDialogState<T>(initialState=null): readonly [T|null, Dispatch<SetStateAction<T|null>>, DialogStateHandlers]`（:97，**default export**）
- `useDialogs<T extends string>(): DialogsHandlers<T>`（:131）

项目的标准弹窗编排模式（Provider + Dialogs 聚合组件）：
`redemptions-provider.tsx:38-63` 提供 `{ open: 'create'|'update'|'delete'|null, setOpen, currentRow, setCurrentRow, refreshTrigger, triggerRefresh }`，`redemptions-dialogs.tsx:23-37` 根据 `open` 渲染对应弹窗。

### 其他通用组件
- **表格**：`web/src/components/data-table/index.ts` 导出 `DataTablePage`(布局级一站式)、`useDataTable`、`DataTableToolbar`、`DataTablePagination`、`DataTableColumnHeader`、`BadgeCell`、`TruncatedCell`、`StaticDataTable`、`MobileCardList`、`DataTableCardGrid`、`DataTableBulkActions`、`useDataTableViewMode`、`useTableUrlState`(在 hooks)，以及 `DISABLED_ROW_DESKTOP` / `DISABLED_ROW_MOBILE` 常量。
- **表单**：`web/src/components/ui/form.tsx` 导出 `Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage`（RHF 封装）。Zod schema 惯例放 `features/<f>/lib/<x>-form.ts`，导出 `get<X>FormSchema(t: TFunction)` 以支持 i18n 报错（见 `redemption-form.ts:34`）。
- 其他自研：`multi-select`、`tag-input`、`date-picker`、`datetime-picker`、`json-code-editor`、`json-editor`、`copy-button`、`long-text`、`truncated-text`、`empty-state`、`error-state`、`loading-state`、`status-badge`、`group-badge`、`coming-soon`、`password-input`、`masked-value-display`。

### 页面骨架组件（新页面必用）
`web/src/components/layout/components/section-page-layout.tsx:57`
```tsx
export function SectionPageLayout(props: { children: ReactNode; fixedContent?: boolean })
// 具名插槽（compound component，:119-122）
SectionPageLayout.Title / .Actions / .Content / .Breadcrumb
```
`fixedContent: true` → 内容区 `overflow-hidden`（表格自撑满）；`false` → `overflow-auto`。底部有 footer portal 容器（配合 `PageFooterPortal`）。

---

## 九、新增页面的标准步骤

### A. 新增一个「管理员页面」（例：`/qy-foo`）

**新建文件（4 类）**

1. `web/src/features/qy-foo/api.ts`
```ts
import { api } from '@/lib/api'
export async function getFoos(params: GetFoosParams): Promise<GetFoosResponse> {
  const res = await api.get('/api/qy/foo/', { params })
  return res.data
}
```
2. `web/src/features/qy-foo/types.ts` / `constants.ts` / `components/*` / `lib/*`
3. `web/src/features/qy-foo/index.tsx` —— 导出 `export function QyFoo()`，用 `SectionPageLayout` + `DataTablePage`
4. `web/src/routes/_authenticated/qy-foo/index.tsx`：
```ts
export const Route = createFileRoute('/_authenticated/qy-foo/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user || auth.user.role < ROLE.ADMIN) throw redirect({ to: '/403' })
  },
  validateSearch: qyFooSearchSchema,   // zod
  component: QyFoo,
})
```

**必须改动的原有文件（最小集合 = 2 个 + 1 个自动生成）**

| 文件 | 改动 |
|---|---|
| `web/src/hooks/use-sidebar-data.ts` | 在 `id: 'admin'` 分组（:118-160）的 `items` 数组里加一项 `{ title: t('Foo'), url: '/qy-foo', icon: XXX }`（**加进这个组就自动只对 role>=10 可见**） |
| `web/src/i18n/locales/en.json` + `zh.json`（+ 另 5 个或跑 sync） | 加新文案 key |
| `web/src/routeTree.gen.ts` | 插件自动重写，不手改 |

**不需要改**：`use-sidebar-config.ts`（未映射 URL 默认可见）、`app-sidebar.tsx`、`command-menu.tsx`（自动继承）、`nav-group.tsx`、`authenticated-layout.tsx`。

### B. 新增一个「用户页面」（例：`/qy-bar`）

与 A 完全相同，只有两处差别：
1. 路由文件**不写 `beforeLoad`**（`_authenticated` 父路由已保证登录），只留 `validateSearch` + `component`（参照 `routes/_authenticated/keys/index.tsx:36-39`）。
2. 侧边栏项加到 `use-sidebar-data.ts` 的 `id: 'general'`（:69）或 `id: 'personal'`（:102）分组里。

**同样只需改 2 个原有文件**：`use-sidebar-data.ts` + locales。

### C. 如果新功能是「一组页面」（多个子页面）

推荐用 **drill-in 嵌套侧栏视图**，只需：
1. 新建 `web/src/components/layout/config/qy-foo.config.ts`，导出 `QY_FOO_VIEW: SidebarView`（照抄 `system-settings.config.ts:100-108` 的结构）；
2. 新建 `web/src/routes/_authenticated/qy-foo/route.tsx`（布局路由 + 角色守卫）、`qy-foo/$section.tsx`、`qy-foo/index.tsx`（重定向到默认 section）；
3. 新建 `web/src/features/qy-foo/section-registry.tsx`，复用 `createSectionRegistry`（`web/src/features/system-settings/utils/section-registry.ts:49`）：
```ts
const registry = createSectionRegistry<QyFooSectionId, QyFooSettings>({
  sections: QY_FOO_SECTIONS,       // [{ id, titleKey, build(settings) }]
  defaultSection: 'overview',
  basePath: '/qy-foo',
  urlStyle: 'path',                // '/qy-foo/<id>'，另可 'query' → '/qy-foo?section=<id>'
})
```
4. **只改 2 处原有文件**：`sidebar-view-registry.ts:33` 的 `SIDEBAR_VIEWS` 数组加一个元素 + `use-sidebar-data.ts` 加一个入口链接。

---

## 十、【扩展点建议】

### 干净的挂载点（改一处挂载最多功能）

| 优先级 | 挂载点 | file:line | 改动量 | 挂载能力 |
|---|---|---|---|---|
| ★★★★★ | 侧边栏菜单 | `web/src/hooks/use-sidebar-data.ts:52` 的 `navGroups` 数组 | 数组里插元素 | **一处改动同时点亮：侧边栏项 + ⌘K 命令面板项 + 折叠态下拉项 + 移动端抽屉项**（`command-menu.tsx:51`、`app-sidebar.tsx:67`、`nav-group.tsx` 全部复用同一数据源） |
| ★★★★★ | 路由 | 新建 `web/src/routes/_authenticated/<name>/**` | **纯新增文件**，零改原文件 | 文件式路由，`routeTree.gen.ts` 自动生成 |
| ★★★★☆ | 嵌套侧栏工作区 | `web/src/components/layout/lib/sidebar-view-registry.ts:33` `SIDEBAR_VIEWS` | 数组加 1 个元素 | 一次挂载整个「二级工作区」（N 个子页面 + 返回按钮），`AppSidebar` 无需任何改动（源码注释明确承诺） |
| ★★★★☆ | 页内分区/选项卡 | 新建 `features/<f>/section-registry.tsx` 调 `createSectionRegistry`（`web/src/features/system-settings/utils/section-registry.ts:49`） | 纯新增 | 一个 registry 同时产出：`sectionIds`（路由校验）、`getSectionNavItems(t)`（侧栏/Tabs 数据）、`getSectionContent(id, settings)`（内容渲染） |
| ★★★☆☆ | 顶栏公共导航 | `web/src/components/layout/config/top-nav.config.ts:30` `defaultTopNavLinks` | 空数组，注释邀请填充 | 加公共（未登录可见）入口 |
| ★★★☆☆ | API 层 | `web/src/lib/http-client.ts:44` 的 `api` 实例 | 零改动，直接 import | 新 feature 的 `api.ts` 直接 `import { api } from '@/lib/api'` 调 `/api/qy/**`；鉴权头、401 刷新、错误 toast、GET 去重全部自动继承 |

### 需要改动的原有文件 —— 最小集合

**必改（每个新增页面）：**
1. `web/src/hooks/use-sidebar-data.ts` —— 加菜单项（唯一无法避开的改动；纯数组插入，上游合并冲突概率低但存在）
2. `web/src/i18n/locales/en.json` + `zh.json`（+ `fr/ja/ru/vi/zh-TW.json`，或 `bun run i18n:sync` 生成）

**条件性改（按需）：**
3. `web/src/components/layout/lib/sidebar-view-registry.ts:33` —— 仅当做二级工作区
4. `web/src/i18n/static-keys.ts` —— 仅当新文案不是 `t('字面量')` 形式（常量/labelKey）
5. `web/src/components/layout/config/top-nav.config.ts:30` —— 仅当加公共顶栏入口

**自动重写（无需手改，但会产生 diff）：**
6. `web/src/routeTree.gen.ts` —— 已提交到仓库；合并上游时**大概率冲突**，处理办法：冲突时直接跑 `bun run dev` / `bun run build` 让插件重新生成，或删除后重生成。**建议在 fork 的合并流程文档里写明这一条。**

**明确不需要改：**
- `use-sidebar-config.ts`（未映射 URL 默认可见，见 :173-176）
- `app-sidebar.tsx` / `nav-group.tsx` / `command-menu.tsx` / `authenticated-layout.tsx` / `main.tsx` / `__root.tsx` / `_authenticated/route.tsx` / `http-client.ts` / `api.ts`

### 进一步降低冲突的建议做法

1. **把新功能的菜单收敛成一个「工作区」**：`use-sidebar-data.ts` 里只加 **1 行**（一个 `{ title, url: '/qy-console/xxx', icon, activeUrls: ['/qy-console'] }`），其余 N 个子页面全部通过新建的 `QY_CONSOLE_VIEW: SidebarView` 挂在 `SIDEBAR_VIEWS` 数组里 —— **两个原有文件各改 1 行，即可挂载任意多的新页面**。
2. **新增 `web/src/components/layout/config/qy-*.config.ts` 与 `web/src/features/qy-*/`**，用 `qy-` 前缀命名，与上游文件天然不重名。
3. **角色守卫目前是复制粘贴的**（8 处 inline `if (!auth.user || auth.user.role < ROLE.ADMIN) throw redirect({to:'/403'})`）。新页面建议**新建** `web/src/lib/qy-route-guards.ts` 导出 `requireAdmin()` / `requireSuperAdmin()`，只在自己的新路由文件里用，不去重构原有 8 处（避免制造冲突）。
4. **i18n 冲突缓解**：新文案 key 用带前缀的命名（如 `"qy.foo.title"`）而非纯英文原文，能显著降低与上游 locale 文件的行级冲突；但注意 `config.ts:50` 设了 `nsSeparator: false`，冒号安全，点号会被当作嵌套分隔（`keySeparator` 未显式关闭 → 默认 `.` 生效），所以**若要用点号层级键，需在 locales 里写成嵌套对象**，或改用 `"qy_foo_title"` 这类下划线扁平键。
5. **独立配置驱动的菜单可见性**：如果新功能需要「后台开关」，不要去改 `URL_TO_CONFIG_MAP`（`use-sidebar-config.ts:97`），而是在新建的 SidebarView / 菜单项上用自己的 `requiredRole` 或自建 hook 从新的 YAML 配置接口（`/api/qy/config`）读开关后过滤 —— `use-sidebar-config.ts:173-176` 的「未映射默认可见」保证了不会被原逻辑误杀。
