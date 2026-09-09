# 部署基线:数据库

**现行口径(2026-09-06 拍板)**:全 MySQL 8.0,两个库。

| 库 | 配置项 | 装什么 |
|---|---|---|
| `newapi` | `.env` 的 `SQL_DSN` | 上游全部表 + `logs` + **`qy_fund_outbox`** |
| `qianye` | `data/qianye.yaml` 的 `database.dsn` | 77 张 `qy_*`(资金单、佣金账本、星屑、抽奖、商城、工单、违规、分组矩阵…) |

代码支持四路独立连接,这套基线**刻意只用两路**,另外两路(`LOG_SQL_DSN`、
`log_database.dsn`)留空回落。什么时候该拆开见 §5。

---

## 1. 为什么全 MySQL 而不是 PostgreSQL

上游对 SQLite / MySQL / PostgreSQL 三家平等支持(AGENTS.md 的硬性规则),
扩展库只认 MySQL 与 PostgreSQL(SQLite 没有行锁,承载不了资金路径 ——
见 `qianye/config/validate.go` 的 `validateDatabase`)。两家都能跑,选 MySQL 的
理由全部落在**出事更贵的那一半**上。

**扩展库的 77 张表是 MySQL 原生的,PG 分支是后补翻译。**
`qianye/db/dialect.go:20` 写着"扩展库长期只支持 MySQL"。手写 SQL 先按 MySQL 写
(`UNIX_TIMESTAMP()`、`ON DUPLICATE KEY UPDATE`、`DELETE ... LIMIT`、迁移互斥用
`GET_LOCK`),PG 侧是逐条对译出来的 —— 每一条都是一个可能漂移的点。

**PG 需要一整个自定义迁移器才不糟蹋资金表。**
`qianye/db/pg_migrator.go` 138 行,头部有实测数据:扩展库的表在 PG 上建好之后,
第二次 `AutoMigrate` 会再发 **507 条 ALTER,横跨 54 张表**;同一份模型在 MySQL 上
是 **0 条**。而 PG 的 `ALTER COLUMN TYPE` 会**重写整张表**并取 ACCESS EXCLUSIVE 锁,
不是 MySQL 8 的 INSTANT。这个补丁哪天失效,代价是每次重启都在佣金账本上做全表
重写加排他锁。

**PG 已经真炸过一次,就在 rc.33。**
`d01c5a45c` — 上游把 gorm 从 v1.25.2 升到 v1.25.12,契约反转
(`field.Unique` 不再由 `uniqueIndex` 置位),把 fork 自有的
`pkg/gormdialect/postgres.go` 打成了制造错误的代码:**第一次建表能过,第二次启动
FATAL**(`constraint "uni_tokens_key" ... does not exist`,SQLSTATE 42704)。
同一次升级,`pkg/gormdialect/mysql.go`(47 行)毫发无伤。详见
[decisions.md](decisions.md) 与 `pkg/gormdialect/unique_contract_test.go`。

### 代价(必须写下来,否则后人会以为 PG 是配错了)

- **上游自己的参考部署是 PostgreSQL。** `docker-compose.yml:29` 默认值是
  `SQL_DSN=postgresql://...`,服务用 `postgres:15`,MySQL 那行是注释掉的。
  上游用户群的主流因此也是 PG。
- **上游 CI 不起任何数据库 service。** `.github/workflows/ci.yml`(fork 一个字
  没改)只跑 `go vet` + `go build` + `make test`。上游的 PG 正确性靠用户实跑
  反馈,不靠 CI。
- **推论:上游新引入的、只在 MySQL 上有问题的 SQL,别人替我们发现得更慢。**

这是一次明知代价的选择,不是"PG 不能用"。翻这个结论之前先读完 §1 全文。

---

## 2. 两个库的边界

**扩展从不修改上游表结构。** `model/user.go` 被 fork 改了 115 行,但没有新增任何
一个 gorm 字段 —— 改的全是行为 hook。上游以后怎么折腾 `users` / `tokens` /
`channels` 的 schema,与扩展数据零交集。

**model 层的耦合面只有一个纯新增文件。** `model/qy_export.go` 头部:

> 这是一个纯新增文件:它的存在不需要修改任何上游既有文件,因此合并上游时冲突为 0。

---

## 3. `qy_fund_outbox` —— 唯一分不掉的表

78 张 `qy_` 表里 77 张在扩展库,**只有 `qy_fund_outbox` 在主库**。这不是遗漏。

它必须与资金变更**写在同一个主库事务里**。两个 MySQL 实例之间没有分布式事务
(除非上 XA),而补偿任务需要一个"主库副作用到底生效没有"的权威探针。
`model/qy_export.go` 的原话:

> 为什么不能用 logs 代替:`RecordLog` 写的是 `LOG_DB`,无法加入主库事务。把单号
> 写进日志只解决"运营能看见",不解决"进程在 commit 与写日志之间崩溃" ——
> 那种情况下钱已经动了,但没有任何记录能证明这一点。

`order_no` 上的唯一索引顺带让主库侧操作幂等:补偿任务重跑事务时,插入冲突即
代表"此前已应用过",可以安全跳过资金变更(`QyClaimFundOutbox` 返回 `false`)。

### 由此得出唯一的运维硬规则:备份必须成对

**两个库各自维护实例,但备份与恢复必须按同一个时间点成对进行。**

分别 `mysqldump`、分别恢复,会让两个库落在不同时间点,而
`QyProbeFundOutbox` 的答案就此失真。两个方向都会出事,形态不同:

