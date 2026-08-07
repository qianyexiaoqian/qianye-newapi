-- group-namespace-report.sql —— 「用户分组 / 模型分组分家」上线前必须先跑的体检。
--
-- 这三组查询是 S0 的交付物,**不许跳过**。它们回答的三个问题,没有一个能靠推断得到:
-- 上一轮就有两份设计对同一批令牌给出了方向相反的判断(「它们今天已经在 503」
-- 与「它们有渠道、零影响」),而两者都超出了当时手上数据能支撑的范围。
--
-- 用法:对**主库**执行(不是扩展库)。同样的结论也可以在管理端
-- /api/qy/admin/group-namespace/report 一次拿到,这份 SQL 是给没装扩展、
-- 或者要在数据库侧独立复核的人用的。
--
-- ══════════════════ 方言说明(执行前必读,否则第一段就报错)══════════════════
--
-- 下面统一用 PostgreSQL 风格的双引号写标识符。**MySQL / SQLite 下必须整份把
-- `"` 换成反引号 `` ` ``**(或对 MySQL 加 SET sql_mode=ANSI_QUOTES)。
--
-- 需要引号的保留字有**两个**,不是一个:
--
--   "group"  users.group / tokens.group / abilities.group / channels.group
--   "key"    options.key —— 它在 MySQL 下是保留字(KEY),不引号直接 1064 语法错误。
--            仓库里 model/main.go 之所以维护 commonKeyCol,原因正是这条。
--
-- 布尔列不要写 `= 1`:abilities.enabled 在 PostgreSQL 下是 boolean,
-- `boolean = integer` 报 "operator does not exist"。三方言通用的写法是
-- `enabled = TRUE`(SQLite 认 TRUE 为 1,MySQL 认 TRUE 为 1)。
-- channels.status / tokens.status 是真正的整数列,`= 1` 三方言都成立。

-- ═══════════════════════ 1. 空分组令牌的分布 ═══════════════════════
--
-- 令牌没有指定分组时,UsingGroup 回落 users.group,再拿它去 abilities 选渠道。
-- 这些令牌就是「默认模型分组」会改变行为的**全部对象** —— 粒度是用户分组,
-- 不是令牌,所以配置的时候只需要看这张表有几行。
SELECT u."group" AS user_group,
       COUNT(*)  AS empty_group_tokens
FROM tokens t
JOIN users u ON u.id = t.user_id
WHERE t.deleted_at IS NULL
  AND u.deleted_at IS NULL
  AND t."group" = ''
  AND t.status = 1
GROUP BY u."group"
ORDER BY empty_group_tokens DESC;

-- ═══════════════════════ 2. 每个用户分组名是否兼作模型分组(has_route)═══════════════════════
--
-- 这是「空分组令牌今天到底是死是活」的直接判据:
--   abilities_enabled > 0  → 它兼作模型分组,今天能路由(配默认反而可能改变现状)
--   abilities_enabled = 0  → 它今天就在 503,配默认是纯修复
--
-- channels 侧的口径必须一起看:缓存模式(MEMORY_CACHE_ENABLED=true)下真实路由由
-- channels.group 决定,而 abilities 只提供外层键集合。两者背离正是
-- 「修复数据库一致性」(FixAbility)存在的理由,只看一个口径会让背离永远看不见。
-- channels.group 是逗号串,SQL 里拆不动,所以这里只统计精确等于的那一档
-- (多分组渠道请用管理端接口,它在 Go 侧展开)。
SELECT ug.user_group,
       ug.users,
       (SELECT COUNT(*) FROM abilities a
         WHERE a."group" = ug.user_group AND a.enabled = TRUE) AS abilities_enabled,
       (SELECT COUNT(*) FROM channels c
         WHERE c."group" = ug.user_group AND c.status = 1)  AS channels_exact_enabled
FROM (
    SELECT "group" AS user_group, COUNT(*) AS users
    FROM users WHERE deleted_at IS NULL GROUP BY "group"
) ug
ORDER BY ug.users DESC;

-- ═══════════════════════ 3. 有路由但没倍率(**真正的资金泄漏集合**)═══════════════════════
--
-- 这一组才是资损的主角,而不是「空分组令牌」那个人群:
-- 上游 GetGroupRatio 查不到分组名时静默返回 1,但**只有路由成功时才真的扣到钱**。
-- 所以泄漏集合 = (abilities 里有 enabled 行的模型分组) ∖ (options.GroupRatio 的键)。
--
-- GroupRatio 存在 options 表里(key='GroupRatio',value 是 JSON),SQL 拆不动 JSON,
-- 所以这里只输出左边那一半;右边请对照管理端「模型分组」页,或直接看
-- /api/qy/admin/group-namespace/report 的 route_without_ratio 字段(它在 Go 侧做差集)。
SELECT a."group"                  AS model_group,
       COUNT(DISTINCT a.channel_id) AS enabled_channels,
       COUNT(*)                   AS ability_rows
FROM abilities a
WHERE a.enabled = TRUE
GROUP BY a."group"
ORDER BY enabled_channels DESC;

-- 对照用:options 里那份 JSON 原文。人工与上一查询做差集。
SELECT value FROM options WHERE "key" = 'GroupRatio';
