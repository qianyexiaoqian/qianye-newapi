# 需求7:违规检测

# 需求 7 · 违规检测 — 实施设计

## 0. 结论摘要

### 0.1 GAPS §一.3「硬矛盾」的正面解决:矛盾前提是错的

GAPS 认为「转发前拦截」必须挂在 `Distribute()` 之前,所以拿不到价格。**这个前提不成立**。真正的「转发上游」边界不是 `Distribute()`,而是 `controller/relay.go:194` 的重试循环(第一次 `adaptor.DoRequest` 在 `relay/compatible_handler.go:189`)。

在 `controller/relay.go` 的 **123 ~ 182 行之间**存在一个窗口,同时满足全部条件:

| 行 | 已就绪的东西 |
|---|---|
| `:123` `GenRelayInfo` | `relayInfo` 完整(UserId / TokenId / UsingGroup / OriginModelName / RelayFormat) |
| `:134` `request.GetTokenCountMeta()` | `meta.CombineText`(全协议归一化 prompt)+ `meta.Files`(多模态) |
| `:156` `helper.ModelPriceHelper` | **`relay/helper/price.go:182` 执行 `info.PriceData = priceData`** —— 此后 `relayInfo.PriceData.ModelPrice` / `ModelRatio` / `GroupRatioInfo.GroupRatio` 全部可用 |
| `:194` | 才开始选渠道并发出上游请求 |

**所以「转发前拦截」和「模型价格 × 倍数扣费」可以在同一个挂载点实现:`controller/relay.go:160` 之后。** 双挂载点方案依然给出,但第二个挂载点(中间件)的定位是**可选的廉价前置闸门**,不是必需品。

### 0.2 双挂载点

| 挂载点 | 位置 | 能力 | 默认 |
|---|---|---|---|
| **M1 主挂载点** | `controller/relay.go:160` 后(+3 行) | 能力 B:转发前拦截 + 拦截时扣费(固定/倍数皆可) | 开 |
| **M2 主挂载点** | `controller/relay.go:180` 后(+1 行) | 能力 A:事后检测扣费(上游错误码/错误文本/reject_reason) | 开 |
| **M3 可选加速层** | `router/relay-router.go:73` 后(+1 行) | 已封禁用户秒拒、全局硬黑名单、响应体 tap(成功响应内容检测) | **关**(config gate) |
| **M4 可选** | `router/relay-router.go:198` 后(+1 行) | 同 M3,覆盖 Gemini 原生 `/v1beta` | 关 |

### 0.3 关键发现:本模块**不需要** `model/qy_export.go`

GAPS §三.2(3) 说「没有可复用的 `model.DisableUser`,必须自己拼四步」——正确。但四步所需的**全部函数都已导出**:

- `model.IncrementUserAuthVersionWithTx(tx, uid) (int64, error)` — `model/user_auth_cache.go:180` ✅ 导出
- `model.PublishUserAuthCache(uid) error` — `model/user_auth_cache.go:229` ✅ 导出
- `model.InvalidateUserTokensCache(uid) error` — `model/token.go:494` ✅ 导出
- `model.RevokeAllUserSessions(uid, reason) (int64, error)` — `model/user_session.go:708` ✅ 导出
- `model.InvalidateUserCache(uid) error` — `model/user_cache.go:72` ✅ 导出

**本模块对 `model/qy_export.go` 的需求为零**,不占用该文件的预算。

### 0.4 改动预算占用

| 文件 | 行数 | 冲突风险 |
|---|---|---|
| `controller/relay.go` | 5(1 import + 3 + 1) | **高** |
| `router/relay-router.go` | 3(1 import + 1 + 1) | 中 |
| **合计** | **8 行 / 2 文件** | |

M3/M4 若不启用可省 3 行(2 文件 → 1 文件 / 5 行)。

---

## 1. 完整表结构

全部位于独立 MySQL(`qianye/model/` 包),表名统一 `qy_violation_*`。GORM 连接使用 `qianye/db.QyDB`。

### 1.1 规则表 `qy_violation_rule`

```go
// qianye/model/violation_rule.go
package qymodel

type ViolationRule struct {
	Id        int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name      string `gorm:"type:varchar(128);not null" json:"name"`                    // 管理端展示 & 写入计费日志的原因文案来源
	Remark    string `gorm:"type:varchar(512);not null;default:''" json:"remark"`       // 内部备注,不对用户展示
	Enabled   bool   `gorm:"not null;default:true;index:idx_qyvr_enabled_phase,priority:1" json:"enabled"`
	Priority  int    `gorm:"not null;default:100;index:idx_qyvr_priority" json:"priority"` // 升序,小者先判;同优先级按 id 升序

	// —— 生效阶段 ——
	Phase string `gorm:"type:varchar(24);not null;index:idx_qyvr_enabled_phase,priority:2" json:"phase"`
	// "prompt"        请求转发前(能力 B,M1)
	// "upstream_err"  上游返回错误后(能力 A,M2)
	// "response"      成功响应内容(需 M3 响应 tap)
	// "reject_reason" 上游软违规信号 ContextKeyAdminRejectReason(M2/M3)

	// —— 匹配方式 ——
	MatchType string `gorm:"type:varchar(24);not null" json:"match_type"`
	// "keyword"      Pattern 为换行分隔词表 → 走 service.AcSearch
	// "regex"        Pattern 为 Go RE2 正则(无回溯灾难风险)
	// "error_code"   Pattern 为逗号分隔的 types.ErrorCode 精确值
	// "status_code"  Pattern 为逗号分隔 HTTP 状态码 / 区间 "400-499"
	// "upstream_text" Pattern 为换行分隔子串,匹配 apiErr.Error() + ToOpenAIError().Message
	Pattern       string `gorm:"type:text;not null" json:"pattern"`
	CaseSensitive bool   `gorm:"not null;default:false" json:"case_sensitive"`
	// 编译期校验:regex 必须 regexp.Compile 通过,且 len(Pattern) <= 4096

	// —— 作用域(需求原文的「全部分组或特定分组」)——
	ModelScope string `gorm:"type:varchar(2048);not null;default:''" json:"model_scope"`
	// "" = 全部模型;否则逗号分隔,支持前后缀通配 "gpt-4*" / "*-vision"
	GroupScope string `gorm:"type:varchar(1024);not null;default:''" json:"group_scope"`
	// "" = 全部分组;否则逗号分隔分组名,匹配 relayInfo.UsingGroup
	ChannelScope string `gorm:"type:varchar(512);not null;default:''" json:"channel_scope"`
	// "" = 全部渠道;逗号分隔 channel_id。仅 upstream_err/response 阶段有效(M1 阶段渠道未选)
	UserScope string `gorm:"type:varchar(512);not null;default:''" json:"user_scope"`
	// "" = 全部用户;支持 "!group:vip"(排除)/"user:123"。用于灰度与白名单

	// —— 处置动作 ——
	Action string `gorm:"type:varchar(24);not null;default:'record'" json:"action"`
	// "record"          仅记录,不扣费不阻断
	// "charge"          扣费(不阻断;prompt 阶段无意义,校验时禁止)
	// "block"           阻断(仅 prompt 阶段有效)
	// "block_and_charge" 阻断 + 扣费

	FeeMode string `gorm:"type:varchar(24);not null;default:'none'" json:"fee_mode"`
	// "none" | "fixed" | "model_price_multiple" | "prompt_quota_multiple"
	FeeAmount   decimal.Decimal `gorm:"type:decimal(18,8);not null;default:0" json:"fee_amount"`
	// fixed 模式:美元金额(与 setting/model_setting/grok.go 的 ViolationDeductionAmount 同语义)
	FeeMultiple decimal.Decimal `gorm:"type:decimal(18,6);not null;default:0" json:"fee_multiple"`
	// model_price_multiple:  单价 × 倍数
	// prompt_quota_multiple: 本次请求预估额度 × 倍数
	FeeMaxAmount decimal.Decimal `gorm:"type:decimal(18,8);not null;default:0" json:"fee_max_amount"`
	// 单笔扣费上限(美元),0 = 不限。防止倍数配置失误一次扣光余额

	// —— 计数与封禁 ——
	CountWeight int `gorm:"not null;default:1" json:"count_weight"` // 命中一次给账号总量线与所绑类型线各加几,0 = 一条线都不推进
	// Severity(1=低 2=中 3=高)在实现中**已移除**:它从头到尾只写不读,
	// 违规类型体系落地后"这一类有多严重"由类型自己的阈值/窗口表达。
	// 数据库列 `severity` 也已删除(启动期一次性迁移 dropLegacySeverityColumn,
	// 幂等、对没有该列的库是 no-op)。删列不影响内置规则指纹:指纹是
	// sha256(pattern),从来不含 severity,所以不会产生"内置规则已被修改"的假告警。

	// —— 上下文归档 ——
	ArchiveContext bool `gorm:"not null;default:true" json:"archive_context"`

	// —— 拒绝文案 ——
	BlockMessage string `gorm:"type:varchar(512);not null;default:''" json:"block_message"`
	// 返回给客户端的 message,空则用全局默认("请求包含违规内容,已被拒绝")
	// 严禁把命中词写进这里(等于告诉刷子绕过方法)

	CreatedAt int64 `gorm:"not null;index:idx_qyvr_created" json:"created_at"`
	UpdatedAt int64 `gorm:"not null" json:"updated_at"`
	CreatedBy int   `gorm:"not null;default:0" json:"created_by"` // 主库 user_id
	UpdatedBy int   `gorm:"not null;default:0" json:"updated_by"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`               // 软删,保证历史记录的 rule_id 可追溯
}

func (ViolationRule) TableName() string { return "qy_violation_rule" }
```

**字段存在理由**
- `Phase` + `MatchType` 拆开:同一条词表可能既要拦 prompt 又要匹配上游返回,两者的执行时机与开销完全不同,合并会导致热路径跑无用规则。
- `ModelScope`/`GroupScope` 直接对应需求原文「设置某个模型(全部分组或特定分组下)」。
- `ChannelScope` 是补充:上游违规标记(如 Grok CSAM)本质是渠道特性,按渠道收敛能减少误判。
- `FeeMaxAmount`:`model_price_multiple` 遇到高价模型(如 $75/M token)+ 大倍数会一次扣穿余额,必须有闸。
- `CountWeight=0`:允许「只扣费不累计封号」或「只累计不扣费」的独立配置。
- `DeletedAt` 软删:历史违规记录外键指向 `rule_id`,硬删会让管理端与申诉流程失去规则上下文。

### 1.2 规则版本表 `qy_violation_rule_version`(热更新用)

```go
type ViolationRuleVersion struct {
	Id        int   `gorm:"primaryKey"`            // 恒为 1,单行表
	Version   int64 `gorm:"not null;default:0"`    // 每次规则写操作 +1
	UpdatedAt int64 `gorm:"not null"`
}
func (ViolationRuleVersion) TableName() string { return "qy_violation_rule_version" }
```

存在理由:多节点部署下,节点 A 改规则,节点 B 必须感知。轮询这张单行表(1 次主键查询,30s 一次,零成本)比轮询全表便宜三个数量级。

### 1.3 违规记录主表 `qy_violation_record`

