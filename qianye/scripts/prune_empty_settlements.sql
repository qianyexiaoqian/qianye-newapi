-- qianye/scripts/prune_empty_settlements.sql
--
-- 【本脚本从未被执行过,也不建议执行】。删账本行不可逆。
--
-- 目标:qy_commission_settlement 里 granted_quota = 0 且 reclaimed_quota = 0
-- 的"空结算单"。它们由旧判据(accrualCount > 0 也落单)产生;修复之后
-- 不会再有新的空行,存量清不清是纯粹的整洁问题,收益 = 三位数的行。
--
-- ┌─ 先看清楚"空行"是哪一类 ────────────────────────────────────────┐
-- │ granted_quota = 0 的行有 11 行,但其中 8 行 reclaimed_quota > 0 —— │
-- │ 那是**冲正**,是钱真的被收回去了,是账本行,一行都不能删。         │
-- │ 真正的空行只有 3 行(id 1 / 7 / 10),判据必须是两列同时为 0。      │
-- └───────────────────────────────────────────────────────────────────┘
--
-- ┌─ 为什么删掉它们不会破坏对账 ──────────────────────────────────────┐
-- │ 全仓只有两处读 qy_commission_settlement(其余都是写):             │
-- │   1. settle.go dailyRemaining  SUM(granted_quota) 按 user + UTC 日 │
-- │   2. api_admin.go 账本体检     SUM(granted_quota - reclaimed_quota)│
-- │ 两处都是 SUM,没有 COUNT(*)、没有 MAX(created_at)、没有列表接口。  │
-- │ 两列同时为 0 的行对两个 SUM 的贡献恰好是 0,删除前后一字不差。      │
-- │ "上次结算时间"取自 qy_commission_balance.last_settled_at,不来自本表。│
-- │ 校验一节的 A/B 两问会把这两点在真实数据上再证一遍。                 │
-- └───────────────────────────────────────────────────────────────────┘
--
-- 用法:
--   /c/mysql80/mysql-8.0.28-winx64/bin/mysql.exe -h127.0.0.1 -P3307 -uroot \
--       --default-character-set=utf8mb4 < qianye/scripts/prune_empty_settlements.sql

-- =====================================================================
-- 一、校验(只读)。三问都答对了才允许往下走。
-- =====================================================================

-- A. 候选行是哪些?两列同时为 0 才算,冲正行必须不在其中。
SELECT id, user_id, settle_no, accrual_count, delta_amount,
       carry_before, carry_after, granted_quota, reclaimed_quota, fiat_delta, remark,
       FROM_UNIXTIME(created_at) AS created
FROM qy_ext.qy_commission_settlement
WHERE granted_quota = 0 AND reclaimed_quota = 0
ORDER BY id;

-- B. 候选行对两个 SUM 口径的贡献必须恰好是 0。这两行输出都必须是 0。
SELECT 'contribution_to_ledger_check' AS k,
       COALESCE(SUM(granted_quota - reclaimed_quota), 0) AS must_be_zero
FROM qy_ext.qy_commission_settlement WHERE granted_quota = 0 AND reclaimed_quota = 0
UNION ALL
SELECT 'contribution_to_daily_cap',
       COALESCE(SUM(granted_quota), 0)
FROM qy_ext.qy_commission_settlement WHERE granted_quota = 0 AND reclaimed_quota = 0;

-- C. 候选行里 remark 必须全为空。非空意味着这一行装着 int32 触顶说明 ——
--    那是只有结算单装得下的信息,装着它的行不是空行,必须排除。
SELECT COUNT(*) AS must_be_zero_rows_with_remark
FROM qy_ext.qy_commission_settlement
WHERE granted_quota = 0 AND reclaimed_quota = 0 AND remark <> '';

