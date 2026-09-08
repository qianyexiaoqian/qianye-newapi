// Package mall 实现星屑商城:只收星屑的商城,可上架套餐 / 兑换码 / 实物。
//
// 设计稿 qianye/docs/design-15-stardust.md §6。三种商品的履行方式不同,资金面却
// 只有一条:扣星屑走 stardust.Debit,退星屑走 stardust.Credit,二者都在扩展库
// 单事务里;只有 kind=plan 要动主库(发订阅),它走 twophase 两阶段,
// 并自带对账任务 + Resolver + PostCommit + root 裁决端点(D-K)。
package mall

import qymodel "github.com/QuantumNous/new-api/qianye/model"

// model.go —— 本模块在扩展库里的五张表。
//
// 全部遵守 qianye/model/tables.go 的约定:qy_ 前缀硬编码在 TableName 里、
// 时间戳 int64 unix 秒手工赋值、跨库无外键(user_id / plan_id 是主库软引用)、
// bool 列不写 default、二进制列用 qymodel.Blob、索引名全库唯一(idx_qy_ml* / uk_qy_ml*)。

// 商品种类。取值同时是 qy_ml_product.kind 与 qy_ml_order.kind 的字面量。
const (
	KindPlan     = "plan"
	KindCode     = "code"
	KindPhysical = "physical"
)

// 订单来源。mall = 用户在商城自己下的单(付了星屑);lottery = 抽奖中奖生成的单
// (价格 0、不扣星屑、由 lottery 模块经 GrantPrizeTx 写入,ref_no 指回出款号)。
//
// 做成一列而不是"price == 0 就是奖品":价格是快照,运营把某件商品临时改成 0 星屑
// 促销时,那些单也是 0 —— 而"抽奖所得"这个身份决定了地址补填入口、退款路径
// (奖品单没有钱可退)与用户列表上的标记,不能靠一个会撞的数字去猜。
const (
	SourceMall    = "mall"
	SourceLottery = "lottery"
)

// StockUnlimited 是 qy_ml_product.stock 上"不限库存"的取值。
// code 类商品的库存由 qy_ml_code_stock 的 unused 行数决定,这一列恒为 StockUnlimited。
const StockUnlimited = -1

// Product 是一件上架商品。
type Product struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// ProductNo 是对外唯一标识。绝不下发自增 id —— 它可枚举。
	ProductNo string `json:"product_no" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_mlp_no"`
	Kind      string `json:"kind" gorm:"type:varchar(16);not null;index:idx_qy_mlp_kind"`
	Title     string `json:"title" gorm:"type:varchar(128);not null"`
	// Description 是商品说明。text 而不是 varchar:它是给用户看的正文,长度由接口按 rune 卡。
	Description string `json:"description" gorm:"type:text"`
	// CoverRef 是上传封面的引用(qy_ml_cover.ref),空 = 无封面。
	CoverRef string `json:"cover_ref" gorm:"type:varchar(64);not null;default:''"`
	// Price 是售价(星屑),所有 kind 都要求 1 ≤ price ≤ common.MaxQuota:
	// 与 stardust.post 的金额校验及 twophase.validateAmount 同源。
	Price int64 `json:"price" gorm:"not null"`
	// Stock 是库存,StockUnlimited 表示不限。**刻意不写 default**:带默认值的列在
	// GORM 的 Create 里会跳过零值,一件库存为 0(售罄)的商品会被写成默认值。
	Stock int `json:"stock" gorm:"not null"`
	Sold  int `json:"sold" gorm:"not null;default:0"`
	// PerUserLimit 是每人限购件数,0 = 不限。
	PerUserLimit int   `json:"per_user_limit" gorm:"not null;default:0"`
	SaleStartAt  int64 `json:"sale_start_at" gorm:"not null;default:0"`
	SaleEndAt    int64 `json:"sale_end_at" gorm:"not null;default:0"`
	// Enabled 不写 default:MySQL 与 PostgreSQL 对布尔默认值的归一化不同,
	// 会让 AutoMigrate 每次启动都发 ALTER。
	Enabled   bool `json:"enabled" gorm:"not null"`
	SortOrder int  `json:"sort_order" gorm:"not null;default:0"`
	// PlanId 只对 kind=plan 有意义:主库 subscription_plans.id 的软引用。
	PlanId    int   `json:"plan_id" gorm:"not null;default:0"`
	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Product) TableName() string { return "qy_ml_product" }

// 兑换码库存的状态。
//
// CodeTaken 是管理员从库里**提走**的那一枚:明文已经交到人手上,平台不再持有
// 它的去向。做成独立一态而不是复用 issued,是因为 issued 的语义带着 order_id ——
// "这枚码发给了哪张单"是履行证据,而提卡没有单。也不能留在 unused:提卡后的码
// 已经离开平台,若还算进可售库存,同一枚码就会被再卖给一个用户。
const (
	CodeUnused  = "unused"
	CodeIssued  = "issued"
	CodeRevoked = "revoked"
	CodeTaken   = "taken"
)

