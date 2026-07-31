package config

// applyDefaults 把零值字段补成默认值。
//
// 只处理"零值即未配置"的数值与字符串字段;布尔开关的默认值由 *bool + boolOr
// 承载(见 config.go),因为 bool 的零值 false 无法与"用户显式写 false"区分。
func applyDefaults(c *Config) {
	d := &c.Database
	intDefault(&d.MaxIdleConns, 20)
	intDefault(&d.MaxOpenConns, 100)
	intDefault(&d.ConnMaxLifetimeSeconds, 600)
	intDefault(&d.ConnMaxIdleTimeSeconds, 120)
	intDefault(&d.ConnectTimeoutSeconds, 5)
	intDefault(&d.SlowThresholdMs, 200)
	strDefault(&d.LogLevel, "warn")

	r := &c.Runtime
	intDefault(&r.HotPathTimeoutMs, 200)
	intDefault(&r.ColdPathTimeoutMs, 3000)
	intDefault(&r.HealthIntervalSeconds, 15)
	intDefault(&r.BreakerFailureThreshold, 5)
	intDefault(&r.BreakerOpenSeconds, 30)
	intDefault(&r.LeaseTTLSeconds, 60)
	intDefault(&r.LeaseRenewSeconds, 20)
	intDefault(&r.HotHookQueueSize, 4096)
	intDefault(&r.HotHookWorkers, 2)

	t := &c.TwoPhase
	intDefault(&t.CompensateIntervalSeconds, 30)
	intDefault(&t.PendingGraceSeconds, 60)
	intDefault(&t.MaxProbeAttempts, 10)
	intDefault(&t.BatchSize, 200)
	intDefault(&t.ManualReviewAfterSeconds, 900)
	intDefault(&t.OutboxRetentionDays, 30)

	intDefault(&c.Audit.SnapshotMaxBytes, 4096)

	tr := &c.Transfer
	int64Default(&tr.MinQuota, 500000)
	int64Default(&tr.MaxPerTxQuota, 50000000)
	int64Default(&tr.DailyMaxQuota, 200000000)
	intDefault(&tr.DailyMaxCount, 20)
	intDefault(&tr.CooldownSecs, 10)
	strDefault(&tr.RecipientLookup, RecipientLookupID)
	intDefault(&tr.NewAccountFreezeHours, 24)
	intDefault(&tr.ReceiverDailyMaxInCount, 50)
	intDefault(&tr.LookupLogRetainDays, 30)

	cm := &c.Commission
	intDefault(&cm.TopupRateBps, 1000)
	intDefault(&cm.ConsumeRateBps, 500)
	intDefault(&cm.Levels, 1)
	int64Default(&cm.MinSettleQuota, 1000)
	int64Default(&cm.MaxPerOrderQuota, 50000000)
	intDefault(&cm.HoldingDays, 7)
	intDefault(&cm.SettleIntervalSecs, 300)
	intDefault(&cm.InviterCacheSecs, 300)
	intDefault(&cm.TopupScanIntervalSec, 60)
	intDefault(&cm.TopupScanLookbackHours, 72)

	w := &c.Withdraw
	if len(w.Methods) == 0 {
		w.Methods = []string{WithdrawMethodQuota, WithdrawMethodFiat}
	}
	int64Default(&w.MinQuota, 500000)
	strDefault(&w.MinFiatAmount, "100")
	strDefault(&w.FiatCurrency, "CNY")
	strDefault(&w.RateFreezeMode, RateFreezeOperationSetting)
	strDefault(&w.RateFreezeFixed, "7.3")
	intDefault(&w.DailyMaxCount, 3)
	intDefault(&w.PayeeAccountMax, 3)
	intDefault(&w.ReviewSLAHours, 72)
	intDefault(&w.RemarkMaxRunes, 200)
	intDefault(&w.PIIKeyVersion, 1)
	intDefault(&w.CooldownSecs, 60)
	intDefault(&w.MaxPendingOrders, 3)
	int64Default(&w.MaxQuotaPerOrder, 500000000)
	int64Default(&w.DailyMaxQuota, 1000000000)
	intDefault(&w.PIIRetentionDays, 180)

	av := &c.Availability
	intDefault(&av.BucketSeconds, 300)
	intDefault(&av.FlushIntervalSeconds, 60)
	intDefault(&av.RetentionDays, 30)
	intDefault(&av.MaxSeriesPerQuery, 200)

	v := &c.Violation
	strDefault(&v.FeeMultiplier, "1.0")
	strDefault(&v.FixedFeeAmount, "0.05")
	int64Default(&v.MaxFeeQuota, 5000000)
	strDefault(&v.InsufficientBalancePolicy, InsufficientClamp)
	intDefault(&v.AutoBanWindowHours, 24)
	intDefault(&v.GlobalBlockRateLimitBps, 500)
	intDefault(&v.GlobalBanRateLimitPerHour, 20)
	intDefault(&v.EvidenceMaxBytes, 8192)
	intDefault(&v.EvidenceRetentionDays, 90)
	intDefault(&v.RuleCacheSeconds, 60)
	intDefault(&v.ScanTimeoutMs, 20)
}

func intDefault(p *int, def int) {
	if *p == 0 {
		*p = def
	}
}

func int64Default(p *int64, def int64) {
	if *p == 0 {
		*p = def
	}
}

func strDefault(p *string, def string) {
	if *p == "" {
		*p = def
	}
}