-- D. 删除前的三条恒等式基线。把这一段的输出**存下来**,删除后要逐字比对。
--    I1  Σ计佣已结算 − Σ结算净额 − 未结算余数 = 0
--    I2  可用 = 累计发放 − 累计回收 − 冻结 − 已提现
--    I3  法币余额非负,且额度为 0 时法币必须也为 0
SELECT b.user_id,
       IFNULL(a.ss, 0) - IFNULL(s.net, 0) - b.unsettled_amount AS i1_drift,
       b.available_quota
         - (b.total_earned_quota - b.total_clawback_quota - b.frozen_quota - b.withdrawn_quota) AS i2_drift,
       IF(b.available_fiat < 0 OR (b.available_quota = 0 AND b.available_fiat <> 0), 'BAD', 'OK') AS i3
FROM qy_ext.qy_commission_balance b
LEFT JOIN (SELECT user_id, SUM(granted_quota) - SUM(reclaimed_quota) net
             FROM qy_ext.qy_commission_settlement GROUP BY user_id) s ON s.user_id = b.user_id
LEFT JOIN (SELECT inviter_id, SUM(settled_amount) ss
             FROM qy_ext.qy_commission_accrual WHERE status <> 'voided' GROUP BY inviter_id) a
       ON a.inviter_id = b.user_id
ORDER BY b.user_id;
-- 备份库实测:user 1622 的 i1_drift = -0.3663,这是**删除本脚本之前**就存在的
-- 历史漂移(qy_commission_accrual 有 99 行被此前的清理删掉,其中一行的
-- settled_amount 已被一张仍然存在的结算单计入)。它与空行无关,删除前后
-- 应当**一字不变** —— 如果它变了,说明删错了行,立刻回滚。

-- =====================================================================
-- 二、删除。只在 A/B/C 三问都答对之后执行。
-- =====================================================================
-- START TRANSACTION;

-- 2.1 先摘引用:这三行被 3 条计佣行的 settlement_id 指着。
--     settlement_id 全仓没有读取方,置 0 与修复后新产生的行状态一致
--     (不落单的轮次本来就写 settlement_id = 0)。
--     必须先摘再删,否则留下的是指向不存在的单号的悬空引用。
UPDATE qy_ext.qy_commission_accrual a
  JOIN qy_ext.qy_commission_settlement s ON s.id = a.settlement_id
  SET a.settlement_id = 0
WHERE s.granted_quota = 0 AND s.reclaimed_quota = 0;

-- 2.2 删空行。remark 非空的一律留下(见 C 问)。
DELETE FROM qy_ext.qy_commission_settlement
WHERE granted_quota = 0 AND reclaimed_quota = 0 AND remark = '';

-- COMMIT;

-- =====================================================================
-- 三、执行后复核:重跑 D,输出必须与删除前逐字相同。
-- =====================================================================
SELECT b.user_id,
       IFNULL(a.ss, 0) - IFNULL(s.net, 0) - b.unsettled_amount AS i1_drift,
       b.available_quota
         - (b.total_earned_quota - b.total_clawback_quota - b.frozen_quota - b.withdrawn_quota) AS i2_drift,
       IF(b.available_fiat < 0 OR (b.available_quota = 0 AND b.available_fiat <> 0), 'BAD', 'OK') AS i3
FROM qy_ext.qy_commission_balance b
LEFT JOIN (SELECT user_id, SUM(granted_quota) - SUM(reclaimed_quota) net
             FROM qy_ext.qy_commission_settlement GROUP BY user_id) s ON s.user_id = b.user_id
LEFT JOIN (SELECT inviter_id, SUM(settled_amount) ss
             FROM qy_ext.qy_commission_accrual WHERE status <> 'voided' GROUP BY inviter_id) a
       ON a.inviter_id = b.user_id
ORDER BY b.user_id;

-- 悬空引用必须是 0 行。
SELECT COUNT(*) AS must_be_zero_dangling
FROM qy_ext.qy_commission_accrual a
LEFT JOIN qy_ext.qy_commission_settlement s ON s.id = a.settlement_id
WHERE a.settlement_id <> 0 AND s.id IS NULL;