// CodeStock 是预存的一枚兑换码。
//
// CodeCipher 是 AES-256-GCM 密文,明文只出现在上传请求体与揭示响应里;
// 三列只经 sealCode / openCode 触碰,由 secret_guard_test.go 的 AST 断言守住。
type CodeStock struct {
	Id         int64        `json:"id" gorm:"primaryKey;autoIncrement"`
	ProductId  int64        `json:"product_id" gorm:"not null;index:idx_qy_mlc_product,priority:1"`
	CodeCipher qymodel.Blob `json:"-"`
	CodeNonce  qymodel.Blob `json:"-"`
	// KeyVersion 支持密钥轮换:解密时按行上的版本选密钥。
	KeyVersion int    `json:"-" gorm:"not null;default:0"`
	Status     string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_mlc_product,priority:2;index:idx_qy_mlc_status"`
	// OrderId 是发给了哪张订单(issued / revoked 时非 0)。
	OrderId   int64 `json:"order_id" gorm:"not null;default:0;index:idx_qy_mlc_order"`
	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	IssuedAt  int64 `json:"issued_at" gorm:"not null;default:0"`
	// TakenAt / TakenBy 记管理员提卡(status=taken):什么时候、谁提走的。
	//
	// 审计表里也有这一条,但审计是按时间排的流水,回答不了"这一整页码里哪几枚被
	// 提走了、分别是谁"—— 而运营对着库存表问的恰恰是这个。两处都留是刻意的:
	// 审计可被保留期清理,库存行不会。
	TakenAt int64 `json:"taken_at" gorm:"not null;default:0"`
	TakenBy int   `json:"taken_by" gorm:"not null;default:0"`
}

func (CodeStock) TableName() string { return "qy_ml_code_stock" }

// Order 是一张订单。三种 kind 共用一张表,状态机见 order.go。
type Order struct {
	Id      int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	OrderNo string `json:"order_no" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_mlo_no"`
	UserId  int    `json:"user_id" gorm:"not null;index:idx_qy_mlo_user,priority:1"`

	ProductId int64 `json:"product_id" gorm:"not null;index:idx_qy_mlo_product"`
	// ProductNo 是下单时的商品号快照:幂等重放要比"重放的是不是同一件商品",
	// 而商品行可以在订单之后被删除 —— 比对不能依赖它还在。
	ProductNo string `json:"product_no" gorm:"type:varchar(32);not null;default:''"`
	Kind      string `json:"kind" gorm:"type:varchar(16);not null"`
	// Title / Price 是下单那一刻的快照:运营改价改名不追溯已成交的单。
	Title  string `json:"title" gorm:"type:varchar(128);not null;default:''"`
	Price  int64  `json:"price" gorm:"not null"`
	Status string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_mlo_status"`
	// Source 是订单来源(SourceMall / SourceLottery)。刻意不写 default:GORM 的 Create
	// 会跳过零值列,一个没显式赋值的来源应当在代码里就被发现,而不是被库端默认成
	// "商城自购"。RefNo 只对 SourceLottery 有值:lottery 的出款号(qy_lot_payout.payout_no),
	// 争议时"这张单对应哪一次中奖"只有它能回答。
	Source string `json:"source" gorm:"type:varchar(16);not null;index:idx_qy_mlo_source"`
	RefNo  string `json:"ref_no" gorm:"type:varchar(64);not null;default:''"`

	// IdemKey = "<user_id>:<NormalizeIdemClientKey(crid)>"。三种 kind 共用一个键空间,
	// 重放判定因此只查这一列;kind=plan 时资金单的 IdemKey 与它同源,让两边的撞键
	// 指向同一张原单。奖品单的键是 "lotprize:<payout_no>"(不带 user 前缀:出款号
	// 由 crypto/rand 生成、全局唯一,而且它不是用户能自选的输入)。
	IdemKey string `json:"-" gorm:"type:varchar(96);not null;uniqueIndex:uk_qy_mlo_idem"`
	// LedgerNo / RefundLedgerNo 是扣款与退款的星屑流水号。
	LedgerNo       string `json:"ledger_no" gorm:"type:varchar(32);not null;default:''"`
	RefundLedgerNo string `json:"refund_ledger_no" gorm:"type:varchar(32);not null;default:''"`

	// FundOrderNo 只对 kind=plan 有值:qy_fund_orders.order_no(列宽与那一侧一致)。
	FundOrderNo string `json:"fund_order_no" gorm:"type:varchar(64);not null;default:'';index:idx_qy_mlo_fund"`
	// TradeNo = "SUBSD" + order_no,写进主库 subscription_orders.trade_no,
	// Resolver 按它回读 provider_payload 回填订阅 id。
	TradeNo            string `json:"trade_no" gorm:"type:varchar(64);not null;default:''"`
	UserSubscriptionId int    `json:"user_subscription_id" gorm:"not null;default:0"`
	SubRenewed         bool   `json:"sub_renewed" gorm:"not null"`
	// ExpectAction / ExpectSuperseded 是用户在下单前确认过的顶替后果(§6.3),
	// MainApply 在主库事务里复核,不一致即拒绝。
	ExpectAction     string `json:"expect_action" gorm:"type:varchar(16);not null;default:''"`
	ExpectSuperseded string `json:"expect_superseded" gorm:"type:varchar(255);not null;default:''"`

	// CodeStockId 只对 kind=code 有值:发出去的那一枚码。
	CodeStockId int64 `json:"code_stock_id" gorm:"not null;default:0"`

	// 收货地址与联系方式的 AES-256-GCM 密文,只经 sealAddress / openAddress 触碰,
	// 到期由 mall.prune 清空(AddressPrunedAt 记时刻)。
	AddressCipher     qymodel.Blob `json:"-"`
	AddressNonce      qymodel.Blob `json:"-"`
	ContactCipher     qymodel.Blob `json:"-"`
	ContactNonce      qymodel.Blob `json:"-"`
	AddressKeyVersion int          `json:"-" gorm:"not null;default:0"`
	// AddressSetAt 是地址密文写入的时刻(0 = 还没有地址)。它是密文列之外唯一能回答
	// "这张单有没有地址"的列:奖品单(source=lottery)在中奖那一刻没有地址,要中奖者
	// 事后补填,列表上得先看得见"还没填";而密文列本身受 secret_guard 约束,
	// 视图层不许碰。由 sealAddress 一并写入。
	AddressSetAt int64 `json:"address_set_at" gorm:"not null;default:0"`

	TrackingNo string `json:"tracking_no" gorm:"type:varchar(128);not null;default:''"`
	ShipNote   string `json:"ship_note" gorm:"type:varchar(255);not null;default:''"`
	FailReason string `json:"fail_reason" gorm:"type:varchar(255);not null;default:''"`

	CreatedAt       int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_mlo_user,priority:2"`
	FulfilledAt     int64 `json:"fulfilled_at" gorm:"not null;default:0"`
	UpdatedAt       int64 `json:"updated_at" gorm:"not null;default:0"`
	AddressPrunedAt int64 `json:"address_pruned_at" gorm:"not null;default:0"`
}

