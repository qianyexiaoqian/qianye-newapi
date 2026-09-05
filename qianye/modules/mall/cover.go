package mall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/imagestore"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// cover.go —— 商品封面(形状照 lottery/cover.go,只保留"上传落盘"这一种来源)。
//
// 落盘、魔数判定、服务端生成文件名、分片目录、MaxBytesReader、O_EXCL、nosniff 下载
// 这一整套住在 qianye/service/imagestore 里;本文件只负责它无从知道的三件事:
// 落在哪个目录、元数据存哪张表(qy_ml_cover)、谁有权取回。
//
// 取回是**匿名**的(PublicRouter):`<img src>` 由浏览器发出、不带 Authorization。
// 匿名安全的前提与 lottery 相同 —— 只回已经绑到某件商品上的封面、ref 是 128 位随机数、
// 封面本来就是面向所有访客的招贴。上传与丢弃只在管理端。

const (
	// coverDirName 是落盘目录名,与配置文件同级(即 Docker/本地下的 data/)。
	coverDirName = "qy-mall-covers"
	// coverMaxBytes 是单张封面的字节上限。商城没有单独的配置项:它的语义完全依附于
	// "一张商品图有多大",4 MiB 对手机截图与设计稿都绰绰有余,imagestore.MaxBytes
	// 是第二道硬上界。
	coverMaxBytes = 4 << 20
	// coverOrphanSeconds 是"这张图已经没人用了"到"从磁盘上删掉"之间的宽限期。
	coverOrphanSeconds = 24 * 3600
	// coverPendingMax 是单个管理员同时挂着的未绑定上传数上限:没有它,一把被盗的
	// 管理员令牌可以用"只传图、不保存商品"把磁盘打满,而商品数上限拦不到这条路径。
	coverPendingMax = 10
	// coverPendingGate 是每人待挂封面上限这道闸门的锚点行键前缀(qymodel.LockGate)。
	coverPendingGate = "mall:cover_pending:"
	// coverPruneBatch 是回收任务每一轮的处理条数上限。
	coverPruneBatch = 200
)

// coverStore 是商品封面的落盘目录。与 lottery / 工单 / 提现各持一个,互不干扰。
var coverStore = imagestore.New(coverDirName)

// acceptCoverUpload 落盘一张封面并登记元数据。
//
// 顺序是【先写库行、再写文件】:反过来的话,写完文件而插库失败会留下一个没有任何行
// 指向它的孤儿文件 —— 回收任务按库行扫,永远清不掉它。
func acceptCoverUpload(c *gin.Context, adminId int) (*Cover, error) {
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	gdb := handle.WithContext(c.Request.Context())

	data, kind, err := imagestore.Accept(c, "file", coverMaxBytes)
	if err != nil {
		switch {
		case errors.Is(err, imagestore.ErrTooLarge):
			return nil, errCoverTooLarge
		case errors.Is(err, imagestore.ErrType):
			return nil, errCoverType
		default:
			return nil, errCoverRequired
		}
	}
	storedName, err := imagestore.NewStoredName(kind.Ext)
	if err != nil {
		return nil, errCoverStore
	}
	sum := sha256.Sum256(data)
	row := &Cover{
		Ref:        strings.ReplaceAll(common.GetUUID(), "-", ""),
		UserId:     adminId,
		StoredName: storedName,
		MimeType:   kind.Mime,
		Size:       int64(len(data)),
		Sha256:     hex.EncodeToString(sum[:]),
		CreatedAt:  common.GetTimestamp(),
	}

	// 计数与插入同事务,并靠锚点行锁串行化:给 COUNT 挂 FOR UPDATE 在 PostgreSQL 上
	// 直接报错、在 SQLite 上被整段丢弃,锚点行锁在三种方言上是同一把普通的单行排他锁。
	err = gdb.Transaction(func(tx *gorm.DB) error {
		if err := qymodel.LockGate(tx, coverPendingGate+strconv.Itoa(adminId)); err != nil {
			return err
		}
		var cnt int64
		if err := tx.Model(&Cover{}).
			Where("user_id = ? AND product_id = 0 AND detached_at = 0 AND purged_at = 0", adminId).
			Count(&cnt).Error; err != nil {
			return err
		}
		if cnt >= int64(coverPendingMax) {
			return errCoverPending
		}
		return tx.Create(row).Error
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			return nil, wrapInternal("登记封面", err)
		}
		return nil, err
	}

	if err := coverStore.Write(storedName, data); err != nil {
		// 文件没写成,这一行就是纯垃圾。删不掉也不要紧:它的 product_id 恒为 0,
		// 孤儿回收会在窗口到期后连同"文件本就不存在"一起收掉。
		if delErr := gdb.Where("id = ?", row.Id).Delete(&Cover{}).Error; delErr != nil {
			db.MarkFailure(delErr)
		}
		common.SysError("qianye/mall: 封面落盘失败: " + err.Error())
		return nil, errCoverStore
	}
	return row, nil
}

