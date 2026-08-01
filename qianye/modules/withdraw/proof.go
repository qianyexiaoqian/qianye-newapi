package withdraw

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 提现凭证图片(裁决 3:本地磁盘 + 鉴权下载)。
//
// 本仓与上游此前【没有任何文件上传设施】—— 没有 SaveUploadedFile、没有对象存储
// SDK、静态资源全部走 embed.FS。这是好事:磁盘目录默认不可能被 Web 访问到
// (router/web-router.go 只 Serve 了 embed 的 web/dist),所以"落盘目录不得在静态
// 资源路由可达范围内"这一条不需要额外配置来保证,只需要不引入新的静态路由。
//
// 全部防线在下面逐条注明。要点是:文件名服务端生成、类型按魔数判定、大小在读
// 第一个字节之前就被 MaxBytesReader 截断、下载必须鉴权、单据终结即清理。

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

	// proofRandomBytes 是文件名的随机位数(16 字节 → 32 位十六进制)。
	// 必须用 crypto/rand:文件名可预测意味着可以被枚举,而下载接口的鉴权
	// 一旦哪天被写错,可预测的文件名就是直接的批量拖库入口。
	proofRandomBytes = 16
)

// proofKind 是一种被接受的图片类型。
//
// 白名单写死在代码里而不是配置里,理由与 payeeSpecs 相同:每一种类型都对应
// 一段魔数判定与一个扩展名,配置里加一个 "gif" 而代码不认识它,结果是
// 文件被存成一个谁都认不出的扩展名。
type proofKind struct {
	Mime string
	Ext  string
}

var (
	proofJPEG = proofKind{Mime: "image/jpeg", Ext: "jpg"}
	proofPNG  = proofKind{Mime: "image/png", Ext: "png"}
	proofWEBP = proofKind{Mime: "image/webp", Ext: "webp"}
)

// proofExts 是允许出现在磁盘文件名里的扩展名集合。
// 它是 proofKind 的投影,而不是另写一份:两处各写一遍就是"同一概念的第二份拷贝"。
var proofExts = map[string]bool{
	proofJPEG.Ext: true, proofPNG.Ext: true, proofWEBP.Ext: true,
}

// ProofAcceptMimes 供 /withdraw/config 下发给前端做 accept 属性。
// 前端的 accept 只是体验,真正的判定在 sniffProof。
func ProofAcceptMimes() []string {
	return []string{proofJPEG.Mime, proofPNG.Mime, proofWEBP.Mime}
}

// sniffProof 按【魔数】判定图片类型。
//
// 三条都不信:
//   - 不信扩展名 —— 用户提供的文件名根本不落盘,更不参与判定
//   - 不信 Content-Type 请求头 —— 那是客户端随便写的一个字符串
//   - 不信 http.DetectContentType —— 它会对认不出的内容回退成
//     "text/plain" 或 "application/octet-stream",而我们需要的是"不认识就拒绝"
//
// 刻意【不解码图片】。解码才是解压炸弹(一张 1 KB 的 PNG 可以声明 60000×60000)
// 真正的风险面;不解码就没有这个面。代价是我们无法保证文件是一张能渲染的图,
// 那由浏览器自己承担 —— 而下载响应带着 nosniff + 精确 Content-Type,
// 一份伪装成图片的 HTML 不会被当作 HTML 执行。
func sniffProof(head []byte) (proofKind, bool) {
	switch {
	case len(head) >= 3 && bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return proofJPEG, true
	case bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return proofPNG, true
	case len(head) >= 12 && bytes.HasPrefix(head, []byte("RIFF")) &&
		bytes.Equal(head[8:12], []byte("WEBP")):
		return proofWEBP, true
	default:
		return proofKind{}, false
	}
}

// newProofStoredName 生成落盘文件名。
func newProofStoredName(ext string) (string, error) {
	buf := make([]byte, proofRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf) + "." + ext, nil
}

// proofRelPath 把落盘文件名解析成相对目录内的路径,并顺便校验它的形状。
//
// 名字是服务端生成的,那为什么还要校验?因为拼进 filepath.Join 的这一步
// 不该依赖"上游一定没被改过":数据库行可以被 DBA 手工改、可以被一次 SQL 注入
// 写坏,而 filepath.Join("dir", "../../etc/passwd") 会老老实实地跳出目录。
// 一次廉价的形状校验换掉一整类"只要别处出一个洞,这里就变成任意文件读写"。
//
// 前两位十六进制做分片子目录:一个目录里堆几十万个文件,ext4 与 NTFS 都会
// 明显变慢,而分片之后单目录期望只有 1/256。
func proofRelPath(storedName string) (string, bool) {
	base, ext, ok := strings.Cut(storedName, ".")
	if !ok || !proofExts[ext] || len(base) != proofRandomBytes*2 {
		return "", false
	}
	if !isLowerHex(base) {
		return "", false
	}
	return filepath.Join(base[:2], storedName), true
}

