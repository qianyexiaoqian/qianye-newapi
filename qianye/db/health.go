package db

import (
	"context"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/bytedance/gopkg/util/gopool"
)

var healthOnce sync.Once

// StartHealthLoop 起一个后台协程周期性探测扩展库,并在恢复后自动闭合熔断。
//
// 没有这个循环,熔断一旦打开就只能靠 openUntil 到期后的第一次真实请求去试探,
// 而热路径是 fail-open 的、根本不会发起请求,扩展会一直"假死"。
func StartHealthLoop() {
	healthOnce.Do(func() {
		gopool.Go(func() {
			interval := config.Get().Runtime.HealthIntervalSeconds
			if interval <= 0 {
				interval = 15
			}
			ticker := time.NewTicker(time.Duration(interval) * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				probe()
			}
		})
	})
}

func probe() {
	gdb := Get()
	if gdb == nil {
		return
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	if err := sqlDB.PingContext(ctx); err != nil {
		// 探测失败不直接打开熔断:那是 MarkFailure 依据真实业务错误做的判断。
		// 这里只标记不健康,避免探测网络抖动就把功能全关掉。
		healthy.Store(false)
		common.SysError("qianye: 扩展数据库健康探测失败: " + err.Error())
		return
	}
	lastPingMs.Store(time.Since(start).Milliseconds())
	lastPingAt.Store(common.GetTimestamp())

	if markProbeHealthy() {
		common.SysLog("qianye: 扩展数据库已恢复")
	}
}

// markProbeHealthy 在探测成功后收敛熔断状态,返回是否发生了"不健康 → 健康"的转变。
//
// 只有这一次转变才清零失败计数与熔断窗口。原先每 15 秒 Ping 成功就无条件
// failStreak.Store(0),而 Ping 走的是另一条连接、只验证 TCP 与握手 —— 扩展库处于
// "可达但查询慢"的状态时,业务侧每积累一次超时,下一次探测就把它抹掉,
// 连续失败数永远到不了阈值,熔断在唯一重要的场景下形同虚设(C4)。
func markProbeHealthy() bool {
	if healthy.Load() {
		return false
	}
	healthy.Store(true)
	failStreak.Store(0)
	openUntil.Store(0)
	return true
}
