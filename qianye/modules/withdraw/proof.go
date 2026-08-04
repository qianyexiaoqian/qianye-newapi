package withdraw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/service/imagestore"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 提现凭证图片(裁决 3:本地磁盘 + 鉴权下载)。
//
// 落盘/取回/清理的通用机制(魔数判定、服务端生成文件名、分片目录、
// MaxBytesReader 前置截断、O_EXCL、nosniff)已经收进 qianye/service/imagestore ——
// 工单系统也要收用户上传的截图,同一套防线在两个模块里各写一遍就是本仓反复
// 栽的"同一概念的第 N 份拷贝各自漂移",而这条路上漏掉任何一条防线的后果都不是
// 显示问题(理由逐条写在那个包的包注释里)。
//
// 本文件因此只保留提现自己的部分:什么时候允许上传(ProofOn + 未绑定上传数
// 上限)、元数据长什么样、一张凭证归哪张单、什么时候该从磁盘上消失。

const (
	// proofDirName 是落盘目录名,与配置文件同级(即 Docker/本地下的 data/)。
	// 整个 /data/ 已在 .gitignore 内。
	proofDirName = "qy-withdraw-proofs"

	// proofOrphanSeconds 是"上传了却一直没提交单据"的容忍窗口。
	//
	// 固化在代码里而不是新增配置项:它的语义完全依附于"用户填一张表单要多久",
	// 24 小时对任何人都绰绰有余,单独放出来只会变成又一个"定义了却没人调"的旋钮
	// (那正是 selfcheck.go 里记着的四次翻车)。
	proofOrphanSeconds = 24 * 3600

	// proofPendingMax 限制单个用户同时挂着的未绑定上传数。
	//
	// 没有它,一个登录用户可以用"只上传、不提交"把磁盘打满 —— 而提现单本身的
	// 那几道闸门(daily_max_count / max_pending_orders)一个都拦不到这条路径,
	// 因为它压根没有创建单据。
	proofPendingMax = 3
)

// proofStore 是提现凭证的落盘目录。
var proofStore = imagestore.New(proofDirName)

// proofKind 是被接受的图片类型,直接复用 imagestore 的定义 ——
// 类型白名单必须与判定魔数的那一份逐字节相同,否则会出现"判定通过但扩展名
// 认不出"的行。
type proofKind = imagestore.Kind

var (
	proofJPEG = imagestore.JPEG
	proofPNG  = imagestore.PNG
)

// ProofAcceptMimes 供 /withdraw/config 下发给前端做 accept 属性。
// 前端的 accept 只是体验,真正的判定在 sniffProof。
func ProofAcceptMimes() []string { return imagestore.AcceptMimes() }

// sniffProof 按【魔数】判定图片类型,不信扩展名也不信 Content-Type 请求头。
func sniffProof(head []byte) (proofKind, bool) { return imagestore.Sniff(head) }

// newProofStoredName 生成落盘文件名(服务端生成、128 位熵、白名单扩展名)。
func newProofStoredName(ext string) (string, error) { return imagestore.NewStoredName(ext) }

// proofRelPath 把落盘文件名解析成相对目录内的路径,并顺便校验它的形状。
func proofRelPath(storedName string) (string, bool) { return imagestore.RelPath(storedName) }

// proofDir 是凭证目录:与配置文件同级的 qy-withdraw-proofs/。
func proofDir() string { return proofStore.Dir() }

// ─────────────────────────────── 上传 ───────────────────────────────

// acceptProofUpload 落盘一张凭证并登记元数据。
//
// 顺序是【先写库行、再写文件】。反过来的话,写完文件而插库失败会留下一个
// 没有任何行指向它的孤儿文件 —— 磁盘上永远清不掉,因为清理任务是按库行扫的。
// 现在这个方向最坏只会留下一行指向不存在文件的元数据,而下载与清理都能优雅处理。
func acceptProofUpload(c *gin.Context, userId int) (*Proof, error) {
	cfg := config.Get().Withdraw
	if !cfg.ProofOn() {
		return nil, errProofDisabled
	}
	maxBytes := cfg.ProofMaxBytes
	if maxBytes <= 0 || maxBytes > config.MaxWithdrawProofBytes {
		// 校验器已经挡过一次,这里是第二道:配置热更新之后仍然要有确定的上界。
		maxBytes = config.MaxWithdrawProofBytes
	}

	data, kind, err := imagestore.Accept(c, "file", maxBytes)
	if err != nil {
		return nil, proofUploadError(err)
	}

	storedName, err := newProofStoredName(kind.Ext)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	row := &Proof{
		Ref:        strings.ReplaceAll(common.GetUUID(), "-", ""),
		UserId:     userId,
		StoredName: storedName,
		MimeType:   kind.Mime,
		Size:       int64(len(data)),
		Sha256:     hex.EncodeToString(sum[:]),
		CreatedAt:  common.GetTimestamp(),
	}

	// 计数与插入同事务,理由与 createPayeeAccount 完全一样:分开做的话
	// 并发提交会双双读到旧计数一起通过,上限形同虚设。
	err = db.Get().Transaction(func(tx *gorm.DB) error {
		var cnt int64
		if err := tx.Model(&Proof{}).
			Where("user_id = ? AND withdrawal_id = 0 AND purged_at = 0", userId).
			Count(&cnt).Error; err != nil {
			return err
		}
		if cnt >= proofPendingMax {
			return errProofPendingLimit
		}
		return tx.Create(row).Error
	})
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}

	if err := writeProofFile(storedName, data); err != nil {
		// 文件没写成,这一行就是纯垃圾。删不掉也不要紧:它的 withdrawal_id 恒为 0,
		// 孤儿清理会在窗口到期后连同"文件本就不存在"一起收掉。
		if delErr := db.Get().Where("id = ?", row.Id).Delete(&Proof{}).Error; delErr != nil {
			db.MarkFailure(delErr)
		}
		common.SysError("qianye/withdraw: 凭证落盘失败: " + err.Error())
		return nil, errProofStore
	}
	return row, nil
}