```go
type ViolationRecord struct {
	Id     int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	RecNo  string `gorm:"type:varchar(48);not null;uniqueIndex:uk_qyvrec_no" json:"rec_no"`
	// 幂等键。格式 "vr_" + request_id + "_" + rule_id。
	// 同一 request_id 同一规则只会有一条记录 —— 重试循环 + defer 可能重入

	UserId    int    `gorm:"not null;index:idx_qyvrec_user_created,priority:1" json:"user_id"`
	Username  string `gorm:"type:varchar(64);not null;default:''" json:"username"` // 冗余,避免管理端跨库 join 主库
	TokenId   int    `gorm:"not null;default:0;index" json:"token_id"`
	TokenName string `gorm:"type:varchar(64);not null;default:''" json:"token_name"`

	RuleId   int64  `gorm:"not null;index:idx_qyvrec_rule" json:"rule_id"`
	RuleName string `gorm:"type:varchar(128);not null;default:''" json:"rule_name"` // 规则改名/删除后仍可读
	Phase    string `gorm:"type:varchar(24);not null" json:"phase"`
	Action   string `gorm:"type:varchar(24);not null" json:"action"`

	ModelName   string `gorm:"type:varchar(128);not null;default:'';index:idx_qyvrec_model" json:"model_name"`
	UsingGroup  string `gorm:"column:using_group;type:varchar(64);not null;default:''" json:"using_group"`
	ChannelId   int    `gorm:"not null;default:0" json:"channel_id"`
	RelayFormat string `gorm:"type:varchar(32);not null;default:''" json:"relay_format"`

	RequestId string `gorm:"type:varchar(64);not null;default:'';index:idx_qyvrec_reqid" json:"request_id"`
	// ★ 与主库 logs.request_id 对齐,是「新库记录 ↔ 主库计费日志」唯一的对账钥匙
	Ip        string `gorm:"type:varchar(64);not null;default:''" json:"ip"`

	// —— 命中证据(小字段,主表内联)——
	MatchedTerms string `gorm:"type:varchar(1024);not null;default:''" json:"matched_terms"`
	// 命中的关键词/正则捕获,最多 16 项,单项截断到 64 字符
	MatchSnippet string `gorm:"type:varchar(2048);not null;default:''" json:"match_snippet"`
	// 命中点前后各 ±160 字符窗口,已脱敏。管理端列表页直接看这个,不必拉 payload

	// —— 扣费 ——
	FeeMode      string          `gorm:"type:varchar(24);not null;default:'none'" json:"fee_mode"`
	FeeBaseUsd   decimal.Decimal `gorm:"type:decimal(18,8);not null;default:0" json:"fee_base_usd"`   // 计费基数(单价或固定额)
	FeeMultiple  decimal.Decimal `gorm:"type:decimal(18,6);not null;default:0" json:"fee_multiple"`
	GroupRatio   decimal.Decimal `gorm:"type:decimal(18,6);not null;default:0" json:"group_ratio"`    // 冻结当时分组倍率
	FeeQuotaWant int64           `gorm:"not null;default:0" json:"fee_quota_want"`                    // 计算出的应扣 quota
	FeeQuota     int64           `gorm:"not null;default:0" json:"fee_quota"`                         // 实际扣掉的 quota(可能被截断)
	FeeStatus    string          `gorm:"type:varchar(16);not null;default:'none'" json:"fee_status"`
	// "none" | "charged" | "truncated" | "skipped_insufficient" | "failed" | "refunded"
	FeeError     string          `gorm:"type:varchar(512);not null;default:''" json:"fee_error"`
	QuotaClamp   string          `gorm:"type:varchar(512);not null;default:''" json:"quota_clamp"`    // common.QuotaClamp.AuditMap() JSON

	// —— 计数 ——
	CountWeight  int  `gorm:"not null;default:1" json:"count_weight"`
	Counted      bool `gorm:"not null;default:false" json:"counted"`       // 是否已计入 counter(补偿任务依据)
	CounterAfter int  `gorm:"not null;default:0" json:"counter_after"`     // 计数后的值,用于审计封号触发点

	// —— 状态机 ——
	Status string `gorm:"type:varchar(16);not null;default:'active';index:idx_qyvrec_status" json:"status"`
	// "active" 生效 | "revoked" 管理员撤销 | "appealed" 申诉中
	RevokedBy     int    `gorm:"not null;default:0" json:"revoked_by"`
	RevokedAt     int64  `gorm:"not null;default:0" json:"revoked_at"`
	RevokeReason  string `gorm:"type:varchar(512);not null;default:''" json:"revoke_reason"`
	RefundedQuota int64  `gorm:"not null;default:0" json:"refunded_quota"`
	RefundNo      string `gorm:"type:varchar(48);not null;default:''" json:"refund_no"` // 退款幂等键

	HasPayload bool  `gorm:"not null;default:false" json:"has_payload"`
	CreatedAt  int64 `gorm:"not null;index:idx_qyvrec_user_created,priority:2;index:idx_qyvrec_created" json:"created_at"`
}

func (ViolationRecord) TableName() string { return "qy_violation_record" }
```

**索引说明**
- `uk_qyvrec_no` 唯一:幂等的唯一保障。重试循环里 `NormalizeViolationFeeError` 会在 `:176` 和 `:232` 两处调用,`ChargeViolationFeeIfNeeded` 虽然只在 defer 调一次,但 panic-recover 路径与未来上游改动都可能重入。
- `idx_qyvrec_user_created(user_id, created_at)`:计数窗口查询与用户端列表的主查询路径。
- `idx_qyvrec_reqid`:与主库 `logs.request_id` 对账。

**金额精度**
- `decimal(18,8)` 承载美元金额(项目 `ViolationDeductionAmount` 默认 0.05,倍率类配置常见 6~8 位小数)。
- `fee_quota` 用 `bigint` 而非 `int`:虽然 `common.MaxQuota = math.MaxInt32`,但归档字段用 bigint 可以在 clamp 发生时把**原始未截断值**如实记下来,便于事后核查。
- 所有 `float64 → quota` 转换**必须**走 `common.QuotaFromDecimalChecked`,clamp 写进 `QuotaClamp` 字段(AGENTS.md 强制)。禁止照抄 `service/violation_fee.go:91-99` 的 `decimal...Round(0).IntPart()` —— 那段本身违反 AGENTS.md。

### 1.4 上下文归档表 `qy_violation_payload`

```go
type ViolationPayload struct {
	RecordId int64 `gorm:"primaryKey" json:"record_id"` // 1:0..1,与 record 共主键

	Codec      string `gorm:"type:varchar(16);not null;default:'zstd'" json:"codec"` // "zstd" | "gzip" | "raw"
	RawBytes   int64  `gorm:"not null;default:0" json:"raw_bytes"`                   // 压缩前(截断后)字节数
	StoredBytes int64 `gorm:"not null;default:0" json:"stored_bytes"`                // 压缩后字节数
	OriginBytes int64 `gorm:"not null;default:0" json:"origin_bytes"`                // 原始 body 字节数(截断前)
	Truncated  bool   `gorm:"not null;default:false" json:"truncated"`

	StorageKind string `gorm:"type:varchar(16);not null;default:'db'" json:"storage_kind"` // "db" | "fs" | "s3"
	StorageRef  string `gorm:"type:varchar(512);not null;default:''" json:"storage_ref"`   // fs/s3 时的路径或对象键
	Body        []byte `gorm:"type:mediumblob" json:"-"`                                   // storage_kind=db 时的压缩 payload

	Redacted     bool   `gorm:"not null;default:false" json:"redacted"`
	RedactStats  string `gorm:"type:varchar(512);not null;default:''" json:"redact_stats"` // {"email":2,"phone":1,...}
	FilesSummary string `gorm:"type:text" json:"files_summary"`                            // 多模态描述符 JSON 数组(不含二进制)

	CreatedAt int64 `gorm:"not null;index:idx_qyvpay_created" json:"created_at"` // 保留期清理走这个索引
}

func (ViolationPayload) TableName() string { return "qy_violation_payload" }
```

**为什么拆表**:见 §7 的体积估算。主表行 ~1KB,payload 行 ~KB~MB。放一起会让「按用户查最近 20 条违规」这种列表查询被大 BLOB 拖垮(InnoDB 溢出页 IO),也会让保留期清理必须动主表。

### 1.5 违规计数器 `qy_violation_counter`

```go
type ViolationCounter struct {
	UserId      int    `gorm:"primaryKey" json:"user_id"`
	WindowStart int64  `gorm:"not null;default:0" json:"window_start"` // 当前滚动窗口起点(秒)
	HitCount    int    `gorm:"not null;default:0" json:"hit_count"`    // 当前窗口内加权计数
	TotalCount  int64  `gorm:"not null;default:0" json:"total_count"`  // 历史累计(只增,审计用)
	BanCycle    int    `gorm:"not null;default:0" json:"ban_cycle"`    // 已封禁次数;解封时 +1 开启新周期
	LastHitAt   int64  `gorm:"not null;default:0" json:"last_hit_at"`
	UpdatedAt   int64  `gorm:"not null" json:"updated_at"`
}
func (ViolationCounter) TableName() string { return "qy_violation_counter" }
```

### 1.6 封禁认领表 `qy_violation_ban`

```go
type ViolationBan struct {
	Id       int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UserId   int   `gorm:"not null;uniqueIndex:uk_qyvban_user_cycle,priority:1" json:"user_id"`
	BanCycle int   `gorm:"not null;uniqueIndex:uk_qyvban_user_cycle,priority:2" json:"ban_cycle"`
	// ★ (user_id, ban_cycle) 唯一 = 分布式互斥的"封号认领锁",一个周期只可能有一个节点插入成功

	TriggerRecordId int64  `gorm:"not null;default:0" json:"trigger_record_id"`
	HitCountAt      int    `gorm:"not null;default:0" json:"hit_count_at"`   // 触发时的计数值
	Threshold       int    `gorm:"not null;default:0" json:"threshold"`      // 冻结当时阈值配置

	Status string `gorm:"type:varchar(16);not null;default:'pending';index:idx_qyvban_status" json:"status"`
	// "pending"  已认领,主库操作尚未完成 → 补偿任务扫描
	// "banned"   主库四步已完成
	// "skipped"  目标是 root/已禁用/白名单 → 无需操作
	// "failed"   主库操作失败,已重试耗尽 → 告警
	// "unbanned" 已解封

	Attempts   int    `gorm:"not null;default:0" json:"attempts"`
	LastError  string `gorm:"type:varchar(512);not null;default:''" json:"last_error"`
	BannedAt   int64  `gorm:"not null;default:0" json:"banned_at"`
	UnbannedAt int64  `gorm:"not null;default:0" json:"unbanned_at"`
	UnbannedBy int    `gorm:"not null;default:0" json:"unbanned_by"`
	UnbanNote  string `gorm:"type:varchar(512);not null;default:''" json:"unban_note"`
	CreatedAt  int64  `gorm:"not null;index:idx_qyvban_created" json:"created_at"`
}
func (ViolationBan) TableName() string { return "qy_violation_ban" }
```

### 1.7 申诉表 `qy_violation_appeal`(建议补充)

```go
type ViolationAppeal struct {
	Id       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	AppealNo string `gorm:"type:varchar(48);not null;uniqueIndex:uk_qyvap_no" json:"appeal_no"`

	UserId   int   `gorm:"not null;index:idx_qyvap_user" json:"user_id"`
	RecordId int64 `gorm:"not null;uniqueIndex:uk_qyvap_record" json:"record_id"`
	// ★ 一条违规记录只允许一次未结申诉。若允许多次,应改为 (record_id, status) 部分唯一;
	//   MySQL 无部分唯一索引,故用应用层 + 唯一键"记录级唯一"的简单策略,驳回后管理员可手动放开

	Reason      string `gorm:"type:varchar(2000);not null;default:''" json:"reason"`
	Attachments string `gorm:"type:text" json:"attachments"` // JSON 数组,仅存 URL/引用,不存二进制

	Status string `gorm:"type:varchar(16);not null;default:'pending';index:idx_qyvap_status" json:"status"`
	// "pending" | "approved" | "rejected" | "withdrawn"
	ReviewerId   int    `gorm:"not null;default:0" json:"reviewer_id"`
	ReviewNote   string `gorm:"type:varchar(1000);not null;default:''" json:"review_note"`
	ReviewedAt   int64  `gorm:"not null;default:0" json:"reviewed_at"`
	RefundQuota  int64  `gorm:"not null;default:0" json:"refund_quota"`
	Unbanned     bool   `gorm:"not null;default:false" json:"unbanned"`

	CreatedAt int64 `gorm:"not null;index:idx_qyvap_created" json:"created_at"`
	UpdatedAt int64 `gorm:"not null" json:"updated_at"`
}
func (ViolationAppeal) TableName() string { return "qy_violation_appeal" }
```

### 1.8 后台任务租约锁 `qy_task_lease`(全项目共享,本模块声明依赖)

```go
type TaskLease struct {
	Name      string `gorm:"type:varchar(64);primaryKey" json:"name"`
	Owner     string `gorm:"type:varchar(128);not null" json:"owner"`   // hostname+pid+启动随机数
	ExpiresAt int64  `gorm:"not null;index" json:"expires_at"`
	UpdatedAt int64  `gorm:"not null" json:"updated_at"`
}
func (TaskLease) TableName() string { return "qy_task_lease" }
```

