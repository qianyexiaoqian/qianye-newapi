package lottery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/imagestore"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// cover.go —— 活动卡片背景图。
//
// # 两种来源,一条落盘管线
//
// 需求原话是「可以填写图片地址,或者 base64 装一下保存在服务器上。支持上传」。
// 落地成两列互斥的来源:
//
//	cover_url  外链地址(http/https)。平台一个字节都不存,浏览器直接加载。
//	cover_ref  上传的图。**落盘,库里只存引用。**
//
// **base64 不进数据库列**,这是刻意的取舍而不是偷懒:大厅列表是抽奖的首屏,
// 每页要读回二十行活动,一列几百 KB 的字符串会被完整读出、序列化、gzip、发走,
// 而那张图在没被渲染出来之前谁也不看。同一笔账 withdraw/model.go 的 Proof
// 与 ticket 的 Attachment 已经各算过一次,结论一致 —— 图片是只在展示那一刻
// 读一次的冷数据,放进行里付的是全链路的热成本。理解成"上传的图保存在服务器上"
// 而不是"把 base64 串塞进表里",与需求原文并不冲突:图确实存在服务器上。
//
// # 为什么不写第二份上传管线
//
// 落盘、魔数判定、服务端生成文件名、分片目录、MaxBytesReader 前置截断、
// O_EXCL、nosniff 下载 —— 这一整套住在 qianye/service/imagestore 里,
// 提现凭证与工单截图已经在用。那个包的包注释逐条列了漏掉任何一条的后果。
// 本文件只负责 imagestore 无从知道的三件事:落在哪个目录、元数据存哪张表、
// 谁有权取回。
//
// # 与工单那一套的**唯一**实质差别:回收时机
//
// 工单的图挂在 append-only 的消息上,一张图一旦提交就不会被换掉,所以它的
// 回收只有两条口径(从没提交的孤儿、工单关闭超过保留期)。封面是**可替换的**:
// 换一张图、改成外链、清空,都会让上一张当场变成垃圾。所以这里多一列
// detached_at,并且多一条"活动已被删除"的口径 —— 后者用 NOT EXISTS 判,
// 而不是在删除活动的那段代码里手写一句 UPDATE:那样每一条新的删除路径
// (今天是管理端接口,明天可能是 DBA 手工清理)都要记得再补一次。

const (
	// coverDirName 是落盘目录名,与配置文件同级(即 Docker/本地下的 data/)。
	coverDirName = "qy-lottery-covers"

	// coverOrphanSeconds 是"这张图已经没人用了"到"从磁盘上删掉"之间的宽限期。
	//
	// 固化在代码里而不是新增配置项:它的语义完全依附于"管理员建一场活动要多久"
	// 与"浏览器手里那份缓存还能被用多久",24 小时对两者都绰绰有余,单独放出来
	// 只会变成又一个"定义了却没人调"的旋钮(config/selfcheck.go 里记着的四次翻车)。
	coverOrphanSeconds = 24 * 3600

	// coverPendingMax 是单个管理员同时挂着的未绑定上传数上限。
	//
	// 这道闸必须存在:没有它,一个管理员账号(或一把被盗的管理员令牌)可以用
	// "只传图、不保存活动"把磁盘打满,而本模块其余全部闸门 —— 活动数上限、
	// 奖品总额、关键操作限流 —— 一个都拦不到这条路径,因为它压根没有创建活动。
	//
	// 取 10 而不是从别的配置派生:与工单不同,这里没有一个"单条消息几张图"的
	// 上限可依附 —— 一场活动的封面永远只有一张。10 张的余量足够覆盖
	// "选了又换"的正常操作,再多只说明有人在拿它当图床。
	coverPendingMax = 10

	// coverURLMaxRunes 是外链地址的长度上限,与 cover_url 列宽(varchar(500))
	// 对齐。按 rune 计而不是字节:超出列宽的截断在 MySQL 上是静默的
	// (非严格模式),截断之后那个地址一定是坏的,而没有任何一处会报错。
	coverURLMaxRunes = 500
)

// coverStore 是活动封面的落盘目录。与工单/提现各持一个,互不干扰。
var coverStore = imagestore.New(coverDirName)

// CoverAcceptMimes 供管理端配置接口下发给前端做 accept 属性。
// 前端的 accept 只是体验,真正的判定是魔数。
func CoverAcceptMimes() []string { return imagestore.AcceptMimes() }

