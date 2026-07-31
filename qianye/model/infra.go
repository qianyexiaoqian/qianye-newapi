package model

// TaskLease 是后台任务的分布式租约。
//
// 为什么不能只靠 common.IsMasterNode:它只是 NODE_TYPE != "slave" 这个环境变量,
// 不是租约。多个节点完全可能都被配成 master,佣金结算、充值扫描这类任务就会双跑,
// 造成重复返佣。
//
// 所有时间比较一律用 MySQL 的 UNIX_TIMESTAMP() 而非 Go 端时间,
// 以消除节点之间的时钟漂移。
type TaskLease struct {
	Name string `json:"name" gorm:"type:varchar(64);primaryKey"`
	// Holder = NodeName + ":" + 进程ID。只用 NodeName 不够,同机多实例会重名。
	Holder string `json:"holder" gorm:"type:varchar(200);not null;default:''"`
	// Fence 每次易主递增。老持有者若卡在 GC 或网络分区中,恢复后其 fence 已过期,
	// 续租与写入都会失败,不会双跑写脏数据。
	Fence      int64 `json:"fence" gorm:"not null;default:0"`
	LeaseUntil int64 `json:"lease_until" gorm:"not null;default:0;index"`
	AcquiredAt int64 `json:"acquired_at" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (TaskLease) TableName() string { return "qy_task_leases" }

// KV 存放轻量的运行期状态:充值扫描低水位、可用率汇总游标等。
//
// 各模块不要各建一张游标表 —— 那些表都只有一行,合成一张即可。
type KV struct {
	K         string `json:"k" gorm:"type:varchar(96);primaryKey"`
	V         string `json:"v" gorm:"type:text"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (KV) TableName() string { return "qy_kv" }

// Setting 是运营可在管理端修改的配置项。
//
// 与 YAML 的分工:YAML 承载启动级、涉及安全的配置(DSN、密钥、降级策略);
// 本表承载上线后需要频繁调整的运营参数(佣金费率、紧急熔断开关)。
//
// 刻意不复用上游的 setting/config.GlobalConfig —— 那套体系持久化到主库 options 表,
// 会违背"新功能数据必须进独立数据库"的核心约束。
type Setting struct {
	Scope string `json:"scope" gorm:"type:varchar(32);primaryKey"`
	K     string `json:"k" gorm:"type:varchar(64);primaryKey"`
	V     string `json:"v" gorm:"type:text"`
	// OperatorId 记录最后修改人。费率这类字段改动会直接影响资金,必须可追溯。
	OperatorId int   `json:"operator_id" gorm:"not null;default:0"`
	UpdatedAt  int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Setting) TableName() string { return "qy_settings" }