本模块占用的租约名:`violation:counter_flush`、`violation:ban_compensate`、`violation:retention_gc`、`violation:rule_refresh`(rule_refresh 不需要租约,每个节点各自刷新)。

**说明**:此表若已由架构地基统一提供,本模块直接复用,不重复建表。

---

## 2. YAML 配置(`qianye.yaml` 的 `violation` 节)

```yaml
violation:
  enabled: true                       # 总开关。false = 所有挂载点立即 return,零开销

  # —— 挂载点开关 ——
  prompt_guard_enabled: true          # M1 转发前拦截
  post_guard_enabled: true            # M2 事后检测扣费
  middleware_enabled: false           # M3/M4 中间件层(廉价前置闸 + 响应 tap)
  response_scan_enabled: false        # M3 响应体内容检测(依赖 middleware_enabled)

  # —— 规则引擎 ——
  rule_refresh_interval: 30s          # 规则快照刷新周期
  max_scan_bytes: 65536               # 单请求参与匹配的最大文本字节(头 32K + 尾 32K)
  max_regex_rules: 32                 # 单请求最多评估的正则规则数,超出按 priority 截断
  force_build_combine_text: true      # 当上游 meta.CombineText 为空时,自行构建(见 §6.1)

  # —— 扣费 ——
  insufficient_balance_policy: truncate  # truncate | allow_negative | debt | ban_only
  max_fee_usd_per_request: 5.0        # 单请求违规扣费硬上限(美元),兜住配置失误
  charge_on_playground: false         # Playground 请求是否扣费

  # —— 计数与封号 ——
  count_window: 720h                  # 滚动计数窗口(30 天)
  ban_threshold: 5                    # 加权计数达到该值触发封号;0 = 关闭自动封号
  ban_scope: user                     # user | user_model(按模型分别计数)
  ban_exempt_roles: [100]             # 豁免角色(root)
  ban_exempt_users: []                # 豁免用户 id
  ban_notify_email: true

  # —— 上下文归档 ——
  archive_enabled: true
  archive_raw_body: false             # true = 额外归档原始 body(默认关,见 §7)
  archive_max_context_bytes: 262144   # 压缩前上限 256KB
  archive_max_payload_bytes: 1048576  # 压缩后上限 1MB(MySQL max_allowed_packet 安全线)
  archive_storage: db                 # db | fs | s3
  archive_fs_dir: ./data/qy_violation_payload
  archive_keep_image_thumbnail: false # true = 图片转 256px JPEG 存,上限 32KB/张
  archive_max_files: 32               # 单条记录最多描述多少个多模态文件

  # —— 脱敏 ——
  redact_enabled: true
  redact_patterns: [email, phone_cn, id_card_cn, bank_card, api_key, bearer, url_query]
  redact_custom: []                   # 追加自定义正则

  # —— 保留期 ——
  payload_retention_days: 30
  record_retention_days: 365
  gc_batch_size: 500
  gc_interval: 1h

  # —— 异步与熔断 ——
  async_queue_size: 4096
  async_workers: 2
  db_timeout: 2s
  circuit_error_threshold: 10         # 窗口内连续失败次数
  circuit_open_duration: 30s
  fail_open: true                     # 硬性约束:热路径永远 true,此项仅供非热路径读取
```

`Enabled()` 语义:配置文件缺失 → `violation.enabled = false` → 所有挂载点 `return` (零分配)。配置存在但新库连不上 → 规则快照回落到**最后一次成功加载的内存副本**(冷启动时为空 = 不检测),记录写入进异步队列并在熔断打开时丢弃 + 计数告警。**热路径一律放行。**

---

## 3. 完整 API 清单

统一前缀 `/api/qy/violation`。注册在 `qianye/router.go` 的 `RegisterRoutes(*gin.Engine)`,组级中间件:

```go
g := server.Group("/api/qy/violation")
g.Use(middleware.CORS())                       // 引擎级 CORS 在 SetRelayRouter 里注册,晚于本组,不会继承
g.Use(middleware.GlobalAPIRateLimit())
admin := g.Group("", middleware.AdminAuth())
user  := g.Group("/self", middleware.UserAuth())
```

响应体统一 `{ "success": bool, "message": string, "data": any }`(与项目一致)。

### 3.1 规则管理(管理员)

| Method | Path | 请求 | 响应 data |
|---|---|---|---|
| GET | `/api/qy/violation/rules` | query: `p`, `page_size`, `keyword`, `phase`, `enabled` | `{ items: Rule[], total: int64 }` |
| GET | `/api/qy/violation/rules/:id` | — | `Rule` |
| POST | `/api/qy/violation/rules` | `RuleUpsertReq`(见下) | `{ id: int64 }` |
| PUT | `/api/qy/violation/rules/:id` | `RuleUpsertReq` | `{}` |
| DELETE | `/api/qy/violation/rules/:id` | — | `{}` |
| POST | `/api/qy/violation/rules/:id/toggle` | `{ enabled: bool }` | `{}` |
| POST | `/api/qy/violation/rules/test` | `{ rule: RuleUpsertReq, sample_text: string, model?: string, group?: string }` | `{ matched: bool, terms: string[], snippet: string, scope_ok: bool, elapsed_us: int }` |
| POST | `/api/qy/violation/rules/refresh` | — | `{ version: int64, rule_count: int }` 强制刷新本节点快照 |

```ts
// RuleUpsertReq
{
  name: string; remark?: string; enabled: boolean; priority: number;
  phase: 'prompt'|'upstream_err'|'response'|'reject_reason';
  match_type: 'keyword'|'regex'|'error_code'|'status_code'|'upstream_text';
  pattern: string; case_sensitive: boolean;
  model_scope: string; group_scope: string; channel_scope: string; user_scope: string;
  action: 'record'|'charge'|'block'|'block_and_charge';
  fee_mode: 'none'|'fixed'|'model_price_multiple'|'prompt_quota_multiple';
  fee_amount: string; fee_multiple: string; fee_max_amount: string;   // decimal 走字符串,避免 JS 精度丢失
  count_weight: number;   // severity 已移除,见 §1.1
  archive_context: boolean; block_message: string;
}
```

**服务端校验(必须)**
- `action` 含 `block` 时 `phase` 必须为 `prompt`(其他阶段字节已发出,阻断无意义);
- `match_type=regex` → `regexp.Compile` 通过 且 `len(pattern) <= 4096`;
- `fee_mode != none` 时 `action` 必须含 `charge`;
- `fee_multiple > 0` 且 `fee_max_amount == 0` → 返回警告(不阻止)。

### 3.2 违规记录(管理员)

| Method | Path | 请求 | 响应 data |
|---|---|---|---|
| GET | `/api/qy/violation/records` | `p`,`page_size`,`user_id`,`username`,`model`,`group`,`rule_id`,`phase`,`status`,`start_ts`,`end_ts`,`request_id` | `{ items: Record[], total: int64 }` |
| GET | `/api/qy/violation/records/:id` | — | `Record & { payload_meta: PayloadMeta }` |
| GET | `/api/qy/violation/records/:id/context` | — | `{ context: NormalizedContext, files: FileDescriptor[], truncated: bool, redacted: bool }` |
| GET | `/api/qy/violation/records/:id/context/raw` | — | 同上但未脱敏。**需 `middleware.RootAuth()` + `middleware.SecureVerificationRequired()`**,且写一条 `RecordOperationAuditLog` |
| POST | `/api/qy/violation/records/:id/revoke` | `{ reason: string, refund: bool }` | `{ refunded_quota: int64, counter_after: int }` |
| GET | `/api/qy/violation/records/export` | 同列表筛选 + `format=csv` | CSV 流(不含 payload,仅元数据 + snippet) |

### 3.3 封禁管理(管理员)

| Method | Path | 请求 | 响应 data |
|---|---|---|---|
| GET | `/api/qy/violation/bans` | `p`,`page_size`,`status`,`user_id` | `{ items: Ban[], total }` |
| GET | `/api/qy/violation/counters` | `p`,`page_size`,`min_count`,`order` | `{ items: Counter[], total }` 风险用户排行 |
| POST | `/api/qy/violation/bans/:id/unban` | `{ note: string, reset_counter: bool }` | `{}` |
| POST | `/api/qy/violation/bans/:id/retry` | — | `{ status: string }` 手动重试 pending/failed 的封号 |
| POST | `/api/qy/violation/counters/:user_id/reset` | `{ note: string }` | `{}` |

### 3.4 配置与统计(管理员)

| Method | Path | 请求 | 响应 data |
|---|---|---|---|
| GET | `/api/qy/violation/config` | — | 当前生效的 YAML `violation` 节(只读,含 `read_only: true` 标记与 `config_path`) |
| GET | `/api/qy/violation/health` | — | `{ db_ok, circuit_open, queue_len, queue_dropped, rule_count, rule_version, snapshot_age_ms }` |
| GET | `/api/qy/violation/stats` | `hours` | `{ by_rule: [], by_model: [], by_group: [], series: [], total_fee_quota, ban_count }` |

**配置为只读**:YAML 由运维改文件 + 重启/热加载,不提供 PUT。理由:配置文件是唯一真相源,提供写 API 会引入「文件 vs 数据库谁赢」的问题,且违背地基第 3 条(禁止入 `GlobalConfig`)。若确需在线改,提供 `POST /api/qy/violation/config/reload` 重新读文件即可。

### 3.5 申诉(管理员侧)

| Method | Path | 请求 | 响应 data |
|---|---|---|---|
| GET | `/api/qy/violation/appeals` | `p`,`page_size`,`status`,`user_id` | `{ items: Appeal[], total }` |
| GET | `/api/qy/violation/appeals/:id` | — | `Appeal & { record: Record }` |
| POST | `/api/qy/violation/appeals/:id/review` | `{ decision: 'approved'\|'rejected', note: string, refund: bool, unban: bool, reset_counter: bool }` | `{ refunded_quota, unbanned }` |

### 3.6 用户端

| Method | Path | 权限 | 响应 data |
|---|---|---|---|
| GET | `/api/qy/violation/self/records` | UserAuth | `{ items: UserRecordView[], total }` |
| GET | `/api/qy/violation/self/summary` | UserAuth | `{ active_count, window_days, ban_threshold, remaining, total_fee_quota }` |
| POST | `/api/qy/violation/self/appeals` | UserAuth + `middleware.CriticalRateLimit()` | `{ appeal_no }` |
| GET | `/api/qy/violation/self/appeals` | UserAuth | `{ items: Appeal[], total }` |
| POST | `/api/qy/violation/self/appeals/:id/withdraw` | UserAuth | `{}` |

```ts
// UserRecordView —— 严格白名单序列化,服务端构造,不复用 Record 结构体
{
  id, created_at, model_name, rule_name,       // rule_name 走可配置的"对外文案",不是内部名
  action, fee_quota, fee_status, status,
  counter_after, appeal_status
}
// ★ 绝不返回:matched_terms / match_snippet / payload / ip / channel_id / group_ratio
```

---

## 4. 关键流程

### 4.1 能力 B — 转发前拦截(M1,`controller/relay.go:160` 后)

```
B1  qyviolation.PreRelayGuard(c, relayInfo, meta) 入口
B2  快速门:cfg.Enabled && cfg.PromptGuardEnabled && snapshot.HasPromptRules() 否则 return nil
      —— 3 次原子读,无分配。这是热路径的第一道成本控制
B3  取文本:
      text := meta.CombineText
      if text == "" && cfg.ForceBuildCombineText && relayInfo.Request != nil {
          text = relayInfo.Request.GetTokenCountMeta().CombineText   // 补偿 fastTokenCountMetaForPricing 的空洞
      }
      text = clipHeadTail(text, cfg.MaxScanBytes)      // 头 32K + 尾 32K
B4  作用域过滤:snapshot.PromptRulesFor(model=relayInfo.OriginModelName,
                                     group=relayInfo.UsingGroup,
                                     userId, userGroup)
      —— 命中的规则集在 snapshot 构建时已按 (model,group) 预分桶,运行期是 map 查找 + 小切片遍历
B5  关键词匹配:一次全局 AC 扫描
      hit, words := service.AcSearch(lower(text), snapshot.PromptKeywordDict, false)
      words → snapshot.KeywordToRules[word] → 候选规则集
      (AC 词典由 snapshot 持有,service/str.go 的 acCache 自动按词典哈希复用已编译自动机)
B6  正则匹配:按 priority 升序遍历,最多 cfg.MaxRegexRules 条,预编译的 *regexp.Regexp
B7  取 priority 最小(最高优先级)的一条命中规则作为 verdict;其余命中规则记入 verdict.AlsoMatched
B8  verdict 写入 Context:common.SetContextKey(c, qyKeyVerdict, verdict)  ← 供 M3 中间件后置归档使用
B9  action 不含 block → 走 B12(扣费/记录)后 return nil(放行)
B10 action 含 block:
      ── 同步扣费(若 action 含 charge):见 §4.3
      ── 同步归档入队:见 §4.4
      ── 构造错误并返回:
         types.NewErrorWithStatusCode(
             errors.New(rule.BlockMessage 或默认文案),
             types.ErrorCode("qy_violation."+rule.Id),        // 前缀与 ViolationFeeCodePrefix 不冲突
             http.StatusBadRequest,
             types.ErrOptionWithSkipRetry(),                  // ★ 必须,否则重试其他渠道
             types.ErrOptionWithNoRecordErrorLog(),           // 违规拒绝不该算渠道不可用
         )
B11 上层 `newAPIError = qyErr; return` → controller/relay.go:92 的 defer 按 relayFormat
      自动输出 OpenAI / Claude / Realtime 三种格式。**我们不写任何序列化代码。**
B12 记录 & 计数:全部走异步队列(见 §4.4)
```