// Cover 是一张活动封面的元数据。图片本体在【本地磁盘】上,本表只存指针。
//
// 已知限制:多节点部署时本地磁盘各存各的,A 节点收到的上传 B 节点取不到。
// 单节点部署无碍;多节点需要共享存储或后续接对象存储。与工单截图、提现凭证
// 同一条约束,写在配置注释里。
type Cover struct {
	Id int64 `json:"-" gorm:"primaryKey;autoIncrement"`
	// Ref 是对外唯一标识。绝不下发自增 id —— 它可枚举。
	Ref string `json:"ref" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_lot_cov_ref"`
	// UserId 是**上传者**(只可能是管理员:上传接口挂在 AdminAuth 之下)。
	// 认领时会带上它:一个管理员不能把另一个管理员正在挑的图抢过来用。
	UserId int `json:"-" gorm:"not null;index:idx_qy_lot_cov_user,priority:1"`

	// ActId = 0 表示"传了但还没落到任何一场活动上"。孤儿回收扫的正是这个条件 ——
	// 管理员选完图关掉向导是常态,不回收就是一个只增不减的磁盘泄漏。
	ActId int64  `json:"-" gorm:"not null;default:0;index:idx_qy_lot_cov_act"`
	ActNo string `json:"-" gorm:"type:varchar(32);not null;default:''"`

	// StoredName 是【服务端生成】的落盘文件名(32 位十六进制 + 白名单扩展名)。
	// 用户提供的文件名从不落盘、也从不拼进路径:那是路径穿越最短的一条路。
	StoredName string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// MimeType 由【魔数】判定,不信扩展名,也不信 Content-Type 请求头。
	MimeType string `json:"mime_type" gorm:"type:varchar(32);not null;default:''"`
	Size     int64  `json:"size" gorm:"not null;default:0"`
	// Sha256 用于事后自证"取回的与当初上传的是同一张图",也顺带能发现重复上传。
	Sha256 string `json:"-" gorm:"type:varchar(64);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_lot_cov_user,priority:2"`
	BoundAt   int64 `json:"-" gorm:"not null;default:0"`
	// DetachedAt 是"被换下来"的时刻(0 = 还在用)。工单的附件没有这一列,
	// 因为消息是 append-only 的;封面可以被换成另一张、换成外链、或者清空,
	// 那一刻旧的这张就成了垃圾,而它的 act_id 曾经是有值的 —— 只靠
	// "act_id = 0" 那条孤儿口径永远扫不到它。
	DetachedAt int64 `json:"-" gorm:"not null;default:0;index:idx_qy_lot_cov_purge,priority:1"`
	// PurgedAt 是文件已从磁盘删除的时间戳(0 = 文件还在)。元数据行留着:
	// 它是"这个 ref 确实存在过、已按规则回收"的答案,与"从来没有这个 ref"
	// 是两种不同的回答,而后者会被拿去枚举。
	PurgedAt int64 `json:"-" gorm:"not null;default:0;index:idx_qy_lot_cov_purge,priority:2"`
}

// coverPendingGate 是"每个管理员的待挂封面上限"这道闸门的锚点行键前缀
// (见 qymodel.LockGate)。
const coverPendingGate = "lottery:cover_pending:"

func (Cover) TableName() string { return "qy_lot_covers" }

// ─────────────────────────── 错误 ───────────────────────────

var (
	errCoverDisabled = newBizError(http.StatusBadRequest, "qy_lot_cover_disabled",
		"当前站点未开放上传封面,可以改填一个图片地址")
	errCoverRequired = newBizError(http.StatusBadRequest, "qy_lot_cover_required",
		"请选择要上传的图片")
	errCoverTooLarge = newBizError(http.StatusRequestEntityTooLarge, "qy_lot_cover_too_large",
		"封面图片超出大小上限")
	errCoverType = newBizError(http.StatusBadRequest, "qy_lot_cover_type",
		"只接受 JPEG / PNG / WebP 图片")
	errCoverNotFound = newBizError(http.StatusBadRequest, "qy_lot_cover_not_found",
		"封面图片不存在或已被使用")
	// errCoverPurged 与 errCoverNotFound 分开:前者是"确实传过,已按规则回收",
	// 后者是"没有这个 ref"。糊成一句会让运营以为自己传错了。
	errCoverPurged = newBizError(http.StatusGone, "qy_lot_cover_purged",
		"封面图片已被回收")
	errCoverPending = newBizError(http.StatusConflict, "qy_lot_cover_pending_limit",
		"还有多张已上传但未使用的封面,请先保存活动或移除它们")
	errCoverStore = newBizError(http.StatusInternalServerError, "qy_lot_cover_store_failed",
		"封面保存失败,请稍后重试")
	// errCoverBothSources 是配置事故的第一现场:两种来源同时给了值时,
	// 到底显示哪一张只能靠代码里的先后顺序回答 —— 而那是最坏的一种规则来源。
	errCoverBothSources = newBizError(http.StatusBadRequest, "qy_lot_cover_both_sources",
		"图片地址与上传的封面只能二选一")
)

