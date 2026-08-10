-- qianye/scripts/cleanup_test_fixtures.sql
--
-- 【本脚本从未被执行过】。它只清理**测试夹具**,由项目方自行决定何时执行。
--
-- 判据只有一条:主库 users.username 以 `qy-` 开头的账号,以及只由这些账号
-- 产生的从属数据。真人账号、真人令牌、真人订阅、真人日志、审计流水
-- 一律不在删除范围内。
--
-- 执行前请先跑「校验」一节确认命中集合,再跑「删除」一节。
-- 审计表(qy_ext.qy_audit_logs)是 append-only 的仲裁凭据,**任何情况下都不删**。
--
-- 用法:
--   /c/mysql80/mysql-8.0.28-winx64/bin/mysql.exe -h127.0.0.1 -P3307 -uroot \
--       --default-character-set=utf8mb4 < qianye/scripts/cleanup_test_fixtures.sql

-- =====================================================================
-- 一、校验(只读)。先看清楚要删什么。
-- =====================================================================
SELECT 'users(qy-)'            AS what, COUNT(*) AS n FROM qianye_newapi.users  WHERE username LIKE 'qy-%'
UNION ALL SELECT 'tokens(qy- 用户的)',  COUNT(*) FROM qianye_newapi.tokens t JOIN qianye_newapi.users u ON u.id=t.user_id WHERE u.username LIKE 'qy-%'
UNION ALL SELECT 'subs(qy- 用户的)',    COUNT(*) FROM qianye_newapi.user_subscriptions s JOIN qianye_newapi.users u ON u.id=s.user_id WHERE u.username LIKE 'qy-%'
UNION ALL SELECT 'logs(qy- 用户的)',    COUNT(*) FROM qianye_newapi.logs l JOIN qianye_newapi.users u ON u.id=l.user_id WHERE u.username LIKE 'qy-%'
UNION ALL SELECT 'ext:invite_relation', COUNT(*) FROM qy_ext.qy_invite_relation
UNION ALL SELECT 'ext:accrual',         COUNT(*) FROM qy_ext.qy_commission_accrual
UNION ALL SELECT 'ext:settlement',      COUNT(*) FROM qy_ext.qy_commission_settlement
UNION ALL SELECT 'ext:balance',         COUNT(*) FROM qy_ext.qy_commission_balance
UNION ALL SELECT 'ext:violation_record',COUNT(*) FROM qy_ext.qy_violation_record;

-- 佣金三张表当前只有 391 / 1622 / 1946 三行余额。**391 = SCPO5 是真人**,
-- 下面的删除按 qy- 前缀过滤,不会碰到它。执行前请确认这一行仍然成立:
SELECT b.user_id, u.username, IF(u.username LIKE 'qy-%','TEST','REAL — 不要删') AS kind
FROM qy_ext.qy_commission_balance b LEFT JOIN qianye_newapi.users u ON u.id = b.user_id;

-- =====================================================================
-- 二、删除。顺序:先从属数据,后账号本身。
-- =====================================================================
-- START TRANSACTION;   -- 跨库(qianye_newapi + qy_ext)不能靠单个事务保证原子性,
--                      -- 两库分别执行,失败时按本文件顺序重跑即可(全部是幂等删除)。

-- 2.1 扩展库:只删 qy- 账号相关的行
DELETE r FROM qy_ext.qy_violation_record r
  JOIN qianye_newapi.users u ON u.id = r.user_id WHERE u.username LIKE 'qy-%';
DELETE p FROM qy_ext.qy_violation_payload p
  LEFT JOIN qy_ext.qy_violation_record r ON r.id = p.record_id WHERE r.id IS NULL;
DELETE c FROM qy_ext.qy_violation_counter c
  JOIN qianye_newapi.users u ON u.id = c.user_id WHERE u.username LIKE 'qy-%';
DELETE b FROM qy_ext.qy_violation_ban b
  JOIN qianye_newapi.users u ON u.id = b.user_id WHERE u.username LIKE 'qy-%';

DELETE a FROM qy_ext.qy_commission_accrual a
  JOIN qianye_newapi.users u ON u.id = a.inviter_id WHERE u.username LIKE 'qy-%';
DELETE s FROM qy_ext.qy_commission_settlement s
  JOIN qianye_newapi.users u ON u.id = s.user_id WHERE u.username LIKE 'qy-%';
DELETE b FROM qy_ext.qy_commission_balance b
  JOIN qianye_newapi.users u ON u.id = b.user_id WHERE u.username LIKE 'qy-%';
DELETE r FROM qy_ext.qy_invite_relation r
  JOIN qianye_newapi.users u ON u.id = r.invitee_id WHERE u.username LIKE 'qy-%';

-- 2.2 主库:令牌 / 订阅 / 日志 / 账号
DELETE t FROM qianye_newapi.tokens t
  JOIN qianye_newapi.users u ON u.id = t.user_id WHERE u.username LIKE 'qy-%';
DELETE s FROM qianye_newapi.user_subscriptions s
  JOIN qianye_newapi.users u ON u.id = s.user_id WHERE u.username LIKE 'qy-%';
DELETE p FROM qianye_newapi.subscription_pre_consume_records p
  JOIN qianye_newapi.users u ON u.id = p.user_id WHERE u.username LIKE 'qy-%';
DELETE l FROM qianye_newapi.logs l
  JOIN qianye_newapi.users u ON u.id = l.user_id WHERE u.username LIKE 'qy-%';
-- 邀请关系:把指向 qy- 账号的 inviter_id 清零(真人不会指向测试账号,这一条通常命中 0 行)
UPDATE qianye_newapi.users v
  JOIN qianye_newapi.users i ON i.id = v.inviter_id
  SET v.inviter_id = 0 WHERE i.username LIKE 'qy-%';
DELETE FROM qianye_newapi.users WHERE username LIKE 'qy-%';

-- 2.3 只由测试建出来的分组/渠道(执行前请人工确认这些名字确实没有真人在用)
-- DELETE FROM qianye_newapi.channels WHERE name LIKE 'qy-%';
-- DELETE FROM qy_ext.qy_violation_ban_policy WHERE user_group LIKE 'qy-%' AND is_default = 0;

-- =====================================================================
-- 三、执行后复核
-- =====================================================================
SELECT COUNT(*) AS should_be_zero FROM qianye_newapi.users WHERE username LIKE 'qy-%';
-- 佣金恒等式必须仍然成立(此时只剩真人 391 那一行)
SELECT b.user_id,
       IFNULL(a.ss,0)                              AS sum_settled,
       IFNULL(s.net,0) + b.unsettled_amount        AS net_plus_carry,
       b.total_earned_quota - b.total_clawback_quota AS earned_minus_clawback,
       b.available_quota + b.frozen_quota + b.withdrawn_quota AS a_f_w
FROM qy_ext.qy_commission_balance b
LEFT JOIN (SELECT user_id, SUM(granted_quota)-SUM(reclaimed_quota) net
             FROM qy_ext.qy_commission_settlement GROUP BY user_id) s ON s.user_id = b.user_id
LEFT JOIN (SELECT inviter_id, SUM(settled_amount) ss
             FROM qy_ext.qy_commission_accrual GROUP BY inviter_id) a ON a.inviter_id = b.user_id;