// proofUploadError 把 imagestore 的哨兵错误翻译成提现自己的业务 code。
//
// 四种情况分得比"上传失败"细,是因为用户能做的事完全不同:换一张图、压缩一下、
// 先去用掉已传的、还是找管理员 —— 合并成一条只会让人反复重试同一张图。
func proofUploadError(err error) error {
	switch {
	case errors.Is(err, imagestore.ErrTooLarge):
		return errProofTooLarge
	case errors.Is(err, imagestore.ErrType):
		return errProofType
	default:
		return errProofRequired
	}
}

// writeProofFile 把内容写进凭证目录。
func writeProofFile(storedName string, data []byte) error {
	if err := proofStore.Write(storedName, data); err != nil {
		return fmt.Errorf("qianye/withdraw: 写入凭证失败: %w", err)
	}
	return nil
}

// removeProofFile 从磁盘删除一张凭证。文件本就不存在、或文件名形状非法都视为
// 成功:清理任务的目的是"磁盘上没有它",而对着一个坏掉的名字去猜,猜错的方向
// 是删掉别的文件。
func removeProofFile(storedName string) error { return proofStore.Remove(storedName) }

// ─────────────────────────────── 绑定 ───────────────────────────────

// bindProof 把一张已上传的凭证挂到刚落库的提现单上。
//
// 整个判定就是这一条带条件的 UPDATE,而不是"先查出来看看是不是本人的、
// 是不是还没用过、再更新":先读后写在并发下必然出现同一张凭证被两张单同时认领。
// 四个条件缺一不可 ——
//
//	ref            这一张
//	user_id        只能是本人的(越权的唯一防线,不能放到取回来之后再比)
//	withdrawal_id  必须还没被别的单认领(一张凭证一张单)
//	purged_at      已被清理的凭证不能再被引用
//
// 必须与落单在同一个事务里:提交失败时凭证要跟着回到未使用状态,
// 否则用户重试一次就会被告知"凭证不存在",而他明明刚传过。
func bindProof(tx *gorm.DB, w *Withdrawal, ref string) error {
	res := tx.Model(&Proof{}).
		Where("ref = ? AND user_id = ? AND withdrawal_id = 0 AND purged_at = 0", ref, w.UserId).
		Updates(map[string]any{
			"withdrawal_id": w.Id,
			"withdraw_no":   w.WithdrawNo,
			"bound_at":      common.GetTimestamp(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errProofNotFound
	}
	return nil
}

// loadProofOfWithdrawal 取出某张单的凭证元数据。
func loadProofOfWithdrawal(withdrawalId int64) (*Proof, error) {
	var row Proof
	err := db.Get().Where("withdrawal_id = ?", withdrawalId).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errProofNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	if row.PurgedAt > 0 {
		return nil, errProofPurged
	}
	return &row, nil
}

// serveProof 把凭证文件回给已鉴权的调用者。
//
// 三个响应头都是必须的:
//   - Content-Type 由入库时的魔数判定给出,不由扩展名或请求头决定
//   - X-Content-Type-Options: nosniff 让浏览器不要自作主张改判类型 ——
//     一份伪装成 JPEG 的 HTML 不能因为浏览器"猜"出 text/html 就被当页面执行
//   - Cache-Control: private, no-store 防止凭证进共享缓存或磁盘缓存
func serveProof(c *gin.Context, p *Proof) {
	full, err := proofStore.Locate(p.StoredName)
	if errors.Is(err, imagestore.ErrMalformedName) {
		common.SysError("qianye/withdraw: 凭证元数据里的文件名形状非法: " + p.StoredName)
		respondErr(c, errProofNotFound)
		return
	}
	if err != nil {
		// 库里有行、磁盘上没有文件:多节点各存各的时最常见的一种表现,
		// 也可能是落盘失败留下的残行。不回 500 —— 它不是服务端故障。
		respondErr(c, errProofPurged)
		return
	}
	imagestore.Serve(c, full, p.MimeType, proofDownloadName(p))
}

// proofDownloadName 是回给浏览器的文件名。
// 由单号 + 服务端扩展名拼出,永远不含用户提供的任何字符串。
func proofDownloadName(p *Proof) string {
	_, ext, _ := strings.Cut(p.StoredName, ".")
	no := p.WithdrawNo
	if no == "" {
		no = p.Ref
	}
	return "proof-" + no + "." + ext
}

// ─────────────────────────────── 清理 ───────────────────────────────

// pruneProofs 清理不该再留在磁盘上的凭证,是 pruneExpiredPii 的第四个面。
//
// 三条到期口径,合起来才覆盖住"图片随单据一起消失"这个要求:
//
//	A. 孤儿      —— 传了但从没提交单据,超过 proofOrphanSeconds
//	B. 单据失败  —— 被拒绝 / 撤销 / 打款失败,钱没出去,凭证没有留存价值(裁决 3)
//	C. 保留期到  —— 已打款的单据,与收款信息密文同一个 pii_retention_days
//
// A 与 B 不受 pii_retention_days 控制。理由:那个键回答的是"收款信息要留多久",
// 而孤儿文件从来不是任何一条记录的一部分,失败单的凭证则是裁决明确要求随单清掉的。
// 把它们挂在同一个开关上,运维填一个负数关掉保留期的同时会打开一个磁盘泄漏。
//
// 终态/孤儿判定一律写进【扫描条件】而不是取回来再过滤 —— 与 prunePii、
// prunePayeeAccounts 同一个坑:一批永远不可清的行(比如挂了半年的待审单的凭证)
// 会把每一轮 batch 占满,后面所有到期行从此再也轮不到。
func pruneProofs(ctx context.Context, gdb *gorm.DB, batch int) {
	if ctx.Err() != nil {
		return
	}
	if batch <= 0 {
		batch = 200
	}
	now := common.GetTimestamp()

	// A. 孤儿:从没被任何单据认领,且已过窗口。
	purgeProofBatch(ctx, gdb, batch, gdb.Model(&Proof{}).
		Where("purged_at = 0 AND withdrawal_id = 0 AND created_at > 0 AND created_at < ?",
			now-proofOrphanSeconds), "孤儿凭证")

	// B. 钱没出去的终态单。用子查询而不是 JOIN:GORM 的 Pluck 走 JOIN 时
	// 列名歧义要靠手写别名,而子查询在三种数据库上的行为完全一致。
	failed := gdb.Model(&Withdrawal{}).Select("id").
		Where("status IN ?", []string{StatusRejected, StatusCancelled, StatusFailed})
	purgeProofBatch(ctx, gdb, batch, gdb.Model(&Proof{}).
		Where("purged_at = 0 AND withdrawal_id > 0 AND withdrawal_id IN (?)", failed),
		"失败单据的凭证")

	// C. 保留期到期的已打款单。这一条才归 pii_retention_days 管。
	days := config.Get().Withdraw.PIIRetentionDays
	if days <= 0 {
		return
	}
	paid := gdb.Model(&Withdrawal{}).Select("id").Where("status = ?", StatusPaid)
	purgeProofBatch(ctx, gdb, batch, gdb.Model(&Proof{}).
		Where("purged_at = 0 AND withdrawal_id > 0 AND created_at > 0 AND created_at < ? AND withdrawal_id IN (?)",
			now-int64(days)*86400, paid), "到期凭证")
}

// purgeProofBatch 执行一轮"取一批 → 删文件 → 标记已清"。
//
// 先删文件再标记,不是反过来:标记完再删,中途崩溃会留下一个 purged_at 已置位
// 却仍躺在磁盘上的文件 —— 它从此不会再被任何一轮扫到,清理任务在日志里
// 一切正常,而 PII 永远留在盘上。删文件失败的那一行则原样留着,下一轮再试。
func purgeProofBatch(ctx context.Context, gdb *gorm.DB, batch int, scope *gorm.DB, what string) {
	if ctx.Err() != nil {
		return
	}
	var rows []Proof
	if err := scope.Order("id asc").Limit(batch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 扫描" + what + "失败: " + err.Error())
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
		if err := removeProofFile(rows[i].StoredName); err != nil {
			common.SysError("qianye/withdraw: 删除" + what + "文件失败(下一轮重试): " + err.Error())
			continue
		}
		done = append(done, rows[i].Id)
	}
	if len(done) == 0 {
		return
	}

	// 再带一次 purged_at = 0,理由同 prunePii:两个节点的租约交接窗口里可能
	// 重叠执行一轮,条件写在 UPDATE 上才不会把 purged_at 刷成第二个时间戳。
	res := gdb.Model(&Proof{}).Where("id IN ? AND purged_at = 0", done).
		Updates(map[string]any{"purged_at": common.GetTimestamp()})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/withdraw: 标记" + what + "已清理失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/withdraw: 已清除 %d 张%s", res.RowsAffected, what))
	}
}