// errCoverBadURL 把"这个地址为什么不行"如实说出来。
//
// 不复用 errBadRequest 的通用文案:外链被拒的原因有四种(协议、长度、
// 没有主机名、带了账号密码),而运营看到的如果只是"请求参数不合法",
// 他会去改别的字段。
func errCoverBadURL(why string) *bizError {
	return newBizError(http.StatusBadRequest, "qy_lot_cover_bad_url", "图片地址"+why)
}

// ─────────────────────────── 外链地址 ───────────────────────────

// normalizeCoverURL 校验并归一化一个外链封面地址。空串合法(表示不设外链)。
//
// # 服务端**从不去拉取**这个地址
//
// 它原样下发给前端,由浏览器直接加载。因此这条路上**没有 SSRF 面** ——
// 没有任何一处服务端代码会拿着它去发请求,也刻意不做"预览取图""探测尺寸"
// "生成缩略图"这类会把它变成 SSRF 的功能。将来若真要加服务端取图,
// 那一刻必须补上完整的 SSRF 防护(解析后校验 IP 段、禁重定向、限时限长),
// 而不是在这里放一句注释就算数。
//
// # 四条判定,以及各自挡住什么
//
//	长度      超过列宽会在 MySQL 非严格模式下被静默截断,截断后的地址一定是坏的
//	协议      只认 http/https。javascript:/data:/file: 一律拒 —— 虽然现代浏览器
//	          不会执行 <img src="javascript:">,但这个字段将来可能被放进 <a href>
//	          或 CSS background-image,那时"当初校验过协议"是唯一还在起作用的东西
//	主机名    没有 Host 的相对地址会在不同页面下解析成不同的东西
//	账号密码  形如 https://user:pass@host/x 的地址会把凭据画进每一张卡片,
//	          也是钓鱼链接最常见的伪装
//
// # 为什么**不**限制域名
//
// 域名白名单在这里是一个必然空着的旋钮:运营用的是七零八落的图床与 CDN,
// 没人能提前枚举它们,于是它会被配成"全放行"或者干脆没人配 —— 那正是
// config/selfcheck.go 记着的四次翻车的形状。真正的风险(把访客的 IP 与
// Referer 泄漏给第三方)由前端那一侧的 referrerPolicy="no-referrer" 处理,
// 而设这个地址的人本来就是管理员 —— 他能做的比这危险得多。
func normalizeCoverURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if utf8.RuneCountInString(s) > coverURLMaxRunes {
		return "", errCoverBadURL(fmt.Sprintf("不能超过 %d 个字符", coverURLMaxRunes))
	}
	// 控制字符(含换行与制表)会在日志、CSV 导出与 HTTP 头里造成注入形状,
	// 而一个合法的 URL 里本来就不该有它们。
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", errCoverBadURL("不能包含控制字符")
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errCoverBadURL("格式不合法")
	}
	// url.Parse 已经把 scheme 转成小写,这里比小写字面量是完整的。
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errCoverBadURL("只支持 http:// 或 https:// 开头的地址")
	}
	if u.Host == "" {
		return "", errCoverBadURL("缺少主机名")
	}
	if u.User != nil {
		return "", errCoverBadURL("不能带账号密码")
	}
	return s, nil
}

// ─────────────────────────── 上传 ───────────────────────────