// isLowerHex 只认小写十六进制。
//
// 刻意不用 hex.DecodeString:它大小写通吃,而生成侧的 hex.EncodeToString
// 只产小写 —— 校验一旦比生成器松,就凭空多出一批"服务端永远不会生成、
// 校验却放行"的名字。在大小写不敏感的文件系统上(NTFS、默认的 APFS/HFS+),
// ABC.jpg 与 abc.jpg 指向同一个文件,于是同一份磁盘文件对应两个都能过校验的
// 数据库值 —— 清理任务按其中一个删、下载接口按另一个取,两边就对不上了。
// 校验必须与生成器逐字符等价,不多不少。
func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// proofDir 是凭证目录:与配置文件同级的 qy-withdraw-proofs/。
//
// 跟着 config.Path() 走而不是新增一个 proof_dir 配置项,是刻意的:
// 目录可配意味着运维可以把它指到 web 根目录下,而"落盘目录不得在静态资源
// 路由可达范围内"这条约束没有任何机制能替他守住。锚在配置文件旁边则天然满足 ——
// 本项目的静态资源只有 embed.FS 一个来源,磁盘上的任何目录都不可达。
func proofDir() string {
	return filepath.Join(filepath.Dir(config.Path()), proofDirName)
}

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

	// 【必须在读第一个字节之前】。放在 FormFile 之后等于先把整个请求体收下来,
	// 那时限流的意义已经没有了 —— 一个 2 GiB 的 POST 已经吃完了内存和磁盘临时空间。
	// 多给 1 MiB 是 multipart 边界与表单头的开销,不是内容的余量。
	bodyCap := maxBytes + (1 << 20)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, bodyCap)

	// ContentLength 是客户端声明的,【不是】准入依据 —— 真正的截断由上面那行做。
	// 这里读它只为把错误码从"表单解析失败"提升成"文件太大":多层解析之后
	// MaxBytesError 会不会被原样透出取决于 mime/multipart 的实现细节,
	// 而告诉用户"图片太大"和"请求格式不对"是两种完全不同的指引。
	if c.Request.ContentLength > bodyCap {
		return nil, errProofTooLarge
	}

	fh, err := c.FormFile("file")
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errProofTooLarge
		}
		return nil, errProofRequired
	}
	if fh.Size > maxBytes {
		return nil, errProofTooLarge
	}

	src, err := fh.Open()
	if err != nil {
		return nil, errProofRequired
	}
	defer func() { _ = src.Close() }()

	// 读满 maxBytes+1:恰好等于上限是合法的,多出一个字节才是超限。
	// header 里的 Size 已经查过一次,这里再查一次是因为 Size 来自客户端声明的
	// Content-Length,而真正落盘的是这条流。
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errProofTooLarge
		}
		return nil, errProofRequired
	}
	if int64(len(data)) > maxBytes {
		return nil, errProofTooLarge
	}
	if len(data) == 0 {
		return nil, errProofRequired
	}
	kind, ok := sniffProof(data)
	if !ok {
		return nil, errProofType
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

// writeProofFile 把内容写进凭证目录。
//
// O_EXCL 是并发写同名的最后一道锁:文件名有 128 位熵,撞名在实践中不会发生,
// 但"不会发生"和"发生了会静默覆盖别人的凭证"之间差着一个标志位。
// 权限 0600 / 0700:凭证是 PII,同机器上的其他进程没有理由读到它。
func writeProofFile(storedName string, data []byte) error {
	rel, ok := proofRelPath(storedName)
	if !ok {
		return fmt.Errorf("qianye/withdraw: 非法的凭证文件名: %q", storedName)
	}
	full := filepath.Join(proofDir(), rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(full)
		return err
	}
	return f.Close()
}

// removeProofFile 从磁盘删除一张凭证。文件本就不存在视为成功 ——
// 清理任务的目的是"磁盘上没有它",不是"我亲手删掉了它"。
func removeProofFile(storedName string) error {
	rel, ok := proofRelPath(storedName)
	if !ok {
		// 形状不对的行不去猜它指向哪里,只告警。猜错的方向是删掉别的文件。
		common.SysError("qianye/withdraw: 凭证元数据里的文件名形状非法,已跳过删除: " + storedName)
		return nil
	}
	if err := os.Remove(filepath.Join(proofDir(), rel)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

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
	rel, ok := proofRelPath(p.StoredName)
	if !ok {
		common.SysError("qianye/withdraw: 凭证元数据里的文件名形状非法: " + p.StoredName)
		respondErr(c, errProofNotFound)
		return
	}
	full := filepath.Join(proofDir(), rel)
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		// 库里有行、磁盘上没有文件:多节点各存各的时最常见的一种表现,
		// 也可能是落盘失败留下的残行。不回 500 —— 它不是服务端故障。
		respondErr(c, errProofPurged)
		return
	}
	c.Header("Content-Type", p.MimeType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Disposition", `inline; filename="`+proofDownloadName(p)+`"`)
	c.File(full)
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