**事务边界**:B 阶段无事务。扣费(B10)是主库操作,归档/计数是新库异步操作,两者失败互不影响(fail-open)。

**幂等键**:`rec_no = "vr_" + relayInfo.RequestId + "_" + ruleId`。B 阶段一个请求只会执行一次(在重试循环之前),但 `rec_no` 唯一索引仍是必须的兜底。

**关键顺序**:B10 里**先扣费再返回错误**。若反过来,defer 已经把响应写出去,后续扣费的错误无法反馈,且 `relayInfo.Billing` 此时为 nil(PreConsumeBilling 在 `:167`,尚未执行),无 Refund 干扰。这是 M1 选在 `:160` 而非 `:182` 的核心理由:**避开 pre-consume/refund 的往返**。

### 4.2 能力 A — 事后检测扣费(M2,`controller/relay.go:180` 后)

```
A1  qyviolation.PostRelayGuard(c, relayInfo, newAPIError) 入口
      (在原 defer 内,此时 Billing.Refund 已执行,预扣费已退回 —— 顺序与现有
       ChargeViolationFeeIfNeeded 一致,不会把违规费一起退掉)
A2  快速门:cfg.Enabled && cfg.PostGuardEnabled && apiErr != nil
A3  幂等门:if common.GetContextKeyBool(c, qyKeyPostDone) { return }; 立即置位
      —— defer 在 panic-recover 与未来上游改动下可能重入
A4  构建匹配输入:
      errText   := apiErr.Error()
      oaiMsg    := apiErr.ToOpenAIError().Message
      errCode   := string(apiErr.GetErrorCode())
      status    := apiErr.StatusCode
      rejectRsn := common.GetContextKeyString(c, constant.ContextKeyAdminRejectReason)
      —— rejectRsn 是项目已有的软违规信号(openai_finish_reason=content_filter /
         claude_stop_reason=refusal / gemini_block_reason),白捡的高质量特征
A5  作用域过滤 + 匹配(同 B4~B7,但规则集为 phase in (upstream_err, reject_reason))
A6  无命中 → return
A7  命中 → 扣费(§4.3)+ 归档入队(§4.4)+ 计数入队(§4.5)
A8  return(不改 newAPIError —— 上游错误原样回给用户,只是额外扣了钱)
```

**能否只加 1 行?**能。`PostRelayGuard(c, relayInfo, newAPIError)` 一行即可,不需要修改现有的 `NormalizeViolationFeeError` / `Refund` / `ChargeViolationFeeIfNeeded` 三行。

**与现有 Grok CSAM 逻辑的关系**:现有 `ChargeViolationFeeIfNeeded` 保持不动、继续生效。我们的规则若也匹配同一个错误,会**重复扣费**。解决:
- 内置一条默认规则 `builtin_grok_csam`,`enabled=false`,注释说明「原项目已有,启用前请先在系统设置关闭 Grok 违规扣费」;
- `PostRelayGuard` 开头判断 `service.IsViolationFeeCode(apiErr.GetErrorCode())` 且 `model_setting.GetGrokSettings().ViolationDeductionEnabled == true` → 只记录不扣费(`fee_status = "skipped_dup_builtin"`)。**这是零改原文件的去重方案。**

### 4.3 扣费子流程(A/B 共用)

```
C1  计算基数 base(decimal):
      fixed:                 base = rule.FeeAmount
      model_price_multiple:  base = 单价 × rule.FeeMultiple
                             单价来源 = relayInfo.PriceData.UsePrice
                                          ? PriceData.ModelPrice          // 按次计费模型
                                          : PriceData.ModelRatio × 0.002  // 按量计费:模型倍率折算美元
                             (0.002 = 项目内 ratio→USD 的既定基准,即 $0.002/1K token 的 1 倍率定义)
      prompt_quota_multiple: base 直接以 quota 计:PriceData.QuotaToPreConsume × FeeMultiple
C2  应用分组倍率:base = base × relayInfo.PriceData.GroupRatioInfo.GroupRatio
      groupRatio <= 0 → 直接返回 0(与现有 calcViolationFeeQuota 一致)
C3  上限裁剪:base = min(base, rule.FeeMaxAmount>0 ? rule.FeeMaxAmount : +inf,
                            cfg.MaxFeeUsdPerRequest)
C4  转 quota(AGENTS.md 强制):
      q := decimal(base).Mul(decimal(common.QuotaPerUnit))
      feeQuota, clamp := common.QuotaFromDecimalChecked(q)
      clamp != nil → 记 record.QuotaClamp = clamp.AuditMap() 的 JSON
      feeQuota <= 0 → fee_status="none",不扣费但仍记录
C5  余额策略(GAPS §三.2(4)):见 §6.3
C6  扣费:service.PostConsumeQuota(relayInfo, feeQuota, 0, true)
      —— 自动区分 wallet/subscription、同步扣令牌额度、触发余额告警。不要自己拼
      失败 → fee_status="failed", fee_error=err,**不抛错、不阻断**,继续走 C8
C7  统计:model.UpdateUserUsedQuotaAndRequestCount(userId, feeQuota)
          relayInfo.ChannelId != 0 → model.UpdateChannelUsedQuota(channelId, feeQuota)
          (M1 阶段渠道未选,ChannelId=0,跳过)
C8  写主库计费日志(★ 需求原文「给用户写入一条计费记录告诉原因」):
      model.RecordConsumeLog(c, userId, model.RecordConsumeLogParams{
          ChannelId: relayInfo.ChannelId,
          ModelName: relayInfo.OriginModelName,
          TokenName: c.GetString("token_name"),
          Quota:     feeQuota,
          Content:   "违规扣费:" + rule.PublicReason,     // ← 用户可见的原因文案
          TokenId:   relayInfo.TokenId,
          UseTimeSeconds: int(time.Now().Unix() - relayInfo.StartTime.Unix()),
          IsStream:  relayInfo.IsStream,
          Group:     relayInfo.UsingGroup,
          Other: map[string]any{
              "violation_fee":      true,                  // ← 与现有约定一致,需求 3 靠它排除返佣
              "violation_fee_code": "qy_violation."+ruleId,
              "qy_violation_rec_no": recNo,                // ★ 跨库对账钥匙
              "qy_rule_name":       rule.PublicReason,
              "fee_quota":          feeQuota,
              "base_amount":        base.String(),
              "group_ratio":        groupRatio,
              "admin_info": map[string]any{                // ← model/log.go:116 formatUserLogs
                  "qy_rule_id":       ruleId,              //   会为普通用户 delete("admin_info")
                  "qy_matched_terms": terms,               //   命中词只给管理员看
                  "qy_phase":         phase,
                  "quota_saturation": clampAuditMap,       // AGENTS.md 额度饱和审计
              },
          },
      })
C9  写新库 record(异步队列),fee_* 字段全部落地
```

**为什么 C8 要写主库 logs**:需求原文明确要求「给用户写入一条计费记录告诉原因」。走 `RecordConsumeLog` 意味着用户在**原生使用日志页**就能看到扣费与原因,**前端零改动**。同时 `other.qy_violation_rec_no` 是新库记录与主库日志的唯一关联键,满足 GAPS §三.2(1) 提出的「中间态可对账」要求。

**事务边界**:C6~C8 **没有跨库事务**,也不需要。理由:这是扣费而非划转,主库扣费成功 + 新库记录失败 = 用户少了钱但新库无记录 → 补偿任务通过 `logs.other.qy_violation_rec_no` 反查补齐;主库扣费失败 + 新库有记录 = `fee_status="failed"`,管理端可见可重试。两个方向都不会造成资金凭空产生或消失。

### 4.4 上下文归档子流程

```
D1  仅当 rule.ArchiveContext && cfg.ArchiveEnabled
D2  ★ 同步阶段(必须在 c.Next() 返回、BodyStorageCleanup 执行之前):
      —— router/relay-router.go:16 的 BodyStorageCleanup 在 c.Next() 后释放 body 存储。
         异步 goroutine 读 body 会拿到已释放的存储。所以「取数据」必须同步,「落库」才能异步。
      normalized := buildNormalizedContext(relayInfo, meta)   // 见 §7.2,不含二进制
      files      := describeFiles(meta.Files)                 // 见 §7.3
      raw        := nil
      if cfg.ArchiveRawBody { raw = snapshotBody(c, cfg.ArchiveMaxContextBytes) }
D3  脱敏:redact(normalized) → (text, stats)
D4  截断:clip(text, cfg.ArchiveMaxContextBytes)  头 60% + 命中窗口 + 尾 20%
D5  压缩:zstd level 3 → payload
      len(payload) > cfg.ArchiveMaxPayloadBytes
        → 二次截断到 1/2 重压,最多 3 次;仍超 → 放弃 payload,
          record.HasPayload=false,payload_error="oversize"
D6  投递:asyncQueue <- archiveJob{record, payload}   (非阻塞,满则丢弃并 metrics++)
D7  worker:
      qydb.Transaction:
        Clauses(clause.OnConflict{Columns: rec_no, DoNothing: true}).Create(&record)
        record.Id == 0(冲突)→ 跳过 payload
        else → Create(&payload)
```

### 4.5 计数与自动封号(GAPS §三.2(3) 的完整实现)

```
E1  异步 worker 内执行(不在热路径)
E2  原子 upsert + 返回新计数(MySQL 专用,必须在事务内以固定同一连接执行):
      qydb.Transaction(func(tx *gorm.DB) error {
        now, winStart := time.Now().Unix(), now - int64(cfg.CountWindow.Seconds())
        // LAST_INSERT_ID(expr) 让 UPDATE 分支也能把新值带回连接会话
        err := tx.Exec(`
          INSERT INTO qy_violation_counter
            (user_id, window_start, hit_count, total_count, ban_cycle, last_hit_at, updated_at)
          VALUES (?, ?, ?, ?, 0, ?, ?)
          ON DUPLICATE KEY UPDATE
            hit_count   = LAST_INSERT_ID(IF(window_start < ?, ?, hit_count + ?)),
            window_start= IF(window_start < ?, ?, window_start),
            total_count = total_count + ?,
            last_hit_at = ?,
            updated_at  = ?`,
          uid, now, w, w, now, now,
          winStart, w, w,
          winStart, now,
          w, now, now).Error
        if err != nil { return err }
        // 同连接读回
        return tx.Raw("SELECT LAST_INSERT_ID()").Scan(&newCount).Error
      })
      ★ 必须用 Transaction 而非 db.Exec + db.Raw:GORM 连接池会把两条语句发到不同连接,
        LAST_INSERT_ID() 是**会话级**变量,跨连接读到的是别人的值 —— 这是最隐蔽的一个 bug。
      ★ INSERT 分支不会设置 LAST_INSERT_ID,需单独处理:RowsAffected==1 时 newCount = w
E3  回写 record.counted=true, record.counter_after=newCount
E4  封号判定:
      cfg.BanThreshold > 0 && newCount >= threshold && 上一次 newCount-w < threshold
      —— 「跨越阈值」而非「大于阈值」。由 E2 的原子性保证:
         并发的 N 个 worker 各自拿到唯一的 newCount,只有一个能观察到"跨越"
E5  认领(第二道锁,防 E4 因窗口重置等边界重复触发):
      读当前 ban_cycle → INSERT qy_violation_ban(user_id, ban_cycle, status='pending', ...)
      唯一键 uk_qyvban_user_cycle 冲突 → 已被认领,直接 return
E6  执行封号四步(见 §5)
E7  成功 → ban.status='banned', banned_at=now
    失败 → ban.attempts++, last_error=...;补偿任务重试(指数退避,上限 5 次)
```