// bindCover 把一件商品的封面从 oldRef 切到 newRef。**必须在调用方的事务里。**
//
// 认领是一条带条件的 UPDATE 而不是"先查再改":先读后写在并发下会让同一张图被两件
// 商品同时认领。四个条件缺一不可:这一张、只能认领自己传的、当前没有商品在用它
// (product_id = 0 或已被换下)、没被回收。被换下的那一张只打 detached_at,
// 由回收任务在宽限期之后删文件 —— 磁盘操作不参与事务回滚。
func bindCover(tx *gorm.DB, productId int64, oldRef, newRef string, adminId int) error {
	if newRef == oldRef {
		return nil
	}
	now := common.GetTimestamp()
	if newRef != "" {
		res := tx.Model(&Cover{}).
			Where("ref = ? AND user_id = ? AND purged_at = 0 AND (product_id = 0 OR detached_at > 0)",
				newRef, adminId).
			Updates(map[string]any{"product_id": productId, "bound_at": now, "detached_at": 0})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errCoverNotFound
		}
	}
	if oldRef != "" {
		if err := tx.Model(&Cover{}).
			Where("ref = ? AND product_id = ? AND purged_at = 0", oldRef, productId).
			Updates(map[string]any{"detached_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

// discardPendingCover 丢弃一张【自己上传且尚未绑到任何商品上】的封面。
//
// 先删库行、再删文件:反过来的话文件删了而库行还在,取回接口会把一张"存在但打不开"
// 的图报成已回收。
func discardPendingCover(ctx context.Context, adminId int, ref string) error {
	handle := db.Get()
	if handle == nil {
		return db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)
	var row Cover
	err := gdb.Where("ref = ? AND user_id = ? AND product_id = 0 AND purged_at = 0", ref, adminId).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errCoverNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("读取封面", err)
	}
	res := gdb.Where("id = ? AND product_id = 0 AND purged_at = 0", row.Id).Delete(&Cover{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return wrapInternal("删除封面", res.Error)
	}
	if res.RowsAffected == 0 {
		// 这一瞬间它被另一个请求认领到某件商品上了。
		return errCoverNotFound
	}
	if err := coverStore.Remove(row.StoredName); err != nil {
		common.SysError("qianye/mall: 丢弃封面时删除文件失败: " + err.Error())
	}
	return nil
}

// loadCover 按 ref 取一行元数据。**不在这里判 purged_at**:"已回收"与"没有这个 ref"
// 是可区分的两个回答,判定属于调用方。
func loadCover(ctx context.Context, ref string) (*Cover, error) {
	handle := db.Get()
	if handle == nil {
		return nil, db.ErrNotReady
	}
	var row Cover
	err := handle.WithContext(ctx).Where("ref = ?", ref).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errCoverNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取封面", err)
	}
	return &row, nil
}

// serveCover 把封面回给调用者。缓存口径是公开且按 ref 不可变:一个 ref 对应的字节
// 永远不变(落盘名由 crypto/rand 生成、Write 带 O_EXCL)。
func serveCover(c *gin.Context, row *Cover) {
	full, err := coverStore.Locate(row.StoredName)
	if errors.Is(err, imagestore.ErrMalformedName) {
		common.SysError("qianye/mall: 封面元数据里的文件名形状非法: " + row.StoredName)
		respondErr(c, errCoverNotFound)
		return
	}
	if err != nil {
		// 库里有行、磁盘上没有文件:多节点各存各的时最常见的一种表现。不回 500。
		respondErr(c, errCoverPurged)
		return
	}
	_, ext, _ := strings.Cut(row.StoredName, ".")
	imagestore.Serve(c, full, row.MimeType, "mall-cover-"+row.Ref+"."+ext, imagestore.CachePublicImmutable)
}

// handleGetCover 匿名回一张【已经绑到某件商品上的】封面。
//
// 刻意不走 guard.RequireAPI(FlagMall):商城被临时关停之后,用户的订单页仍然会被打开,
// 那时每一张商品图都变成破图没有任何好处。只判扩展是否启用与库是否可用。
func handleGetCover(c *gin.Context) {
	if !guard.Enabled() {
		respondErr(c, errCoverNotFound)
		return
	}
	if !db.Available() {
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "封面暂不可用,请稍后重试",
		})
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		respondErr(c, errCoverNotFound)
		return
	}
	row, err := loadCover(c.Request.Context(), ref)
	if err != nil {
		respondErr(c, err)
		return
	}
	if row.ProductId == 0 || row.BoundAt == 0 {
		respondErr(c, errCoverNotFound)
		return
	}
	if row.PurgedAt > 0 {
		respondErr(c, errCoverPurged)
		return
	}
	serveCover(c, row)
}

