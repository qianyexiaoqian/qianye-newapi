package invite

// relation_sync.go —— 把主库的邀请关系补进扩展库快照。
//
// # 为什么必须有这个任务
//
// qy_invite_relation 是**懒建**的(见 api_admin_relation.go 文件头):ensureRelation
// 只在某个下线第一次被解析到上线时才写那一行,而解析只发生在返还链路上。
// 管理端列表早已改成从主库出,但用户侧「我的推广」的下线列表 / 下线人数 /
// 今日下线消费、佣金页的下线数,读的仍是这张快照。于是上线前就绑定的关系、
// 以及还没产生过任何返还的新下线,在**邀请人自己的页面上**整行缺失,没有任何报错。
// 生产实测:主库 466 条绑定,快照只有 22 条。
//
// # 做法
//
// 按 users.id 游标扫描 inviter_id > 0 的账号,对快照里还没有的那些调 ensureRelation ——
// 与解析路径写的是同一行、走同一套互邀环路判定;已存在的行一个字节都不动
// (OnConflict DoNothing),管理员手工拉黑的决定因此不会被覆盖。
// 已软删的账号由 GORM 自动排除,与管理端列表的口径一致。

import (
	"context"
	"sync/atomic"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
)

const (
	relationSyncBatch = 500
	// relationSyncOverlap 是每轮从游标往回多看的 id 跨度:自增 id 在插入时分配、
	// 提交可能更晚,只看 id > 游标会永久漏掉后提交的那几个注册。
	relationSyncOverlap = 200
)

// relationSyncCursor 是本进程扫到的最大 users.id。只在内存里:换节点拿到租约时
// 从 0 重扫一遍,代价是一轮全量的 DoNothing,换来不需要再落一个游标。
var relationSyncCursor atomic.Int64

// syncRelationSnapshots 补建主库里有、快照里没有的邀请关系。
func syncRelationSnapshots(ctx context.Context) error {
	gdb := db.Get()
	if model.DB == nil || gdb == nil {
		return nil
	}
	from := max(relationSyncCursor.Load()-relationSyncOverlap, 0)
	for {
		var users []struct {
			Id        int
			InviterId int
			Username  string
			Email     string
			CreatedAt int64
		}
		if err := model.DB.WithContext(ctx).Model(&model.User{}).
			Select("id", "inviter_id", "username", "email", "created_at").
			Where("id > ? AND inviter_id > ? AND inviter_id <> id", from, 0).
			Order("id asc").Limit(relationSyncBatch).
			Find(&users).Error; err != nil {
			return err
		}
		if len(users) == 0 {
			return nil
		}

		ids := make([]int, 0, len(users))
		for _, u := range users {
			ids = append(ids, u.Id)
		}
		var present []int
		if err := gdb.WithContext(ctx).Model(&InviteRelation{}).
			Where("invitee_id IN ?", ids).Pluck("invitee_id", &present).Error; err != nil {
			db.MarkFailure(err)
			return err
		}
		have := make(map[int]bool, len(present))
		for _, id := range present {
			have[id] = true
		}
		for _, u := range users {
			if have[u.Id] {
				continue
			}
			// 与 resolveInviter 同一个口径:没有用户名的账号用邮箱做脱敏底稿。
			name := u.Username
			if name == "" {
				name = u.Email
			}
			ensureRelation(ctx, u.InviterId, u.Id, name, u.CreatedAt)
		}

		last := int64(users[len(users)-1].Id)
		if last > relationSyncCursor.Load() {
			relationSyncCursor.Store(last)
		}
		if len(users) < relationSyncBatch {
			return nil
		}
		from = last
	}
}