### 4.6 撤销 / 退款 / 解封流程

```
F1  管理员 POST /records/:id/revoke {reason, refund:true}
F2  新库事务:
      UPDATE qy_violation_record SET status='revoked', revoked_by, revoked_at, revoke_reason,
             refund_no = 'vrf_'+id
      WHERE id=? AND status='active'         ← RowsAffected=0 → 已撤销,幂等返回
F3  退款(仅当 refund && fee_quota > 0 && fee_status in ('charged','truncated')):
      model.IncreaseUserQuota(userId, feeQuota, true)
      model.RecordLog(userId, model.LogTypeRefund,
          fmt.Sprintf("违规扣费撤销退还 %s(单号 %s)", logger.LogQuota(feeQuota), refundNo))
      —— 用 LogTypeRefund 而非 LogTypeConsume,避免污染消费统计
      成功 → record.fee_status='refunded', refunded_quota=feeQuota
F4  计数回退(原子):
      UPDATE qy_violation_counter
      SET hit_count = GREATEST(hit_count - ?, 0), total_count = GREATEST(total_count - ?, 0)
      WHERE user_id = ? AND window_start = ?     ← 窗口已滚动则不回退(值已失效)
F5  若该记录是某次封号的 trigger_record_id 且 ban.status='banned' → 提示管理员是否解封
      (不自动解封:撤销一条记录不等于全部违规都是误判)
```

解封四步(与封号对称):
```
G1  UPDATE qy_violation_ban SET status='unbanned', unbanned_at, unbanned_by, unban_note
    WHERE id=? AND status='banned'                    ← RowsAffected 保证幂等
G2  UPDATE qy_violation_counter SET ban_cycle = ban_cycle + 1,
        hit_count = IF(?, 0, hit_count)               ← reset_counter 参数
    WHERE user_id = ?
    ★ ban_cycle+1 是关键:不 +1 则下次达阈值时 E5 的唯一键冲突会让封号永远无法再触发
G3  主库:status → UserStatusEnabled + bump auth_version + 缓存刷新(同 §5 的 3~4 步)
G4  审计:model.RecordLogWithAdminInfo(userId, model.LogTypeManage, "违规封禁已解除", {...})
```

---

## 5. 自动封号的四步实现(GAPS §三.2(3) 完整代码)

```go
// qianye/service/violation_ban.go
package qyservice

var ErrBanSkipped = errors.New("qy: ban skipped")

// disableUserForViolation 是「update status + bump auth_version + invalidate 缓存 + 写审计」
// 四步的完整实现。任何一步遗漏都会留下"被禁用但旧 token 仍可用"的安全洞。
func disableUserForViolation(userId int, ban *qymodel.ViolationBan) error {
	// —— 前置豁免检查(读主库,非事务)——
	u, err := model.GetUserById(userId, false)
	if err != nil {
		return err
	}
	if u.Role >= common.RoleRootUser || slices.Contains(cfg.BanExemptRoles, u.Role) {
		return ErrBanSkipped
	}
	if slices.Contains(cfg.BanExemptUsers, userId) {
		return ErrBanSkipped
	}

	// —— 步骤 1 + 2:主库单事务内「改 status」+「递增 auth_version」——
	// 两者必须同事务:先改 status 后崩溃 → 旧 JWT/session 仍有效;
	//                先 bump 后崩溃 → 用户被强制登出但没被禁用(可重新登录)。
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.User{}).
			Where("id = ? AND status = ? AND role < ?",
				userId, common.UserStatusEnabled, common.RoleRootUser).
			Update("status", common.UserStatusDisabled)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 已被禁用 / 是 root / 已删除 —— 幂等成功,不是错误
			return ErrBanSkipped
		}
		// model/user_auth_cache.go:180,已导出。内部会 lockForUpdate + SetUserAuthVersionFence(Redis 栅栏)
		// + CAS 更新 auth_version。栅栏先于提交写入 = fail-closed。
		_, err := model.IncrementUserAuthVersionWithTx(tx, userId)
		return err
	})
	if errors.Is(err, ErrBanSkipped) {
		return ErrBanSkipped
	}
	if err != nil {
		return err
	}

	// —— 步骤 3:三处缓存失效,缺一不可 ——
	// 3a. 刷新用户 hash(含 status)。middleware/auth.go:450 的
	//     `userEnabled := userCache.Status == common.UserStatusEnabled`
	//     读的就是这份缓存 —— 这一步决定了 relay 热路径能否立刻拦住。
	if e := model.PublishUserAuthCache(userId); e != nil {
		logger.SysError("qy: PublishUserAuthCache failed: " + e.Error())
		// 兜底:退化为删缓存,让下次请求回源 DB
		_ = model.InvalidateUserCache(userId)
	}
	// 3b. 清 PAT / relay 令牌缓存。middleware/auth.go:317 读 token 缓存判断 token.Status,
	//     不清则已缓存的令牌在 TTL 内仍能通过第一道校验。
	if e := model.InvalidateUserTokensCache(userId); e != nil {
		logger.SysError("qy: InvalidateUserTokensCache failed: " + e.Error())
	}
	// 3c. 吊销浏览器会话(控制台侧)。auth_version 栅栏已经能拒绝,
	//     但显式吊销会把 user_sessions 行标记为 revoked,管理端可见,也避免栅栏 TTL 过期后复活。
	if _, e := model.RevokeAllUserSessions(userId, "qy_violation_auto_ban"); e != nil {
		logger.SysError("qy: RevokeAllUserSessions failed: " + e.Error())
	}

	// —— 步骤 4:审计(主库 logs,与 controller.ManageUser 的口径一致)——
	model.RecordLogWithAdminInfo(userId, model.LogTypeManage,
		fmt.Sprintf("账号因违规次数达到阈值(%d 次)被系统自动禁用", ban.HitCountAt),
		map[string]interface{}{
			"source":            "qy_violation",
			"qy_ban_id":         ban.Id,
			"qy_ban_cycle":      ban.BanCycle,
			"qy_threshold":      ban.Threshold,
			"qy_hit_count":      ban.HitCountAt,
			"qy_trigger_record": ban.TriggerRecordId,
		})
	return nil
}
```

**为什么不直接用 `user.Update(false)`**:它内部是 `Updates(struct)`,零值跳过,无 `RowsAffected` 幂等保护;多节点并发时会双写、双 bump、双吊销、双审计。上面的条件 UPDATE 是**天然幂等**的。

`user.Update(false)` 仍是可接受的替代(与 `controller.ManageUser:1243` 完全同路径),若选它需补 `model.InvalidateUserTokensCache` 并接受重复审计。**推荐上面的显式四步。**

**邮件通知(建议)**:步骤 4 后 `gopool.Go(func(){ ... common.SendEmail(...) })`,异步、失败不影响封号。

---

## 6. 并发与边界

### 6.1 `meta.CombineText` 可能为空 —— 静默失效陷阱

`controller/relay.go:131-137`:

```go
if needSensitiveCheck || needCountToken {
    meta = request.GetTokenCountMeta()          // CombineText 已构建
} else {
    meta = fastTokenCountMetaForPricing(request) // ★ CombineText 为空字符串
}
```

若部署方关闭了 `CheckSensitiveEnabled` 且 `CountToken=false`,我们拿到的 `meta.CombineText == ""`,所有 prompt 规则静默失效,**没有任何报错**。

处理:`cfg.ForceBuildCombineText`(默认 `true`)。为空且存在 prompt 规则时自行调 `relayInfo.Request.GetTokenCountMeta()`。代价是一次 `strings.Join`(与开启敏感词检测的部署等同)。管理端 `/health` 暴露 `combine_text_rebuilt_ratio` 指标,让运维知道自己在付这笔钱。

### 6.2 竞态清单

| 竞态 | 场景 | 处理 |
|---|---|---|
| 同请求重复扣费 | defer 重入 / 未来上游在重试循环内也调 PostGuard | Context 幂等位 `qyKeyPostDone` + `rec_no` 唯一索引 |
| 多节点同时跨阈值 | 用户并发 100 请求,5 个同时把计数推到 5 | `LAST_INSERT_ID` 原子 upsert 保证每个 worker 拿到唯一 newCount;只有观察到「跨越」的那个继续;再叠加 `uk_qyvban_user_cycle` 唯一键认领 |
| 封号后仍有 in-flight 请求 | 封号瞬间已有 200 个请求在链路中 | 无法也不必阻止。它们各自的扣费/计数照常;计数会超过阈值但认领锁保证只封一次 |
| 解封后立即再次达阈值 | | `ban_cycle+1` 开启新周期,认领锁不冲突;`reset_counter` 决定是否清零 |
| 计数窗口滚动与并发 | 两个 worker 同时判断窗口过期 | `IF(window_start < ?, ...)` 在单条 SQL 内完成判断与重置,原子 |
| 规则快照读写 | 管理员改规则时热路径正在读 | `atomic.Pointer[Snapshot]` 整体替换,读端零锁;旧快照被 GC 时正在用它的请求已持有引用 |
| 撤销与计数回退 | 撤销时窗口已滚动 | F4 带 `window_start = ?` 条件,窗口变了就不回退(旧值已不在窗口内) |
| 退款重复 | 管理员连点两次 revoke | F2 的 `status='active'` 条件 + `RowsAffected` 保证只有一次进入 F3 |

### 6.3 余额不足 / 负数 / 溢出(GAPS §三.2(4))

`model.DecreaseUserQuota` 是无条件 `quota = quota - ?`,**没有余额校验**。四种策略(`insufficient_balance_policy`):

```go
switch cfg.InsufficientBalancePolicy {
case "truncate":              // ★ 默认
    // relayInfo.UserQuota 是 GenRelayInfo 时的快照;更准确用 model.GetUserCache(uid).Quota
    avail := currentQuota(relayInfo.UserId)
    if avail <= 0 {
        feeStatus = "skipped_insufficient"; feeQuota = 0
    } else if feeQuota > avail {
        feeStatus = "truncated"; feeQuotaWant = feeQuota; feeQuota = avail
    }
case "allow_negative":
    // 原样扣,余额变负。record.fee_status="charged",额外记 debt = want - avail
case "debt":
    // 截断到 0 或 avail,差额写 qy_violation_debt 挂账(表结构预留,追扣为二期)
case "ban_only":
    // 不扣费,直接把 count_weight 视为 threshold(立即触发封号判定)
    feeStatus = "skipped_insufficient"; feeQuota = 0; weight = cfg.BanThreshold
}
```

**已知不可消除的竞态**:`avail` 的读与 `PostConsumeQuota` 的写不是原子的。并发违规扣费仍可能把余额扣成小额负数(超额部分 ≤ 并发数 × 单笔费用)。

不做 `WHERE quota >= ?` 条件扣减的理由:那样必须绕过 `service.PostConsumeQuota`,而它是唯一同时维护 **wallet/subscription 分流 + 令牌额度同步 + Redis quota 缓存(`cacheDecrUserQuota` 未导出)+ 余额告警** 的入口。绕过它会造成缓存与 DB 不一致 —— 比小额负数严重得多。

**记录 `fee_quota_want` 与 `fee_quota` 两个字段**就是为了让这个偏差在管理端可见可审计。

**溢出**:全部经 `common.QuotaFromDecimalChecked`,`MaxQuota = math.MaxInt32`。clamp 发生时:
- `record.QuotaClamp` 落 `clamp.AuditMap()` JSON;
- `logs.other.admin_info.quota_saturation` 同步落一份(AGENTS.md 额度饱和审计要求);
- 管理端 `/stats` 单独统计 clamp 次数并高亮告警(clamp 几乎必然意味着规则配置错误)。

