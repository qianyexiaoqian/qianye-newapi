package dto

// UserSetting 是持久化在 users.setting(JSON)里的每用户设置。
//
// 注意 BillingPreference：**已废弃，任何代码都不再读它**。扣费顺序写死为
// 「套餐有余额且本次用得上 → 扣套餐，否则 → 扣钱包」。
//
// 字段刻意保留而不是删除，两条理由：
//
//  1. controller/user.go 的侧边栏与语言两条保存路径是 read-modify-write
//     （user.GetSetting() → 改一个字段 → UpdateUserSetting 写回整个结构体）。
//     删掉字段，这两条路径就会在反序列化时丢掉 billing_preference，再序列化时
//     把它从存量用户的 setting 里静默抹掉，回滚无从恢复原值。
//  2. relaykit 是必须独立可构建的模块，删它的公开字段属于该模块的 API 破坏。
//
// 注意第 3 条**不**成立：通知设置那条保存路径（controller/user.go 的
// UpdateUserSetting handler）本来就是从零重建整个结构体、只回填
// UpstreamModelUpdateNotifyEnabled 一项，因此它今天就已经在抹掉
// billing_preference —— 连同 sidebar_modules 与 language 一起，与本字段存不存在
// 无关。那是一条独立的既有缺陷，不要拿它给这个字段的去留当论据。
//
// 要清理这个 JSON 键请单独跑一次幂等迁移，别顺手删字段。
type UserSetting struct {
	NotifyType                       string  `json:"notify_type,omitempty"`                          // QuotaWarningType 额度预警类型
	QuotaWarningThreshold            float64 `json:"quota_warning_threshold,omitempty"`              // QuotaWarningThreshold 额度预警阈值
	WebhookUrl                       string  `json:"webhook_url,omitempty"`                          // WebhookUrl webhook地址
	WebhookSecret                    string  `json:"webhook_secret,omitempty"`                       // WebhookSecret webhook密钥
	NotificationEmail                string  `json:"notification_email,omitempty"`                   // NotificationEmail 通知邮箱地址
	BarkUrl                          string  `json:"bark_url,omitempty"`                             // BarkUrl Bark推送URL
	GotifyUrl                        string  `json:"gotify_url,omitempty"`                           // GotifyUrl Gotify服务器地址
	GotifyToken                      string  `json:"gotify_token,omitempty"`                         // GotifyToken Gotify应用令牌
	GotifyPriority                   int     `json:"gotify_priority"`                                // GotifyPriority Gotify消息优先级
	UpstreamModelUpdateNotifyEnabled bool    `json:"upstream_model_update_notify_enabled,omitempty"` // 是否接收上游模型更新定时检测通知（仅管理员）
	AcceptUnsetRatioModel            bool    `json:"accept_unset_model_ratio_model,omitempty"`       // AcceptUnsetRatioModel 是否接受未设置价格的模型
	RecordIpLog                      bool    `json:"record_ip_log,omitempty"`                        // 是否记录请求和错误日志IP
	SidebarModules                   string  `json:"sidebar_modules,omitempty"`                      // SidebarModules 左侧边栏模块配置
	BillingPreference                string  `json:"billing_preference,omitempty"`                   // Deprecated: 已废弃，不再被任何代码读取；保留只为不抹掉存量用户的这个键
	Language                         string  `json:"language,omitempty"`                             // Language 用户语言偏好 (zh, en)
}

var (
	NotifyTypeEmail   = "email"   // Email 邮件
	NotifyTypeWebhook = "webhook" // Webhook
	NotifyTypeBark    = "bark"    // Bark 推送
	NotifyTypeGotify  = "gotify"  // Gotify 推送
)
