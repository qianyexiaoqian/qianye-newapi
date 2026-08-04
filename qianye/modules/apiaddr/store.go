package apiaddr

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// handle 取出一个已绑定 ctx 的扩展库句柄。
//
// 全模块只有这一个取句柄的地方:db.Get() 返回 nil 与"忘了 WithContext"是本仓
// 反复出现的两个漏点,收成一处之后调用方只可能拿到已绑定的句柄。
func handle(ctx context.Context) (*gorm.DB, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	return gdb.WithContext(ctx), nil
}

// orderClause 是全模块唯一的排序口径。
//
// 必须是**全序**:只按 sort_order 排的话,两条同序号的行在不同数据库、
// 不同执行计划下顺序会变 —— 而"第一条"正是用户侧只有一条时直接采用的那条,
// 也是重排后 UI 与库不一致的来源。
const orderClause = "sort_order asc, id asc"

// listAll 按展示顺序读出全部地址。onlyEnabled 为 true 时只取已启用的。
func listAll(ctx context.Context, gdb *gorm.DB, onlyEnabled bool) ([]Address, error) {
	q := gdb.WithContext(ctx).Model(&Address{})
	if onlyEnabled {
		q = q.Where("enabled = ?", true)
	}
	rows := make([]Address, 0, maxAddresses)
	if err := q.Order(orderClause).Limit(maxAddresses).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return rows, nil
}

// loadRow 读出一行。不存在时返回 errNotFound。
func loadRow(ctx context.Context, gdb *gorm.DB, id int) (Address, error) {
	var row Address
	err := gdb.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Address{}, errNotFound
		}
		db.MarkFailure(err)
		return Address{}, err
	}
	return row, nil
}

// urlTaken 判断除 excludeId 之外是否已有同 URL 的行。
//
// 用应用层判重而不是唯一索引:URL 列是 varchar(512),MySQL 的 utf8mb4 下
// 单列唯一索引的键长上限(3072 字节)刚好卡在边界上,不同版本/不同行格式的
// 结果不一样;而且撞了之后 GORM 抛回来的是驱动错误,还要再解析一次才能变成
// 一句人话。判重本身不是并发敏感的:两个管理员在同一秒加同一个地址,
// 最坏结果是列表里出现两条一样的,运营删掉一条即可 —— 没有任何账目后果。
func urlTaken(ctx context.Context, gdb *gorm.DB, rawURL string, excludeId int) (bool, error) {
	var count int64
	q := gdb.WithContext(ctx).Model(&Address{}).Where("url = ?", rawURL)
	if excludeId > 0 {
		q = q.Where("id <> ?", excludeId)
	}
	if err := q.Count(&count).Error; err != nil {
		db.MarkFailure(err)
		return false, err
	}
	return count > 0, nil
}

// countAll 返回当前行数。
func countAll(ctx context.Context, gdb *gorm.DB) (int64, error) {
	var count int64
	if err := gdb.WithContext(ctx).Model(&Address{}).Count(&count).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	return count, nil
}

// nextSortOrder 返回追加到末尾应使用的序号。
//
// 空表时 Pluck 到的是零值,于是第一条拿 sortStep 而不是 0 —— 留出把某条
// 手工提到最前面的空间。
func nextSortOrder(ctx context.Context, gdb *gorm.DB) (int, error) {
	var maxOrder *int
	if err := gdb.WithContext(ctx).Model(&Address{}).
		Select("MAX(sort_order)").Scan(&maxOrder).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	if maxOrder == nil {
		return sortStep, nil
	}
	return *maxOrder + sortStep, nil
}

// applyOrder 在一个事务里把整表重排成 ids 给出的顺序。
//
// # 为什么必须"整表提交 + 全集比对"
//
// 只发"这一条移到第几位"的话,并发下两个管理员各自基于自己那份旧列表算出的
// 目标位置会互相踩;而"提交完整顺序 + 要求 id 集合与库里当前完全一致"把这件事
// 变成一次乐观锁:有人在你编辑期间加了或删了一条,你的提交必然被拒(errOrderStale),
// 而不是把他新加的那条静默挤到末尾。
func applyOrder(ctx context.Context, gdb *gorm.DB, ids []int) error {
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current := make([]int, 0, maxAddresses)
		if err := tx.WithContext(ctx).Model(&Address{}).
			Order(orderClause).Pluck("id", &current).Error; err != nil {
			return err
		}
		if len(current) != len(ids) {
			return errOrderStale
		}
		want := make(map[int]bool, len(ids))
		for _, id := range ids {
			want[id] = true
		}
		for _, id := range current {
			if !want[id] {
				return errOrderStale
			}
		}
		for index, id := range ids {
			if err := tx.WithContext(ctx).Model(&Address{}).Where("id = ?", id).
				Update("sort_order", (index+1)*sortStep).Error; err != nil {
				return err
			}
		}
		return nil
	})
	// errOrderStale 是业务判定,不是扩展库故障 —— 把它算进熔断的失败计数,
	// 会让"两个管理员同时排序"这种日常操作把整个扩展推向不可用。
	var be *bizError
	if err != nil && !errors.As(err, &be) {
		db.MarkFailure(err)
	}
	return err
}
