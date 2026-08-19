package model

import (
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

const (
	BatchUpdateTypeUserQuota = iota
	BatchUpdateTypeTokenQuota
	BatchUpdateTypeUsedQuota
	BatchUpdateTypeChannelUsedQuota
	BatchUpdateTypeRequestCount
	BatchUpdateTypeCount // if you add a new type, you need to add a new map and a new lock
)

var batchUpdateStores []map[int]int
var batchUpdateLocks []sync.Mutex

func init() {
	for i := 0; i < BatchUpdateTypeCount; i++ {
		batchUpdateStores = append(batchUpdateStores, make(map[int]int))
		batchUpdateLocks = append(batchUpdateLocks, sync.Mutex{})
	}
}

func InitBatchUpdater() {
	gopool.Go(func() {
		for {
			time.Sleep(time.Duration(common.BatchUpdateInterval) * time.Second)
			batchUpdate()
		}
	})
}

// FlushBatchUpdates 把内存队列里还没落库的额度变动立刻写进数据库。
//
// BATCH_UPDATE_ENABLED=true 时,用户额度增减、令牌额度增减、used_quota、
// request_count 全部只落在进程内存的 map 里,由 InitBatchUpdater 起的 goroutine
// 每 BATCH_UPDATE_INTERVAL 秒(默认 5)刷一次。此前优雅退出只调
// SaveQuotaDataCache()(那是看板缓存,与额度队列无关),于是 SIGTERM/崩溃时最后
// 一个窗口内的**全站**扣费与退款被静默丢弃,一行日志都没有,而消费日志照写不误
// —— logs 记的钱与用户实际被扣的钱从此对不上。
//
// 幂等:队列在 batchUpdate() 里被整体取走再落库,重复调用只是多跑一次空转。
func FlushBatchUpdates() {
	if !common.BatchUpdateEnabled {
		return
	}
	batchUpdate()
}

func addNewRecord(type_ int, id int, value int) {
	batchUpdateLocks[type_].Lock()
	defer batchUpdateLocks[type_].Unlock()
	if _, ok := batchUpdateStores[type_][id]; !ok {
		batchUpdateStores[type_][id] = value
	} else {
		batchUpdateStores[type_][id] += value
	}
}

func batchUpdate() {
	// check if there's any data to update
	hasData := false
	for i := 0; i < BatchUpdateTypeCount; i++ {
		batchUpdateLocks[i].Lock()
		if len(batchUpdateStores[i]) > 0 {
			hasData = true
			batchUpdateLocks[i].Unlock()
			break
		}
		batchUpdateLocks[i].Unlock()
	}

	if !hasData {
		return
	}

	common.SysLog("batch update started")
	stores := make([]map[int]int, BatchUpdateTypeCount)
	for i := 0; i < BatchUpdateTypeCount; i++ {
		batchUpdateLocks[i].Lock()
		stores[i] = batchUpdateStores[i]
		batchUpdateStores[i] = make(map[int]int)
		batchUpdateLocks[i].Unlock()
	}

	for i, store := range stores {
		if i == BatchUpdateTypeUserQuota || i == BatchUpdateTypeUsedQuota || i == BatchUpdateTypeRequestCount {
			continue
		}
		for key, value := range store {
			switch i {
			case BatchUpdateTypeTokenQuota:
				err := increaseTokenQuota(key, value)
				if err != nil {
					// 落库失败必须把增量还回队列,下一轮重试。
					// 队列在上面已经被整体换出,不还回去这笔扣减就永久消失了 ——
					// 而消费日志照写不误,logs 记的钱与令牌实际被扣的钱从此对不上。
					// addNewRecord 是按 id 相加的,重排队与本轮期间新产生的增量
					// 会自动合并,不会覆盖。
					addNewRecord(i, key, value)
					common.SysLog("failed to batch update token quota (requeued): " + err.Error())
				}
			case BatchUpdateTypeChannelUsedQuota:
				updateChannelUsedQuota(key, value)
			}
		}
	}

	userQuotaStore := stores[BatchUpdateTypeUserQuota]
	usedQuotaStore := stores[BatchUpdateTypeUsedQuota]
	requestCountStore := stores[BatchUpdateTypeRequestCount]

	userIDs := make(map[int]struct{}, len(userQuotaStore)+len(usedQuotaStore)+len(requestCountStore))
	for key := range userQuotaStore {
		userIDs[key] = struct{}{}
	}
	for key := range usedQuotaStore {
		userIDs[key] = struct{}{}
	}
	for key := range requestCountStore {
		userIDs[key] = struct{}{}
	}
	for key := range userIDs {
		if err := updateUserQuotaUsedQuotaAndRequestCount(key,
			userQuotaStore[key], usedQuotaStore[key], requestCountStore[key]); err != nil {
			// 三列走同一条 UPDATE 语句,失败就是三列都没落库(单语句原子),
			// 所以三个增量一起还回队列。不还回去的后果是**用户白嫖**:
			// quota 没被扣、used_quota 与 request_count 也没涨,而这笔消费
			// 已经写进 logs 并且照常计佣发钱 —— 平台在同一笔上亏两次。
			addNewRecord(BatchUpdateTypeUserQuota, key, userQuotaStore[key])
			addNewRecord(BatchUpdateTypeUsedQuota, key, usedQuotaStore[key])
			addNewRecord(BatchUpdateTypeRequestCount, key, requestCountStore[key])
			common.SysLog("failed to batch update user quota (requeued): " + err.Error())
		}
	}
	common.SysLog("batch update finished")
}

func RecordExist(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}

func shouldUpdateRedis(fromDB bool, err error) bool {
	return common.RedisEnabled && fromDB && err == nil
}