| 漂移方向 | 后果 |
|---|---|
| 主库新、扩展库旧 | 主库有 outbox 行(钱已动),扩展库里那张单据被回滚掉了 —— 补偿任务根本不知道它存在。**钱动了,账本无记录。** |
| 扩展库新、主库旧 | 扩展库有已完成的单据,主库的资金变更被回滚掉了。单据已 `done`,补偿任务不会再碰它。**账本记着已发放,实际没发。** |

**做法:** 两个库都开 binlog,恢复时按同一个 GTID / 时间戳做 PITR。
两个库最好放同一个 MySQL 实例的两个 schema —— 那样 PITR 天然同点,
表前缀 `qy_` 保证不会撞名(`qianye.example.yaml` 里也说了这条)。
放两个实例也可以,但恢复流程必须写成一步。

---

## 4. 配置

`.env`:

```
SQL_DSN=newapi:PASS@tcp(mysql:3306)/newapi?charset=utf8mb4&parseTime=true&loc=Local
```

`data/qianye.yaml`:

```yaml
database:
  dsn: "qy_user:PASS@tcp(mysql:3306)/qianye?charset=utf8mb4&parseTime=true&loc=Local"
  max_idle_conns: 20
  max_open_conns: 100
```

`LOG_SQL_DSN` 与 `log_database.dsn` **两个都不填** —— 各自回落进上面两个库。

硬性要求:

- **MySQL 8.0**,不是 5.7。选 8.0 是为了 INSTANT DDL(启动迁移不锁表)。
- **两个库都必须是 `utf8mb4`。** 启动时 `checkMySQLChineseSupport` 会检查,
  不是 utf8mb4 直接 panic。
- `data/qianye.yaml` 权限设 `0600`。文件里有数据库密码,启动时检测到其他
  用户可读会告警。`/data/` 已在 `.gitignore` 里。

---

## 5. 什么时候拆到三个 / 四个库

两路可选连接留空是**默认**,不是将来必须改。触发条件各自独立:

**`LOG_SQL_DSN`(上游 `logs` 表)** —— 全系统行数最多的表,而且只有它一张
(`model/main.go` 的 `migrateLOGDB` 只 AutoMigrate 一个 `Log{}`)。
主库备份体积被它拖垮时拆出去。日量很大的话直接上 ClickHouse,代码本来就支持。
`logs` 不参与任何资金判定,拆走**不影响** §3 的成对 PITR 规则。

**`log_database.dsn`(扩展 `qy_violation_ai_review`)** —— 行数正比于**被抽中的
请求数**而不是成交笔数,两者可以差三到四个数量级;还要每小时按保留期批量删一遍。
大批量删除在 InnoDB 上留碎片,而它压在佣金账本、两阶段资金单旁边毫无必要 ——
那些表靠 `SELECT ... FOR UPDATE` 串行化,最不该跟清理任务抢 IO。
理由全文见 `qianye/db/logdb.go` 的文件头。这张表**不需要备份**。

两者都是纯配置改动,不动一行代码。注意 `log_database.dsn` 填得与
`database.dsn` **完全相同会直接启动失败** —— 那等于没分家却多开一套连接池、
多跑一次迁移、多一把迁移锁(`qianye/config/validate.go` 的 `validateLogDatabase`)。

**`risk_watch.database.dsn`(风控预警的两张表)—— 这一路不是"可选拆分",
是开这个功能的前置条件。** 与上面两路的方向相反:留空不是"跟着主库走",
而是整个功能不注册(`risk_watch.enabled: true` 却漏填 dsn 会**直接拒绝启动**)。

理由是它的体量不由部署决定,而由**某个管理员下午三点点的那一下**决定:监听记录存的
是被抽中请求的完整上下文,一个「永久监听 + 100% 概率」的任务一天就能写进几十 GB。
并进任何一个已有的库,迟早会由一次监管操作把佣金账本所在的那块盘写满。
理由全文见 `qianye/db/watchdb.go` 的文件头与 [design-16](design-16-riskwatch.md) §2。

它**不需要成对 PITR**,与台账库同一档:资金要能按时间点恢复,一份滚动 30 天的
取证台账不要。建议直接给它一台单独的机器 —— 磁盘写满时躺下的只有它自己。

也就是说:开了风控预警的站点,扩展这一侧最多有**三个**库,而 §3 的成对 PITR 规则
仍然只约束「主扩展库 + 主库」这一对。

---

## 6. 验收:必须启动两次

```
docker compose down && docker compose up -d && docker compose logs -f new-api
docker compose restart new-api && docker compose logs -f new-api
```

**第二遍才是判据。** §1 里那个 PG 事故第一遍是全绿的。第二遍应该看到:

- 没有成片的 `ALTER TABLE`(迁移是空操作)
- `qianye: 扩展数据库已连接`
- 开了风控预警时还要有 `qianye: 风控预警存储节点已连接`。**没有这一行就是没生效**,
  而管理端页面会 404 —— 那时先回去看 `risk_watch.enabled` 与它的 dsn
- `qianye: 扩展初始化完成`
- 没有 `[SYS]` 的缺段告警(那说明 YAML 里少了某个模块段)

---

## 7. 每次同步上游,查这三条

跟跑哪个引擎无关,是 fork 自有代码与上游的接触面:

1. **`go.mod` 里 gorm 或其 driver 版本变了** → 复核 `pkg/gormdialect/` 三个
   装饰器。这是 fork 自有目录(上游 `pkg/` 下没有它),上游不会替我们测。
   现成守卫:`pkg/gormdialect/unique_contract_test.go`,不连库,直接问 gorm
   契约变没变。
2. **上游改了 model 的 `uniqueIndex` 或 `default:` 标签**
3. **上游新增了进 `AutoMigrate` 的表**

命中任意一条,连真库跑 §6 的双启动。三条都没命中,普通 merge 即可。

