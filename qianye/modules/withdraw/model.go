// Package withdraw 实现佣金提现:申请、审核、发放登记与历史。
//
// # 产品口径:提现只做佣金扣除,金额由管理员手动发放
//
// 本模块**不会自己动任何一分钱**。用户提交申请时佣金立刻离开可用池,审核通过后
// 单据进入「待发放」队列;管理员照着单据上的金额自己去加站内额度或线下打款,
// 回来在单据上「标记已发放」,系统只登记凭证与操作者,不碰主库 users.quota。
//
// 由此而来的两条铁律,违反任何一条都会造成资损:
//
//  1. 佣金在【申请那一刻】就必须离开可用池(commission.FreezeForWithdraw)。
//     不这么做的话,同一笔佣金可以在审核队列里被无限次重复申请;而管理员是照着
//     单据发钱的,重复的单就是重复的钱。申请即冻结把这个风险前移消解 ——
//     最坏情况只是佣金卡在 frozen 等人处理,永远不会变成超发。
//
//  2. 「该发多少」只能有一个来源:佣金账本。法币金额取自冻结时从
//     qy_commission_balance.available_fiat 削走的那个绝对值
//     (commission.QuoteWithdrawFiat 与 FreezeForWithdraw 共用 fiatTakenBy,
//     同一把行锁同一事务)。改成人工发放**不会**放松这条 —— 管理员照着一个错
//     数字打款,和系统自动打错款,损失完全一样,区别只在于前者还多赖一个人。
package withdraw

import "github.com/shopspring/decimal"