**下界**:`feeQuota <= 0` 一律不调 `PostConsumeQuota`(否则 `quota<0` 会被 `IncreaseUserQuota` 当退款处理,变成**给违规用户送钱**)。这是 `PostConsumeQuota` 的符号语义陷阱,必须显式挡。

### 6.4 其他边界

- **`relayInfo.PriceData` 为零值**:M1 挂在 `:160` 之后,`ModelPriceHelper` 必然已执行且 `info.PriceData` 已赋值(`relay/helper/price.go:182`/`:335`)。但 `PriceData.GroupRatioInfo.GroupRatio` 在 tiered_expr 分支也会赋值(`:335` 前的 `groupRatioInfo`)。防御:`groupRatio <= 0 → feeQuota = 0`,与现有 `calcViolationFeeQuota:88` 行为一致。
- **`FreeModel`**:免费模型也可能违规。设计上**照常扣费**(`model_price_multiple` 会算出 0,`fixed` 仍生效)。这是合理的:免费模型的滥用成本更需要惩罚。
- **Playground**:`relayInfo.IsPlayground=true` 时 `PostConsumeQuota` 会跳过令牌扣减。`cfg.ChargeOnPlayground=false`(默认)时整体跳过扣费,只记录。
- **`relayInfo.ChannelId`**:M1 阶段 `ChannelMeta` 为 nil,访问 `relayInfo.ChannelId` 会 **panic**(嵌入指针)。必须 `if relayInfo.ChannelMeta != nil` 保护。这是最容易踩的空指针。
- **超长 `matched_terms`**:`AcSearch(stopImmediately=false)` 在词表大、文本长时可能返回上万个命中。必须 `terms = terms[:min(len,16)]` 且单项截断 64 字符,否则 `varchar(1024)` 写入失败(MySQL 严格模式下报错而非截断)。
- **正则 ReDoS**:Go `regexp` 是 RE2,线性时间,无回溯灾难。真正的风险是**超长输入**,由 `max_scan_bytes` 兜住。
- **UTF-8 截断**:`clipHeadTail` 必须按 rune 边界切,否则 `AcSearch([]rune(text))` 会产生替换字符,且写库时 `utf8mb4` 报 `Incorrect string value`。用 `utf8.DecodeLastRuneInString` 回退到边界。

---

## 7. 上下文归档专章(GAPS §四② 的重头)

### 7.1 体积估算(先算账再定方案)

假设 1000 次违规/天:

| 归档内容 | 单条 | 日增 | 月增 | 结论 |
|---|---|---|---|---|
| 纯元数据(record 行) | ~1 KB | 1 MB | 30 MB | 无压力 |
| 归一化文本上下文(压缩前 4 KB) | ~1.3 KB(zstd ~3.2x) | 1.3 MB | 40 MB | 无压力 |
| 长上下文场景(压缩前 256 KB 截断上限) | ~40 KB | 40 MB | **1.2 GB** | 需保留期 |
| **原始 body 含 base64 图片(10 张 × 1 MB)** | **~10 MB** | **10 GB** | **300 GB** | **不可接受** |
| body 上限场景(128 MB) | 128 MB | — | — | **绝对不可接受** |

**硬性结论**:
1. **base64 二进制一律不入库。** `archive_raw_body` 默认 `false`,即便开启也走同样的 `archive_max_context_bytes` 截断,且在截断前先做「data URI 剥离」(把 `data:image/png;base64,....` 替换成 `«image/png,1048576B,sha256:ab12…»` 描述符)。
2. 归档的是**归一化上下文**,不是原始 body。这既小又更适合人看。
3. 三层容量闸:`archive_max_context_bytes`(压缩前 256KB)→ zstd → `archive_max_payload_bytes`(压缩后 1MB)。

**MySQL 相关的两个真实陷阱**:
- `MEDIUMBLOB` 上限 16 MB。1 MB 默认值远低于它,安全。
- `max_allowed_packet`:MySQL 8 默认 64 MB,但 **MySQL 5.7 默认 4 MB**,不少云 RDS 也设 4~16 MB。压缩后 1 MB + 行其他字段远低于 4 MB,**任何常见配置都安全**。若管理员把 `archive_max_payload_bytes` 调到 8 MB 以上,`/health` 接口要给出警告。

### 7.2 归一化上下文结构

```jsonc
{
  "v": 1,
  "meta": {
    "request_id": "…", "model": "gpt-4o", "group": "vip",
    "relay_format": "openai", "phase": "prompt", "created_at": 1730000000
  },
  "messages": [
    { "role": "system",    "text": "You are…" },
    { "role": "user",      "text": "…", "parts": [ {"kind":"image","ref":"f0"} ] },
    { "role": "assistant", "text": "…" }
  ],
  "tools":  ["get_weather", "search"],       // 只存名字,不存 schema(schema 可以极大且无违规信息)
  "params": { "temperature": 0.7, "max_tokens": 4096, "stream": true },
  "hit": {
    "rule_id": 12, "terms": ["…"],
    "windows": [ { "at": 1832, "text": "…±160 字符…" } ]
  },
  "upstream": {                              // phase = upstream_err / response 时
    "status_code": 400, "error_code": "…", "error_message": "…",
    "reject_reason": "openai_finish_reason=content_filter"
  },
  "truncated": { "head_bytes": 157286, "tail_bytes": 52428, "dropped_bytes": 8912345 }
}
```

构建来源:优先 `relayInfo.Request`(`dto.Request` 接口,全 relay format 通用)→ 按具体类型断言取 messages;取不到则回落到 `meta.CombineText`(纯文本,丢失 role 结构但仍可用)。**不要为每种协议写解析器** —— `GetTokenCountMeta()` 已经做了归一化,`CombineText` 是保底。

### 7.3 多模态处理

```go
type FileDescriptor struct {
    Ref       string `json:"ref"`        // "f0" "f1",与 messages.parts.ref 对应
    Kind      string `json:"kind"`       // image | audio | video | file(来自 types.FileType)
    Origin    string `json:"origin"`     // "url" | "base64"
    URL       string `json:"url,omitempty"`      // origin=url 时,已剥离 query string
    MIME      string `json:"mime,omitempty"`     // 从 data URI 前缀解析
    Bytes     int64  `json:"bytes"`
    SHA256    string `json:"sha256"`             // 全量哈希,用于跨记录识别同一张图/建黑名单库
    Magic     string `json:"magic,omitempty"`    // 前 16 字节 hex,校验声明 MIME 是否伪造
    Detail    string `json:"detail,omitempty"`   // types.FileMeta.Detail(low/high/auto)
    Thumbnail string `json:"thumb,omitempty"`    // 可选,256px JPEG 的 base64,硬上限 32KB
}
```

数据源:`meta.Files []*types.FileMeta`,`FileMeta.Source.GetRawData()` / `IsURL()` / `GetIdentifier()`。

- **默认不存缩略图**。`archive_keep_image_thumbnail=true` 时才生成,且要在配置注释里写明**合规风险**:CSAM 类违规的图片留存本身可能违法,应咨询法务后再开启。这是必须给用户的警告。
- `SHA256` 是这套设计里最有价值的字段:同一张违规图被多个账号反复上传时,可以做「按哈希封禁」而不需要保存图片本体。
- `archive_max_files` 上限 32,超出只记数量。

### 7.4 脱敏

在压缩前对归一化 JSON 的所有字符串叶子执行:

| 类型 | 正则(示意) | 替换 |
|---|---|---|
| email | `[\w.+-]+@[\w-]+\.[\w.]+` | `«email»` |
| phone_cn | `(?:\+?86)?1[3-9]\d{9}` | `«phone»` |
| id_card_cn | `[1-9]\d{5}(19\|20)\d{2}...[\dXx]` | `«idcard»` |
| bank_card | `\b\d{16,19}\b`(Luhn 校验通过才替换) | `«bankcard»` |
| api_key | `sk-[A-Za-z0-9_-]{16,}` / `AKIA[0-9A-Z]{16}` 等 | `«apikey»` |
| bearer | `(?i)bearer\s+[A-Za-z0-9._-]{16,}` | `«bearer»` |
| url_query | URL 的 `?...` 部分 | 剥离 |

- **命中片段本身不脱敏**(管理员必须看到违规内容才能判断),但仍会被上述规则处理 —— 若违规内容里恰好含邮箱,邮箱会被替换,这是可接受的。
- `redact_stats` 记录每类替换次数,让管理员知道「这条记录有多少信息被隐去」。
- **未脱敏原文**只通过 `GET /records/:id/context/raw` 提供,要求 `RootAuth` + `SecureVerificationRequired`,并写 `RecordOperationAuditLog`。但注意:脱敏是**写入前**执行的,原文并未保存 —— 所以 `/raw` 实际返回的是「未做展示层二次遮罩」的版本。若确实需要保留真正的原文,需 `redact_enabled=false`,并在配置注释里写明这会把用户 PII 落进你的数据库(GDPR/个保法风险)。**默认必须开启脱敏。**

### 7.5 保留期与清理

```
retention_gc 任务(需持有 qy_task_lease 'violation:retention_gc',间隔 cfg.GCInterval):
  1. 删 payload:DELETE FROM qy_violation_payload
                 WHERE created_at < ? ORDER BY created_at LIMIT ?     (batch=500)
                 循环直到 RowsAffected < batch;每批之间 sleep 200ms(避免长事务与从库延迟)
     storage_kind='fs'/'s3' 的行先删外部对象再删行
  2. 同步 record.has_payload = false
  3. 删 record:DELETE FROM qy_violation_record
               WHERE created_at < ? AND status != 'appealed' ORDER BY created_at LIMIT ?
     ★ 申诉中的记录不删(否则申诉流程断链)
  4. 删已结束的 counter:window_start 过期且 hit_count=0 且 ban_cycle=0 的行
```

**分表建议(二期)**:record 行超 1000 万时,按月分表 `qy_violation_payload_YYYYMM`,`DROP TABLE` 代替 `DELETE`(避免 InnoDB 空间不回收)。当前设计用「独立表 + 批量 DELETE + `OPTIMIZE TABLE` 季度维护」即可覆盖到千万级。

---

## 8. 热路径性能

### 8.1 预算

| 阶段 | 目标 | 实现保证 |
|---|---|---|
| 总开关关闭 | < 50 ns | 3 次 atomic load,零分配,内联 |
| 无匹配规则 | < 5 μs | 快照预分桶 map 查找,空桶直接返回 |
| AC 扫描 64 KB | < 300 μs | `service.AcSearch` 自带编译缓存;AC 匹配 ~200 MB/s/core |
| 32 条 RE2 正则 × 64 KB | < 1.5 ms | RE2 线性;`max_regex_rules` 硬上限 |
| **P99 增量** | **≤ 2 ms** | 上述之和 |
| 新库同步访问 | **0 次** | 热路径只读内存快照;所有写走异步队列 |
| 主库同步访问 | 仅命中且扣费时 | 与现有 `ChargeViolationFeeIfNeeded` 同量级 |

`CombineText` 重建(§6.1)是唯一的大额外开销(与开启敏感词检测等价)。指标暴露在 `/health`。

### 8.2 异步队列

```go
type asyncQueue struct {
    ch      chan job          // 容量 cfg.AsyncQueueSize(4096)
    dropped atomic.Int64
}
func (q *asyncQueue) TryPush(j job) {
    select {
    case q.ch <- j:
    default:
        q.dropped.Add(1)      // ★ 永不阻塞热路径。丢弃是可接受的降级
    }
}
```
- `cfg.AsyncWorkers`(默认 2)个 worker,每个持有 `context.WithTimeout(cfg.DBTimeout)`。
- worker 内所有 DB 操作套 `qydb.WithContext(ctx)`,2s 超时。
- **计数与封号也走这个队列**,所以封号延迟 ≈ 队列延迟(正常 < 10ms,极端积压 < 数秒)。这是刻意的取舍:封号即时性 vs 热路径延迟,选后者。
- 进程退出时 `Close()` + drain(上限 5s),避免丢记录。

### 8.3 熔断(fail-open,地基第 4 条)

```go
type breaker struct {
    consecutiveFail atomic.Int32
    openUntil       atomic.Int64   // unix nano
}
// 任一新库操作失败 → consecutiveFail++;成功 → 归零
// consecutiveFail >= cfg.CircuitErrorThreshold(10)
//   → openUntil = now + cfg.CircuitOpenDuration(30s),并 SysError 一次(不刷屏)
// 熔断打开期间:
//   - 规则快照:保持最后一次成功加载的内存副本(不清空!)→ 检测能力不降级
//   - 异步队列:直接丢弃并计数 → 记录能力降级
//   - 扣费:照常(走主库,与新库无关)→ 惩罚能力不降级
//   - 计数与封号:暂停 → 封号能力降级(可接受)
// 热路径永远不因新库故障返回错误。
```