// coverMaxBytes 是本次上传的字节上限。
//
// 两道:配置值,以及 imagestore 的硬上界。第二道不是多余的 —— 配置可以热更新,
// 而校验器只在启动时跑过一次。
func coverMaxBytes() int64 {
	n := config.Get().Lottery.CoverMaxBytes
	if n <= 0 || n > config.MaxLotteryCoverBytes {
		return config.MaxLotteryCoverBytes
	}
	return n
}

// acceptCoverUpload 落盘一张封面并登记元数据。
//
// 顺序是【先写库行、再写文件】。反过来的话,写完文件而插库失败会留下一个
// 没有任何行指向它的孤儿文件 —— 磁盘上永远清不掉,因为回收任务是按库行扫的。
// 现在这个方向最坏只会留下一行指向不存在文件的元数据,而取回与回收都能优雅处理。
func acceptCoverUpload(c *gin.Context, adminId int) (*Cover, error) {
	if !config.Get().Lottery.CoverOn() {
		return nil, errCoverDisabled
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}

	data, kind, err := imagestore.Accept(c, "file", coverMaxBytes())
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

	// 计数与插入同事务,而且计数必须**带锁**。
	//
	// 只做到"同事务"是不够的:MySQL 默认 REPEATABLE READ,并发上传的每个事务
	// 在自己的快照里读计数,同时读到旧值、同时通过,实测直接超配额。带上
	// FOR UPDATE 之后这条 SELECT 在 (user_id, created_at) 索引上取的是 next-key
	// 锁,并发的第二个事务会在这里等,而不是各读各的快照 —— 而这是"一个账号
	// 用只传图不保存把磁盘打满"唯一的总量闸,它被绕过的后果是不可回收的磁盘占用。
	//
	// 闸门靠**锚点行锁**串行化,不是给 COUNT 挂 FOR UPDATE。
	// 后者只在 MySQL 的 next-key 锁下成立:PostgreSQL 会直接报
	// "FOR UPDATE is not allowed with aggregate functions"(上传整条失败),
	// 而 SQLite 上 FOR UPDATE 被驱动整段丢弃(闸门形同虚设)。
	// 锚点按管理员分键 —— 闸门本来就是每人一份,共用一把会让两个管理员互相排队。
	err = gdb.Transaction(func(tx *gorm.DB) error {
		if err := qymodel.LockGate(tx, coverPendingGate+strconv.Itoa(adminId)); err != nil {
			return err
		}
		var cnt int64
		if err := tx.Model(&Cover{}).
			Where("user_id = ? AND act_id = 0 AND detached_at = 0 AND purged_at = 0", adminId).
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
		// 文件没写成,这一行就是纯垃圾。删不掉也不要紧:它的 act_id 恒为 0,
		// 孤儿回收会在窗口到期后连同"文件本就不存在"一起收掉。
		if delErr := gdb.Where("id = ?", row.Id).Delete(&Cover{}).Error; delErr != nil {
			db.MarkFailure(delErr)
		}
		common.SysError("qianye/lottery: 封面落盘失败: " + err.Error())
		return nil, errCoverStore
	}
	return row, nil
}

// ─────────────────────────── 绑定与解绑 ───────────────────────────

// bindCover 把一场活动的封面从 oldRef 切到 newRef。**必须在调用方的事务里。**
//
// 认领走的是一条带条件的 UPDATE,而不是"先查出来看看是不是待用的、再更新":
// 先读后写在并发下会让同一张图被两场活动同时认领,而它们随后都以为自己独占它 ——
// 其中一场换图时会把另一场的封面一起弄没。四个条件缺一不可:
//
//	ref                          这一张
//	user_id                      只能认领自己传的那一张(管理员之间也不共享待用上传)
//	act_id = 0 OR detached_at>0  必须**当前没有任何活动在用它**
//	purged_at = 0                已回收的图不能再被引用(它在磁盘上已经不存在了)
//
// 第三条刻意不是单纯的 act_id = 0。被换下来的那一张只打 detached_at、不清
// act_id(清了它就落不进回收任务那条"活动已被删除"的口径),于是只判 act_id
// 会让一张**此刻谁都没在用**的图永远认领不回来 —— 包括换错了想换回原来那一张,
// 而运营收到的文案是"封面图片不存在或已被使用"。两条合起来才是"没人在用"。
//
// 被换下来的那一张**只打 detached_at,不删行也不删文件**:删文件要放在事务外
// (磁盘操作不参与回滚),而一次回滚之后活动行上仍然指着它,文件却已经没了。
// 交给回收任务在宽限期之后统一删,是唯一不会出现"库里有、盘上没有"的顺序。
func bindCover(tx *gorm.DB, actId int64, actNo, oldRef, newRef string, adminId int) error {
	if newRef == oldRef {
		return nil
	}
	if newRef != "" {
		res := tx.Model(&Cover{}).
			Where("ref = ? AND user_id = ? AND purged_at = 0 AND (act_id = 0 OR detached_at > 0)",
				newRef, adminId).
			Updates(map[string]any{
				"act_id":      actId,
				"act_no":      actNo,
				"bound_at":    common.GetTimestamp(),
				"detached_at": 0,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errCoverNotFound
		}
	}
	if oldRef != "" {
		res := tx.Model(&Cover{}).
			Where("ref = ? AND act_id = ? AND purged_at = 0", oldRef, actId).
			Updates(map[string]any{"detached_at": common.GetTimestamp()})
		if res.Error != nil {
			return res.Error
		}
	}
	return nil
}

// discardPendingCover 丢弃一张【自己上传且尚未落到任何活动上】的封面。
//
// 没有它,管理员在向导里换一张图只会把本地那一项换掉,服务端那条未绑定的行
// 会一直占着 coverPendingMax 的名额 —— 十次"选了又换"之后他在 24 小时宽限期
// 到期之前再也传不了图,而收到的提示是"请先保存活动",一个他此刻无法执行的动作。
//
// 三个条件全写进 WHERE:只能丢自己的、已经用在某场活动上的不能丢
// (那会让一场活动的封面凭空消失)、已回收的行留着当答案。
//
// 先删库行、再删文件:反过来的话,文件删了而库行还在,取回接口会把一张
// "存在但打不开"的图报成已回收;而现在这个方向最坏留下一个没有行指向的文件,
// 由下面那次 Remove 负责,失败也只是多占一份磁盘并留日志。
func discardPendingCover(adminId int, ref string) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	var row Cover
	err := gdb.Where("ref = ? AND user_id = ? AND act_id = 0 AND purged_at = 0", ref, adminId).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errCoverNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return wrapInternal("读取封面", err)
	}
	res := gdb.Where("id = ? AND act_id = 0 AND purged_at = 0", row.Id).Delete(&Cover{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return wrapInternal("删除封面", res.Error)
	}
	if res.RowsAffected == 0 {
		// 这一瞬间它被另一个请求认领到某场活动上了。
		return errCoverNotFound
	}
	if err := coverStore.Remove(row.StoredName); err != nil {
		common.SysError("qianye/lottery: 丢弃封面时删除文件失败: " + err.Error())
	}
	return nil
}

// ─────────────────────────── 取回 ───────────────────────────

// loadCover 按 ref 取一行元数据。**不在这里判 purged_at**:
// "已回收"与"没有这个 ref"是可区分的两个回答,判定属于调用方。
func loadCover(ref string) (*Cover, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var row Cover
	err := gdb.Where("ref = ?", ref).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errCoverNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("读取封面", err)
	}
	return &row, nil
}