// Withdrawal 是提现单的业务明细,也是管理员的发放待办。
//
// 本表是提现的唯一状态载体。这里曾经有一个 OrderNo 软关联到 qy_fund_orders
// (跨库两阶段的资金单),随自动到账链路一并下线:人工发放没有任何跨库副作用,
// 也就没有"主库到底动没动"这个问题需要资金单去回答。
type Withdrawal struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// WithdrawNo 是面向用户与客服的业务单号,同时充当佣金冻结/解冻/核销的 refNo。
	// 三个动作共用同一个 refNo,佣金账户的每一次变动才能追回到具体哪张提现单。
	WithdrawNo string `json:"withdraw_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_wd_no"`
	// IdemScope + IdemKey = ("withdraw", "<userId>:<client_request_id>")(裁定 C10/C11)。
	// 双击、多标签、客户端超时重试、负载均衡重放全靠这个唯一索引归并成一单;
	// 进程内锁和限流都只是辅助,唯一索引才是正确性依据。
	IdemScope string `json:"-" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_wd_idem,priority:1"`
	IdemKey   string `json:"-" gorm:"type:varchar(96);not null;uniqueIndex:uk_qy_wd_idem,priority:2"`

	UserId int `json:"user_id" gorm:"not null;index:idx_qy_wd_user,priority:1"`
	// Username 是快照。用户可改名可注销,管理端队列不能靠实时 join 主库。
	Username string `json:"username" gorm:"type:varchar(64);not null;default:''"`

	Method string `json:"method" gorm:"type:varchar(16);not null;index:idx_qy_wd_status,priority:2"`
	Status string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_wd_status,priority:1;index:idx_qy_wd_updated,priority:1"`

	// Quota 是从佣金余额扣走的额度,也是 quota 方式下写进主库 users.quota 的值。
	// 用 int64 承载,跨库前由 twophase 强制校验不超过 common.MaxQuota。
	Quota int64 `json:"quota" gorm:"not null;default:0"`

	// ── 法币侧(method=fiat),全部在申请时冻结 ──────────────────────────
	//
	// common.QuotaPerUnit 与 operation_setting.USDExchangeRate 都是管理员可随时
	// 热改的全局变量。不冻结的话,一年后没有任何办法复现"当时这笔佣金折合多少钱",
	// 历史对账永远对不上。两个都要冻:只冻汇率仍会被 QuotaPerUnit 的改动破坏。
	Currency           string          `json:"currency" gorm:"type:varchar(8);not null;default:''"`
	FrozenQuotaPerUnit decimal.Decimal `json:"frozen_quota_per_unit" gorm:"type:decimal(18,6);not null;default:0.000000"`
	FrozenFxRate       decimal.Decimal `json:"frozen_fx_rate" gorm:"type:decimal(18,8);not null;default:0.00000000"`
	// 应付 / 手续费 / 实付三段拆开存,展示层再 Round(2)(裁定 C26)。
	GrossAmount decimal.Decimal `json:"gross_amount" gorm:"type:decimal(18,6);not null;default:0.000000"`
	FeeAmount   decimal.Decimal `json:"fee_amount" gorm:"type:decimal(18,6);not null;default:0.000000"`
	NetAmount   decimal.Decimal `json:"net_amount" gorm:"type:decimal(18,6);not null;default:0.000000"`
	// FeeBps 是本单适用的费率快照(裁定 C28,比例一律用整数万分比)。
	// 费率改了不影响历史单据的可解释性。
	FeeBps int `json:"fee_bps" gorm:"not null;default:0"`

	// ── 收款信息的脱敏投影,明文在 qy_withdrawal_payees ─────────────────
	PayeeChannel string `json:"payee_channel" gorm:"type:varchar(24);not null;default:''"`
	PayeeMasked  string `json:"payee_masked" gorm:"type:varchar(128);not null;default:''"`
	// PayeeDigest 用独立且不轮换的 digest_key 生成(裁定 R8)。
	// 索引建在指纹上而不是明文上:同一收款账号被多个 user_id 使用即刷单信号,
	// 且 PII 清除之后这条风控线索仍然有效。
	PayeeDigest string `json:"-" gorm:"type:char(64);not null;default:'';index:idx_qy_wd_digest"`
	RiskFlags   string `json:"risk_flags" gorm:"type:varchar(255);not null;default:''"`

	// Remark 是用户自定义说明,按 rune 计数限长(withdraw.remark_max_runes)。
	// MySQL 的 varchar(N) 按字符计,与 Go 的 rune 口径一致,emoji 都算 1。
	Remark string `json:"remark" gorm:"type:varchar(2000);not null;default:''"`

	// HasProof 表示本单附了一张凭证图片(文件在磁盘上,元数据在 qy_withdrawal_proofs)。
	//
	// 这是一份刻意的冗余,但它只有【一个】写入点:submitInTx 里与 bindProof 同一个
	// 事务,而 bindProof 的 CAS 失败会让整个事务回滚。因此不存在
	// "HasProof=true 却没有凭证"的中间态 —— 那正是本项目反复栽的"第 N 份拷贝漂移"。
	// 不冗余的话,列表页每渲染一行就要多一次 qy_withdrawal_proofs 查询。
	//
	// 保留期到了图片被删除,本字段仍为 true:它回答的是"当时传没传",
	// 不是"现在还能不能下载"。下载接口会以 qy_wd_proof_purged 明确作答。
	HasProof bool `json:"has_proof" gorm:"not null"`

	// ── 审核:回答"什么时候拒绝的、拒绝理由是什么" ──────────────────────
	ReviewerId   int    `json:"reviewer_id" gorm:"not null;default:0"`
	ReviewerName string `json:"reviewer_name" gorm:"type:varchar(64);not null;default:''"`
	ReviewedAt   int64  `json:"reviewed_at" gorm:"not null;default:0"`
	RejectReason string `json:"reject_reason" gorm:"type:varchar(512);not null;default:''"`

	// ── 发放登记:回答"什么时候发的、谁发的、凭什么说发过了" ──────────────
	//
	// 这一组是人工发放模型下**唯一**的"钱已经给了"的证据。系统不动钱,所以它
	// 无法自证;能自证的只有"哪个管理员在什么时候声明发过、凭证是什么"。
	PayoutOperatorId   int    `json:"payout_operator_id" gorm:"not null;default:0"`
	PayoutOperatorName string `json:"payout_operator_name" gorm:"type:varchar(64);not null;default:''"`
	PaidAt             int64  `json:"paid_at" gorm:"not null;default:0;index:idx_qy_wd_paid"`
	// PayoutRef 是发放凭证,必填。
	// fiat 单存银行流水号 / 支付宝订单号 / 链上 txid;
	// quota 单存管理员那次手工加额度的操作记录标识(主库日志 id / 工单号)。
	// 没有它,一张 paid 单在争议时和一次误点击完全同形。
	PayoutRef  string `json:"payout_ref" gorm:"type:varchar(128);not null;default:''"`
	PayoutNote string `json:"payout_note" gorm:"type:varchar(255);not null;default:''"`
	FailReason string `json:"fail_reason" gorm:"type:varchar(512);not null;default:''"`

	ClientIp  string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;index:idx_qy_wd_user,priority:2"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;index:idx_qy_wd_updated,priority:2"`
}

func (Withdrawal) TableName() string { return "qy_withdrawals" }

// Payee 是提现单上收款信息的密文快照。
//
// 独立成表的唯一理由:保留期到了要能整行清空明文,而不动财务单据本体。
// 单据永久保留(法务/税务需要),解密能力可以按期销毁。
type Payee struct {
	Id int64 `json:"-" gorm:"primaryKey;autoIncrement"`
	// 一张提现单只能有一份收款信息,唯一索引兜住重复写入。
	WithdrawalId int64  `json:"-" gorm:"not null;uniqueIndex:uk_qy_wdp_wid"`
	WithdrawNo   string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	UserId       int    `json:"-" gorm:"not null;index:idx_qy_wdp_user"`

	Channel   string `json:"-" gorm:"type:varchar(24);not null"`
	CipherAlg string `json:"-" gorm:"type:varchar(24);not null;default:'aes-256-gcm'"`
	// KeyVersion 支持密钥轮换:解密时按版本选密钥,旧密文不会因为换钥而全部作废。
	KeyVersion int    `json:"-" gorm:"not null;default:1"`
	Nonce      []byte `json:"-" gorm:"type:varbinary(16);not null"`
	// Cipher 在保留期到期后置空,Masked 与 Digest 保留 —— 风控与对账仍然可用。
	Cipher []byte `json:"-" gorm:"type:varbinary(4096)"`
	Digest string `json:"-" gorm:"type:char(64);not null;default:'';index:idx_qy_wdp_digest"`
	Masked string `json:"-" gorm:"type:varchar(128);not null;default:''"`

	CreatedAt int64 `json:"-" gorm:"not null;default:0"`
	PurgedAt  int64 `json:"-" gorm:"not null;default:0"`
}

func (Payee) TableName() string { return "qy_withdrawal_payees" }

// PayeeAccount 是用户保存的收款方式,供下次申请直接复用。
//
// 与 Payee 的分工:本表是"用户当前的收款方式",可增删;Payee 是"这张单当时用的
// 收款方式",永不随本表变化 —— 否则用户改一次收款账号,历史打款记录就全变了。
type PayeeAccount struct {
	Id int64 `json:"-" gorm:"primaryKey;autoIncrement"`
	// Ref 是对外唯一标识。绝不下发自增 id:那等于把主键空间暴露给客户端,
	// 也让越权猜测变得廉价。
	Ref    string `json:"ref" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_wda_ref"`
	UserId int    `json:"-" gorm:"not null;index:idx_qy_wda_user"`

	Channel    string `json:"channel" gorm:"type:varchar(24);not null"`
	Label      string `json:"label" gorm:"type:varchar(64);not null;default:''"`
	CipherAlg  string `json:"-" gorm:"type:varchar(24);not null;default:'aes-256-gcm'"`
	KeyVersion int    `json:"-" gorm:"not null;default:1"`
	Nonce      []byte `json:"-" gorm:"type:varbinary(16);not null"`
	// Cipher 与 Payee.Cipher 装的是同一份银行卡号,因此也必须受同一个保留期约束:
	// 用户删掉收款方式之后,这里的密文到期同样要清空(见 prunePayeeAccounts)。
	Cipher []byte `json:"-" gorm:"type:varbinary(4096)"`
	Digest string `json:"-" gorm:"type:char(64);not null;default:'';index:idx_qy_wda_digest"`
	Masked string `json:"masked" gorm:"type:varchar(128);not null;default:''"`

	// DeletedAt 是软删除标记(0 = 未删除)。不用 gorm.DeletedAt:
	// 本项目统一手工写 unix 秒时间戳,混用两套时间语义会让人读不懂。
	DeletedAt int64 `json:"-" gorm:"not null;default:0"`
	// PurgedAt 是保留期清理的时间戳(0 = 未清理)。与 Payee 同口径:只把 Cipher
	// 置空,Masked/Digest 留着 —— 风控索引与事后追查不该跟着解密能力一起作废。
	//
	// 独立于 DeletedAt 存在,因为两者回答的是不同问题:DeletedAt 是"用户还要不要
	// 用它",PurgedAt 是"密文还在不在"。合并成一个字段就没法再区分"删了但还没到期"
	// 与"删了且已清理",清理任务会反复扫到同一批行。
	PurgedAt  int64 `json:"-" gorm:"not null;default:0"`
	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"-" gorm:"not null;default:0"`
}

func (PayeeAccount) TableName() string { return "qy_withdrawal_payee_accounts" }

// Proof 是提现凭证图片的元数据。图片本体在【本地磁盘】上,本表只存指针。
//
// 为什么不入库(裁决 3):本仓 violation/evidence.go 已经就"二进制入库"算过一次
// 体积账并明确反对 —— 一张 2 MiB 的手机截图进 MySQL,等于让每一次 SELECT *、
// 每一次 mysqldump、每一次主从复制都拖着它走。图片是只在争议时被看一眼的冷数据,
// 放数据库里付的是全链路的热成本。
//
// 已知限制:多节点部署时本地磁盘各存各的,A 节点收到的上传 B 节点下载不到。
// 单节点部署无碍;多节点需要共享存储或后续接对象存储。这条写在配置注释里。
type Proof struct {
	Id int64 `json:"-" gorm:"primaryKey;autoIncrement"`
	// Ref 是对外唯一标识,与 PayeeAccount.Ref 同口径:绝不下发自增 id。
	Ref    string `json:"ref" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_wdf_ref"`
	UserId int    `json:"-" gorm:"not null;index:idx_qy_wdf_user,priority:1"`

	// WithdrawalId = 0 表示"上传了但还没提交单据"。孤儿清理正是扫这个条件 ——
	// 用户传完图关掉页面是常态,不清理就是一个只增不减的磁盘泄漏。
	WithdrawalId int64  `json:"-" gorm:"not null;default:0;index:idx_qy_wdf_wid"`
	WithdrawNo   string `json:"-" gorm:"type:varchar(64);not null;default:''"`

	// StoredName 是【服务端生成】的落盘文件名(32 位十六进制 + 白名单扩展名)。
	// 用户提供的文件名从不落盘、也从不拼进路径:那是路径穿越最短的一条路。
	// 用户原始文件名连存都不存 —— 它本身就可能是 PII(比如"张三身份证.jpg")。
	StoredName string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// MimeType 由【魔数】判定,不信任扩展名,也不信任 Content-Type 请求头。
	MimeType string `json:"mime_type" gorm:"type:varchar(32);not null;default:''"`
	Size     int64  `json:"size" gorm:"not null;default:0"`
	// Sha256 用于事后自证"下载到的与当初上传的是同一张图",也顺带能发现重复上传。
	Sha256 string `json:"-" gorm:"type:char(64);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_wdf_user,priority:2"`
	BoundAt   int64 `json:"-" gorm:"not null;default:0"`
	// PurgedAt 是文件已从磁盘删除的时间戳(0 = 文件还在)。与 Payee.PurgedAt 同口径:
	// 元数据行留着(它是"这张单当时附过凭证"的证据),可读取的内容销毁。
	PurgedAt int64 `json:"-" gorm:"not null;default:0;index:idx_qy_wdf_purge,priority:1"`
}