冷启动特例:进程启动时新库就连不上 → 快照为空 → **不检测任何规则**(fail-open)。`/health` 返回 `rule_count: 0, snapshot_age_ms: -1`,管理端页面顶部红色横幅告警。

### 8.4 M3 中间件(可选层)的额外成本

若 `middleware_enabled=false`(默认),中间件仍会被注册,但函数体第一行就 `c.Next(); return` —— 成本 ≈ 一次函数调用(~5 ns)。

若启用:
- pre-phase:查封禁缓存(进程内 LRU,TTL 5s,miss 时不查库直接放行)+ 可选全局黑名单 AC;
- post-phase:仅当 Context 里有 verdict 时才做 body 快照(§4.4 D2)。**不做无差别的响应体 tap** —— 那会给所有流式请求增加一份全量内存副本,代价过高。
- `response_scan_enabled=true` 时才包装 `c.Writer`,且必须实现 `Unwrap() http.ResponseWriter`,否则 `relay/helper/stream_scanner.go:74` 的 `http.NewResponseController` 拿不到 `SetWriteDeadline`,会丢失流式写超时保护(不崩溃,但静默降级)。包装器带 `maxSize` 上限(默认 256KB),超出停止累积。

---

## 9. 原项目改动清单

### 改动 1 — `controller/relay.go` import

**位置**:`controller/relay.go:27` 之后(`operation_setting` 那行后,空行 `:28` 之前)
**风险**:**低**(import 块冲突易解)

```go
	qyviolation "github.com/QuantumNous/new-api/qianye/service/violation"
```

### 改动 2 — `controller/relay.go` 能力 B 挂载点

**位置**:`controller/relay.go:160` 之后(`priceData, err := helper.ModelPriceHelper` 的 `if err != nil {…}` 闭合花括号之后,`:162` 的注释行之前)
**风险**:**高**(该文件是上游最高频改动文件之一)

```go
	if qyErr := qyviolation.PreRelayGuard(c, relayInfo, meta); qyErr != nil {
		newAPIError = qyErr
		return
	}
```

**为什么必须是这个位置**:
- 早于 `:167` `PreConsumeBilling` → 阻断请求不产生预扣费/退款往返;
- 晚于 `:156` `ModelPriceHelper` → `relayInfo.PriceData` 已由 `relay/helper/price.go:182` 赋值,`model_price_multiple` 扣费可用;
- 早于 `:194` 重试循环 → 真正的「转发前」;
- 返回的 `newAPIError` 由 `:92` 的 defer 自动按 `relayFormat` 序列化成 OpenAI / Claude / Realtime 三种格式,我们零序列化代码。

### 改动 3 — `controller/relay.go` 能力 A 挂载点

**位置**:`controller/relay.go:180` 之后(现有 `service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)` 之后,`:181` 的 `}` 之前)
**风险**:**高**(同上,但该 defer 块本身很稳定)

```go
			qyviolation.PostRelayGuard(c, relayInfo, newAPIError)
```

**顺序正确性**:排在 `Billing.Refund` 与 `ChargeViolationFeeIfNeeded` 之后,与现有语义一致(先退预扣费,再扣违规费),不会被 Refund 冲掉。

### 改动 4 — `router/relay-router.go` import(仅当启用 M3/M4)

**位置**:`router/relay-router.go:8` 之后
**风险**:**低**

```go
	qymw "github.com/QuantumNous/new-api/qianye/middleware"
```

### 改动 5 — `router/relay-router.go` M3(仅当启用)

**位置**:`router/relay-router.go:73` 之后(`relayV1Router.Use(middleware.ModelRequestRateLimit())` 之后,`:74` 的 `{` 之前)
**风险**:**中**(该文件随新端点增加而变,但这一段的中间件序列稳定)

```go
	relayV1Router.Use(qymw.ViolationGuard())
```

一行覆盖 `wsRouter` + `httpRouter` 两个子组的全部 `/v1/**` 端点(chat/completions、messages、responses、embeddings、images、audio、rerank、moderations)。

### 改动 6 — `router/relay-router.go` M4 Gemini 原生(仅当启用)

**位置**:`router/relay-router.go:198` 之后(`relayGeminiRouter.Use(middleware.ModelRequestRateLimit())` 之后,`:199` 的 `Distribute()` 之前)
**风险**:**中**

```go
	relayGeminiRouter.Use(qymw.ViolationGuard())
```

### 预算合计

| 文件 | 行数 | 风险 |
|---|---|---|
| `controller/relay.go` | 5 | 高 |
| `router/relay-router.go` | 3 | 中 |
| **合计** | **8 行 / 2 文件** | |

**不需要**:`model/qy_export.go`(§0.3)、`main.go`(由地基统一挂载)、`service/violation_fee.go`(不动)、`setting/model_setting/grok.go`(不动)、任何 `relay/channel/**`(不做 per-adaptor 阻断)。

### 明确放弃的方案(供决策)

| 方案 | 放弃理由 |
|---|---|
| 在 `relay/channel/openai/relay-openai.go:334` 等处做**响应阻断** | 需要改 4~6 个 adaptor 文件,且只对非流式有效;流式因 `lastStreamData` 一帧延迟机制无法真正阻断。收益远小于冲突成本 |
| 在 `service/text_quota.go:397` `PostTextConsumeQuota` 挂成功路径 hook | 拿不到响应文本;`ContextKeyAdminRejectReason` 通过 M3 中间件后置阶段同样能读到,不必改这个文件 |
| 修改 `RelayInfo` 结构体加字段 | 改原文件。改用 `gin.Context` + `constant.ContextKey("qy_violation_verdict")` 传递(`ContextKey` 是 `type ContextKey string`,可在包外构造,无需改 `constant/context_key.go`) |
| 修改 `service/violation_fee.go` 加 hook | 与直接改 `controller/relay.go` 行数相当,但多一个文件的冲突面 |

---

## 10. 前端页面

### 10.1 管理端(4 个子页,收敛成 1 个二级工作区)

按 `recon-frontend-arch.md` §九.C 的 drill-in 方案,**只改 2 个原有文件各 1 行**。

**新建文件**

| 路径 | 说明 |
|---|---|
| `web/src/routes/_authenticated/qy-violation/route.tsx` | 布局路由,`beforeLoad` 要求 `ROLE.ADMIN` |
| `web/src/routes/_authenticated/qy-violation/index.tsx` | 重定向到默认 section `records` |
| `web/src/routes/_authenticated/qy-violation/$section.tsx` | section 路由 + zod `validateSearch` |
| `web/src/components/layout/config/qy-violation.config.ts` | 导出 `QY_VIOLATION_VIEW: SidebarView`(`pathPattern: /^\/qy-violation(\/\|$)/`) |
| `web/src/features/qy-violation/section-registry.tsx` | `createSectionRegistry`,4 个 section |
| `web/src/features/qy-violation/api.ts` | `import { api } from '@/lib/api'`,全部 `/api/qy/violation/**` |
| `web/src/features/qy-violation/types.ts` | Rule / Record / Ban / Appeal / Counter |
| `web/src/features/qy-violation/constants.ts` | 枚举 + i18n key 常量(需在 `static-keys.ts` 登记) |
| `web/src/features/qy-violation/components/rules-table.tsx` | 规则列表(`DataTablePage`) |
| `web/src/features/qy-violation/components/rule-mutate-drawer.tsx` | 规则新建/编辑(`Sheet` + RHF + Zod) |
| `web/src/features/qy-violation/components/rule-tester.tsx` | **规则试跑面板**:粘一段文本 + 选模型/分组 → 实时显示是否命中、命中词、耗时 |
| `web/src/features/qy-violation/components/records-table.tsx` | 违规记录列表 |
| `web/src/features/qy-violation/components/record-context-dialog.tsx` | **上下文查看器**:messages 时间线、命中处高亮、多模态卡片(哈希/大小/MIME/URL)、截断提示条、脱敏统计条、「查看未脱敏原文」按钮(触发二次验证) |
| `web/src/features/qy-violation/components/bans-table.tsx` | 封禁 + 计数排行(双 Tab) |
| `web/src/features/qy-violation/components/appeals-table.tsx` | 申诉队列 |
| `web/src/features/qy-violation/components/appeal-review-dialog.tsx` | 复核(通过/驳回 + 退款/解封/清零 三个开关) |
| `web/src/features/qy-violation/components/health-banner.tsx` | `/health` 状态条:熔断打开 / 队列丢弃 / 规则数为 0 时红色横幅 |
| `web/src/features/qy-violation/lib/rule-form.ts` | `getRuleFormSchema(t)`,含 `action`↔`phase`↔`fee_mode` 的联动校验 |
| `web/src/lib/qy-route-guards.ts` | `requireAdmin()`,只给新路由用,不重构原有 8 处 |

**必改的原有文件**

| 文件 | 改动 |
|---|---|
| `web/src/hooks/use-sidebar-data.ts` | `id: 'admin'` 分组 items 数组 **+1 项**:`{ title: t('qy_violation_nav'), url: '/qy-violation/records', icon: ShieldAlert, activeUrls: ['/qy-violation'] }` |
| `web/src/components/layout/lib/sidebar-view-registry.ts:33` | `SIDEBAR_VIEWS` 数组 **+1 元素** `QY_VIOLATION_VIEW` |
| `web/src/i18n/locales/*.json` ×7 | 追加 `qy_violation_*` 下划线扁平键(**不用点号**) |
| `web/src/i18n/static-keys.ts` | 登记 `constants.ts` 里的非字面量 key |
| `web/src/routeTree.gen.ts` | 自动重生成,冲突时删除重跑 build |

**主要交互**
- 规则表:开关列直接 toggle(乐观更新 + `invalidateQueries(['qy-violation-rules'])`);优先级列支持拖拽排序(可选,二期)。
- 规则编辑抽屉:`action` 选 `block` 时自动锁定 `phase='prompt'` 并给出说明;`fee_mode='model_price_multiple'` 时展示实时预览「以 gpt-4o + vip 分组为例,单次约扣 $X.XX」。
- 记录表:行内展示 `match_snippet`(`TruncatedCell`),点击行打开上下文抽屉;批量撤销;按 `request_id` 一键跳转原生使用日志页。
- 封禁表:pending/failed 状态高亮 + 「重试」按钮(对应 `/bans/:id/retry`)。

### 10.2 用户端(1 个页面)

| 路径 | 说明 |
|---|---|
| `web/src/routes/_authenticated/qy-my-violations/index.tsx` | 无 `beforeLoad`(父路由已保证登录) |
| `web/src/features/qy-my-violations/index.tsx` | `SectionPageLayout` |
| `web/src/features/qy-my-violations/components/summary-card.tsx` | 「当前窗口违规 2 / 5 次」进度条 + 说明 |
| `web/src/features/qy-my-violations/components/records-list.tsx` | 记录列表 + 每行「申诉」按钮 |
| `web/src/features/qy-my-violations/components/appeal-dialog.tsx` | 申诉表单(理由 ≥ 20 字,`CriticalRateLimit`) |

侧边栏加到 `id: 'personal'` 分组。

**空态文案**(建议,别忘了):
- 无违规记录:「暂无违规记录 — 你的账号使用记录良好」+ 平台使用规范链接;
- 有记录但全部已撤销:「历史记录已全部撤销」;
- 功能未启用(`/health` 返回 disabled):整页隐藏(侧边栏项也隐藏,由自建 hook 读 `/api/qy/violation/health` 决定)。

---

## 11. 用户端可见性(问题 10 的明确建议)

**建议:给看,但严格分层。**