// serveCover 把封面回给调用者。
//
// 缓存口径是 CachePublicImmutable 而不是工单/凭证那档 private, no-store:
// 封面对所有人是同一份,而且一个 ref 对应的字节永远不变(落盘名由 crypto/rand
// 生成、Write 带 O_EXCL,覆盖不可能发生)。配成 no-store 的后果是每进一次大厅
// 就把首屏那十几张图重新拉一遍。
func serveCover(c *gin.Context, row *Cover) {
	full, err := coverStore.Locate(row.StoredName)
	if errors.Is(err, imagestore.ErrMalformedName) {
		common.SysError("qianye/lottery: 封面元数据里的文件名形状非法: " + row.StoredName)
		respondErr(c, errCoverNotFound)
		return
	}
	if err != nil {
		// 库里有行、磁盘上没有文件:多节点各存各的时最常见的一种表现,
		// 也可能是落盘失败留下的残行。不回 500 —— 它不是服务端故障。
		respondErr(c, errCoverPurged)
		return
	}
	imagestore.Serve(c, full, row.MimeType, coverDownloadName(row), imagestore.CachePublicImmutable)
}

// coverDownloadName 是回给浏览器的文件名。
// 由 ref + 服务端扩展名拼出,永远不含用户提供的任何字符串。
func coverDownloadName(row *Cover) string {
	_, ext, _ := strings.Cut(row.StoredName, ".")
	return "lottery-cover-" + row.Ref + "." + ext
}