func (Proof) TableName() string { return "qy_withdrawal_proofs" }

// Event 是提现单的状态机流水,也是前端时间线的唯一数据源。
//
// 为什么不把时间线塞进主表的 JSON 字段:单据主表要能被 SQL 聚合统计,事件要能
// 按时间倒序分页;且事件是 append-only,永不 UPDATE,天然免并发。主表上的
// ReviewedAt/PaidAt/RejectReason 是事件的冗余投影,只为让列表页免 join。
type Event struct {
	Id           int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	WithdrawalId int64  `json:"-" gorm:"not null;index:idx_qy_wde_wid,priority:1"`
	WithdrawNo   string `json:"withdraw_no" gorm:"type:varchar(64);not null;default:''"`

	FromStatus string `json:"from_status" gorm:"type:varchar(16);not null;default:''"`
	ToStatus   string `json:"to_status" gorm:"type:varchar(16);not null"`
	Action     string `json:"action" gorm:"type:varchar(32);not null"`

	ActorType string `json:"actor_type" gorm:"type:varchar(16);not null"`
	ActorId   int    `json:"-" gorm:"not null;default:0"`
	// ActorName 对普通用户降级为"管理员",否则用户可以枚举出全部管理员账号。
	ActorName string `json:"actor_name" gorm:"type:varchar(64);not null;default:''"`

	Reason string `json:"reason" gorm:"type:varchar(512);not null;default:''"`
	// Detail 是 JSON(金额/汇率/单号/错误码),仅管理端下发。
	Detail string `json:"-" gorm:"type:varchar(1024);not null;default:''"`
	Ip     string `json:"-" gorm:"type:varchar(64);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_wde_wid,priority:2"`
}