// pruneCovers 清理不该再留在磁盘上的封面:从未使用的 / 已被换下的 / 商品已删除的。
//
// 第三条用 NOT IN 子查询判,而不是在删除商品的那段代码里补一句 UPDATE:删除路径
// 不止一条,每多一条都要记得再补一次,而漏掉的那一次是永久的磁盘泄漏。
func pruneCovers(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	handle := db.Get()
	if handle == nil {
		return
	}
	gdb := handle.WithContext(ctx)
	cutoff := common.GetTimestamp() - coverOrphanSeconds

	purgeCoverBatch(ctx, gdb, gdb.Model(&Cover{}).
		Where("purged_at = 0 AND product_id = 0 AND detached_at = 0 AND created_at > 0 AND created_at < ?",
			cutoff), "从未使用的封面")
	purgeCoverBatch(ctx, gdb, gdb.Model(&Cover{}).
		Where("purged_at = 0 AND detached_at > 0 AND detached_at < ?", cutoff), "已被换下的封面")
	live := gdb.Model(&Product{}).Select("id")
	purgeCoverBatch(ctx, gdb, gdb.Model(&Cover{}).
		Where("purged_at = 0 AND product_id > 0 AND product_id NOT IN (?)", live), "商品已删除的封面")
}

// purgeCoverBatch 执行一轮"取一批 → 删文件 → 标记已清"。先删文件再标记:标记完再删,
// 中途崩溃会留下一个 purged_at 已置位却仍躺在磁盘上的文件,从此不会再被任何一轮扫到。
func purgeCoverBatch(ctx context.Context, gdb *gorm.DB, scope *gorm.DB, what string) {
	if ctx.Err() != nil {
		return
	}
	rows := make([]Cover, 0, coverPruneBatch)
	if err := scope.Order("id asc").Limit(coverPruneBatch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/mall: 扫描" + what + "失败: " + err.Error())
		return
	}
	if len(rows) == 0 {
		return
	}
	done := make([]int64, 0, len(rows))
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := coverStore.Remove(rows[i].StoredName); err != nil {
			common.SysError("qianye/mall: 删除" + what + "文件失败(下一轮重试): " + err.Error())
			continue
		}
		done = append(done, rows[i].Id)
	}
	if len(done) == 0 {
		return
	}
	res := gdb.Model(&Cover{}).Where("id IN ? AND purged_at = 0", done).
		Updates(map[string]any{"purged_at": common.GetTimestamp()})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/mall: 标记" + what + "已回收失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/mall: 已回收 %d 张%s", res.RowsAffected, what))
	}
}