| 信息 | 用户可见 | 理由 |
|---|---|---|
| 违规发生的时间、模型、扣费金额 | ✅ | 钱被扣了必须给理由,否则是黑箱扣费,会产生大量工单与信任危机 |
| 规则的**对外文案**(`rule.PublicReason`,如「涉及未成年人内容」) | ✅ | 需求原文「告诉原因,为何扣费」 |
| 当前违规计数 / 封号阈值 / 剩余次数 | ✅ | **威慑价值 > 泄露价值**。用户知道「再违规 2 次就封号」会主动收敛;不知道则只会在被封后申诉 |
| 申诉入口与状态 | ✅ | 见 §11 误判申诉 |
| **命中的具体关键词 / 正则** | ❌ | 等于把规则库送给刷子。一次探测就能反推词表 |
| **命中片段 / 完整上下文** | ❌ | 同上,且可能含他人 PII |
| 规则内部名、rule_id、severity、channel_id、group_ratio、IP | ❌ | 无业务价值,徒增攻击面 |

**实现方式**:`UserRecordView` 由服务端**白名单构造**(不是给 `Record` 加 `json:"-"`)。理由:`json:"-"` 是负面清单,新增字段时会默认泄露;白名单是正面清单,新增字段默认不泄露。

**零前端改动的兜底通道**:扣费已经通过 `RecordConsumeLog` 落进主库 `logs`,`Content = "违规扣费:<对外文案>"`,命中词放 `other.admin_info`(`model/log.go:116` 的 `formatUserLogs` 会为普通用户 `delete(otherMap, "admin_info")`)。**即使不做用户端页面,用户在原生使用日志页也能看到扣费与原因。** 这条通道必须保证正确,因为它是所有前端改动全部回滚后的最后防线。

⚠️ 注意一处已知不一致:`model/log.go:126` 的 `delete(otherMap, "reject_reason")` **是被注释掉的**,即上游违规原因目前对普通用户可见。我们的 `qy_rule_name` 也在顶层 `other` 里,同样可见 —— 这是刻意的(需求要求告知原因)。但 `qy_matched_terms` 必须在 `admin_info` 里。

---

## 12. 我建议补充的

以下均为需求未提、但不做会出问题的设计,**明确标注为建议**。

### 12.1 误判申诉与复核闭环(强烈建议,已在 §1.7/§3.5/§4.6/§10 展开)
自动扣费 + 自动封号 = 必然产生误判。没有申诉通道时,误判会全部变成工单/差评/退款请求。申诉流程本身也是**规则质量的反馈回路**:`/stats` 里「某规则的申诉通过率」直接告诉管理员哪条规则该改。

**建议指标**:规则维度的 `申诉通过数 / 命中数 > 20%` 时管理端自动标红该规则。

### 12.2 影子模式(Shadow Mode)—— 最重要的一条建议

> **本节已按第六批「裁决 2」重写。** 早期版本写的是「规则照常匹配、照常记录、
> **照常计数**;但不扣费、不阻断、不封号」—— 这两句自相矛盾,而代码当时是照着
> 前半句实现的。详见下面的 12.2.1。

影子模式开启时:
- 规则照常匹配、照常落一条 `qy_violation_record`(含证据与请求上下文)、照常进统计;
- **不扣费、不阻断、不封号,也不推进违规计数**;
- 记录 `record.shadow = true`,`record.counted = false`,
  `record.counter_after = -1`(哨兵值,见 `violation.CounterAfterShadow`);
- `fee_quota_want` 仍然算准并落库 —— 「若真实执行会扣多少钱」是影子模式的全部价值。

**理由**:一条正则或词表上线即刻作用于全量流量。没有影子模式,任何配置失误的后果是「几分钟内几百个用户被误扣费/误封号」,且**扣费与封号都很难完全回滚**(用户已经收到错误、已经登出、已经发工单)。影子模式让管理员先跑一周看命中分布再切真实模式。

同时支持**规则级** `dry_run` 字段(`qy_violation_rule` 的 `DryRun bool`),允许单条规则灰度。

#### 12.2.1 影子命中为什么必须一次都不碰计数器

自动封号的判据是 `reachedThreshold(after, threshold)`,也就是
`hit_count >= auto_ban_threshold`,**完全由持久化的 `qy_violation_counter.hit_count` 推导**。

早期实现只跳过了最后一步(不执行封号),`bumpCounter` 照常调用。后果是延迟发作的:

| 时刻 | 事件 | `hit_count` | 结果 |
|---|---|---|---|
| T1 | 影子命中 | 1 | 只记录 |
| T2 | 影子命中 | 2 | 只记录 |
| T3 | **真实**命中 | 3 | 达阈值 → 落 `qy_violation_ban(status=pending)` → `runBanCompensate` 5 分钟内执行封号 |

也就是说,阈值为 3 时**用户只真实违规了一次就被封号**,而封禁行的
`trigger_record_id` 指向的是那条真实记录,事后从库里完全看不出前两次是影子。
「影子模式不会真实执行」在那个版本里是假的。

现在的口径(裁决 2 原话:「不扣费,不封号,**不记录违规次数**,存入日志,请求上下文,
供管理员核查」):`guard.persistRecord` 在写完记录与证据之后、调用 `bumpCounter` 之前
就返回,影子命中一个字节都不写 `qy_violation_counter`。

**已知代价,不糊过去**:管理员因此失去了 O(1) 的「这个用户在影子期间已经攒了多少次」。
要得到这个数只能对 `qy_violation_record` 按 `(user_id, shadow = 1, created_at >= 窗口起点)`
做一次 `COUNT`,在这张增长最快的表上是一次范围扫描,比读计数器贵得多。
这是裁决 2 明确接受的代价 —— 另加一列「影子计数」本身就是在记录违规次数,
而它一旦存在,下一次改动就会有人把它接回封号判据。

#### 12.2.2 全局开关放在哪里(第六批新增)

| 层 | 位置 | 谁能改 | 用途 |
|---|---|---|---|
| 兜底默认 | YAML `violation.shadow_mode`(`*bool`,默认 `true`) | 改文件 + 重载 | 发布口径 |
| 运营覆盖 | `qy_settings` `scope='violation'`, `k='shadow_mode'`, `v='1'/'0'` | 管理端 `PUT /api/qy/admin/violation/mode`,**写审计** | 上线安全阀的日常开关 |
| 熔断回落 | 进程内 `forcedShadowUntil` | 自动触发;`POST /api/qy/admin/violation/breaker/reset` 解除 | 规则事故自愈 |

覆盖存在时**不再回落 YAML** —— 否则永远退不出影子模式,这正是需求原文
「违规规则无法调整模式」的根因:YAML 默认为真,而 `shadowActive()` 见到配置为真就
无条件返回 shadow,规则级 `dry_run` 无从覆盖;而当时唯一的写入口
`POST /api/qy/admin/config/reload` 只是重读磁盘上的 YAML。

覆盖值在热路径上不能查库,因此走的是 `rules.go` 的 `maybeRefresh` 同一形状:
进程内 atomic 快照 + 到期后经 `guard.HotAsync` 异步重载(周期 30 秒),
读端只做一次 atomic 比较。读库失败时沿用上一份快照并计数告警(回落 YAML 会让
扩展库抖一下就把已经确认过的真实模式静默退回「只记录」);值被手工改坏时强制影子。

**叠加语义**(`guard.effectiveShadow`,取更保守者胜):

```
effective_shadow = 全局影子(qy_settings 覆盖 / YAML / 熔断回落) OR rule.dry_run
```

全局开关是一票否决的总闸;规则级只能让单条规则更保守。
反过来,**全局关掉时绝不去动各条规则的 `dry_run`** —— 那样一次熔断自动恢复
就会把全部灰度规则一起转正。

#### 12.2.3 升级影响与历史数据

- **现存规则保持当前行为**。`dry_run` 列早已存在且默认 `false`,`qy_settings` 里
  也不会凭空多出覆盖行,因此升级后全局与规则级取值都与升级前一致。
  静默把生产风控降级成「只记录」是资损,所以这一条不做任何默认值变更;
  新建规则在表单上默认 `dry_run = true`,那只是前端默认值。
- **计数器里已经混有影子命中**。修复只能保证从此不再混入,历史行无法分辨。
  静默清库不可接受(会连真实违规的累计一起抹掉,且没有任何记录说明发生过),
  因此提供一个显式动作:管理端「违规规则」页的**违规计数器**卡片
  → `POST /api/qy/admin/violation/counters/:userId/reset`,**写审计**。
  它只清 `hit_count` 与窗口起点;`total_count`(终身累计展示值)与
  `ban_cycle`(封禁认领的互斥键,回退它会让该用户的自动封号永久静默失效)都不动。
  配套的 `GET /api/qy/admin/violation/counters` 列出计数并下发阈值,
  否则重置就是盲操作。

### 12.3 全局熔断阈值(防规则事故)
配置 `global_block_rate_limit`(默认 `0.05`)与 `global_ban_rate_limit`(默认 `20/hour`)。滑动窗口内:
- 拦截率超过 5% → 自动进入影子模式并告警(几乎可以肯定是规则写错了,比如正则漏了转义匹配到所有文本);
- 每小时封号数超过 20 → 暂停自动封号,只记录待审。

**这条比任何单个规则的正确性都重要。** 一个 `.*` 的正则能在 30 秒内封掉全站用户。

### 12.4 白名单与灰度
- `ban_exempt_roles` / `ban_exempt_users`(已在配置里);
- 规则级 `UserScope` 支持 `!group:vip`(排除 VIP 分组)与百分比灰度 `sample:10`(按 `user_id % 100 < 10` 生效)。

### 12.5 二次违规加重(建议)
配置 `escalation`:第 N 次违规的扣费倍数 = `base × escalation_factor^(n-1)`(默认 `escalation_factor: 1.0` 即不加重)。这比单纯计数封号更有梯度,也更符合「先警告后惩罚」的通行做法。

### 12.6 告警通道
- 封号时给用户发邮件(`ban_notify_email`,复用项目 `common.SendEmail`);
- 管理员告警:熔断打开、队列丢弃 > 0、单用户 1 分钟内命中 > 10 次 → 走项目已有的通知机制(与 `service.NotifyRootUser` 同路径);(原设计里的「命中 `severity=3` 的高危规则」这一条随 severity 一并作废 —— 严重程度现在由违规类型的阈值表达)
- `/health` 被管理端页面每 30s 轮询,异常时页面顶部红条。

### 12.7 审计完备性
所有管理端写操作(改规则、撤销记录、退款、解封、查看未脱敏原文)都要写 `model.RecordOperationAuditLog(...)`(`model/log.go:229`)。**尤其是「查看未脱敏原文」** —— 那是查看他人隐私数据的操作,必须留痕,且应对接项目的 `middleware.SecureVerificationRequired()`。

### 12.8 数据主体权利(合规,建议)
- 用户删除账号时,`qy_violation_payload` 应级联删除(或匿名化 `user_id = -1`);
- 提供管理端「按 user_id 清除全部归档上下文」的操作(GDPR 删除权);
- 配置注释里明确写出:归档内容包含用户输入的原始文本,属于个人数据,保留期与访问控制需符合当地法规。

### 12.9 错误文案(建议的具体措辞)

| 场景 | 文案 | 说明 |
|---|---|---|
| prompt 被拦截(默认) | `Your request was blocked by content policy. Request ID: <rid>` | 中文:`您的请求因违反内容策略被拒绝。请求 ID:<rid>` |
| 拦截 + 扣费 | 同上 + `A policy violation fee has been charged. See usage logs for details.` | 必须告知扣费,否则用户不知道钱去哪了 |
| 账号被自动封禁 | 复用项目 `i18n.MsgAuthUserBanned`(`middleware/auth.go:452`) | **不要自造文案**,让封禁提示与管理员手动封禁完全一致 |
| 申诉提交成功 | `申诉已提交,我们会在 3 个工作日内处理` | SLA 应可配置 |

**统一原则**:对外文案**永远不包含**命中词、规则名(内部名)、规则 id、阈值细节。`rule.BlockMessage` 字段在管理端保存时应做校验/提示,防止管理员把内部信息写进去。

### 12.10 未做的事(明确列出,避免误期待)
- **不做**语义模型 / 外部审核 API 接入(如 OpenAI Moderations)。规则模型预留了 `match_type` 扩展位,二期可加 `match_type: "moderation_api"`。
- **不做**流式响应的真实阻断(架构上受 `lastStreamData` 一帧延迟限制,收益 < 成本)。
- **不做**欠费追扣(`debt` 策略只落账不追扣)。追扣需要在充值路径挂 hook,属于需求 3 模块的领地。
- **不做**图片内容检测(仅存哈希)。哈希黑名单是二期最低成本的增强点。