func (Order) TableName() string { return "qy_ml_order" }

// OrderEvent 是订单时间线(withdraw 的 Event 形状):只增不改。
type OrderEvent struct {
	Id      int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	OrderId int64  `json:"-" gorm:"not null;index:idx_qy_mle_order"`
	Action  string `json:"action" gorm:"type:varchar(32);not null"`
	Note    string `json:"note" gorm:"type:varchar(255);not null;default:''"`
	// ActorId 是操作人;系统 0。刻意不下发:普通用户不该能枚举管理员账号。
	ActorId   int   `json:"-" gorm:"not null;default:0"`
	CreatedAt int64 `json:"at" gorm:"not null;default:0"`
}

func (OrderEvent) TableName() string { return "qy_ml_order_event" }

// Cover 是商品封面的元数据,图片本体落盘(qianye/service/imagestore,目录 qy-mall-covers)。
// 形状照 lottery 的 Cover:ProductId=0 表示传了还没绑到商品,DetachedAt 表示被换下。
type Cover struct {
	Id         int64  `json:"-" gorm:"primaryKey;autoIncrement"`
	Ref        string `json:"ref" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_mlv_ref"`
	UserId     int    `json:"-" gorm:"not null;index:idx_qy_mlv_user,priority:1"`
	ProductId  int64  `json:"-" gorm:"not null;default:0;index:idx_qy_mlv_product"`
	StoredName string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	MimeType   string `json:"mime_type" gorm:"type:varchar(32);not null;default:''"`
	Size       int64  `json:"size" gorm:"not null;default:0"`
	Sha256     string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;default:0;index:idx_qy_mlv_user,priority:2"`
	BoundAt    int64  `json:"-" gorm:"not null;default:0"`
	DetachedAt int64  `json:"-" gorm:"not null;default:0;index:idx_qy_mlv_purge,priority:1"`
	PurgedAt   int64  `json:"-" gorm:"not null;default:0;index:idx_qy_mlv_purge,priority:2"`
}

func (Cover) TableName() string { return "qy_ml_cover" }

// Tables 是本模块交给 AutoMigrate 的全部模型。
//
// Order / OrderEvent / CodeStock 是证据表:订单与发码记录永不随商品删除而删除
// (删商品只清 unused 码),争议时"这个人拿到的是哪一枚码"只有它们能回答。
func Tables() []any {
	return []any{&Product{}, &CodeStock{}, &Order{}, &OrderEvent{}, &Cover{}}
}