func (Event) TableName() string { return "qy_withdrawal_events" }

// PiiAudit 记录每一次收款信息明文访问。
//
// 与 qy_audit_logs 分表而不是合并:合规检查要能把"谁在什么时候看了谁的银行卡"
// 单独导出并单独设保留期,混在通用审计里会连带导出大量无关记录。
type PiiAudit struct {
	Id           int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Resource     string `json:"resource" gorm:"type:varchar(32);not null"`
	ResourceId   int64  `json:"resource_id" gorm:"not null;index:idx_qy_pii_res"`
	TargetUserId int    `json:"target_user_id" gorm:"not null;default:0;index:idx_qy_pii_target"`

	AdminId   int    `json:"admin_id" gorm:"not null;index:idx_qy_pii_admin,priority:1"`
	AdminName string `json:"admin_name" gorm:"type:varchar(64);not null;default:''"`
	Action    string `json:"action" gorm:"type:varchar(24);not null"`
	Fields    string `json:"fields" gorm:"type:varchar(255);not null;default:''"`
	// Reason 强制填写:没有事由的明文访问无法在事后区分"正常核对"与"顺手看看"。
	Reason string `json:"reason" gorm:"type:varchar(255);not null;default:''"`

	Ip        string `json:"ip" gorm:"type:varchar(64);not null;default:''"`
	UserAgent string `json:"-" gorm:"type:varchar(255);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;index:idx_qy_pii_admin,priority:2"`
}

func (PiiAudit) TableName() string { return "qy_pii_audits" }