// ─────────────────────────── 回收 ───────────────────────────

// coverPruneBatch 是每一轮扫描的处理条数上限。固化在代码里:它只影响
// "一轮干多少活",而任务每小时跑一次、积压会在下一轮继续消化。
const coverPruneBatch = 200

// pruneCovers 清理不该再留在磁盘上的封面。
//
// 三条到期口径,每一条对应一种真实发生过的"图还在、没人用"的形状:
//
//	A 从未使用   传了但从没落到任何活动上(管理员选完图关掉向导),超过宽限期
//	B 已被换下   活动换了封面 / 改成外链 / 清空,detached_at 超过宽限期
//	C 活动没了   活动行被删掉,而封面行还指着它
//
// C 用 NOT IN 子查询判,而不是在删除活动的那段代码里补一句 UPDATE:
// 删除路径不止一条(今天是管理端接口,明天可能是 DBA 手工清理或一次迁移),
// 每多一条都要记得再补一次,而漏掉的那一次是**永久**的磁盘泄漏 ——
// 回收任务按库行扫,一个没有任何行指向的文件谁也找不到。
//
// 到期判定一律写进【扫描条件】而不是取回来再过滤:一批永远不可清的行
// (比如正在用的封面)会把每一轮 batch 占满,后面所有到期行从此再也轮不到。
//
// **已经用在活动上的封面没有保留期**。活动表是永不清理的证据表,一场三年前的
// 抽奖今天仍然能被翻出来看;把它的封面按时间清掉,只会让历史页上多一个破图。
func pruneCovers(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	handle := db.Get()
	if handle == nil {
		return
	}
	// 句柄必须接上租约给的 ctx:租约到期或进程收到关停信号时,正在跑的那条
	// UPDATE 要跟着被取消,否则一个没有上界的回收批次会把关停拖到超时。
	gdb := handle.WithContext(ctx)
	cutoff := common.GetTimestamp() - coverOrphanSeconds

	purgeCoverBatch(ctx, gdb, gdb.Model(&Cover{}).
		Where("purged_at = 0 AND act_id = 0 AND detached_at = 0 AND created_at > 0 AND created_at < ?",
			cutoff), "从未使用的封面")

	purgeCoverBatch(ctx, gdb, gdb.Model(&Cover{}).
		Where("purged_at = 0 AND detached_at > 0 AND detached_at < ?", cutoff), "已被换下的封面")

	live := gdb.Model(&Activity{}).Select("id")
	purgeCoverBatch(ctx, gdb, gdb.Model(&Cover{}).
		Where("purged_at = 0 AND act_id > 0 AND act_id NOT IN (?)", live), "活动已删除的封面")
}

// purgeCoverBatch 执行一轮"取一批 → 删文件 → 标记已清"。
//
// 先删文件再标记,不是反过来:标记完再删,中途崩溃会留下一个 purged_at 已置位
// 却仍躺在磁盘上的文件 —— 它从此不会再被任何一轮扫到,回收任务在日志里一切正常,
// 而图片永远留在盘上。删文件失败的那一行则原样留着,下一轮再试。
func purgeCoverBatch(ctx context.Context, gdb *gorm.DB, scope *gorm.DB, what string) {
	if ctx.Err() != nil {
		return
	}
	rows := make([]Cover, 0, coverPruneBatch)
	if err := scope.Order("id asc").Limit(coverPruneBatch).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/lottery: 扫描" + what + "失败: " + err.Error())
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
			common.SysError("qianye/lottery: 删除" + what + "文件失败(下一轮重试): " + err.Error())
			continue
		}
		done = append(done, rows[i].Id)
	}
	if len(done) == 0 {
		return
	}

	// 再带一次 purged_at = 0:两个节点的租约交接窗口里可能重叠执行一轮,
	// 条件写在 UPDATE 上才不会把 purged_at 刷成第二个时间戳。
	res := gdb.Model(&Cover{}).Where("id IN ? AND purged_at = 0", done).
		Updates(map[string]any{"purged_at": common.GetTimestamp()})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/lottery: 标记" + what + "已回收失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/lottery: 已回收 %d 张%s", res.RowsAffected, what))
	}
}
