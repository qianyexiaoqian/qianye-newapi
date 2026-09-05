package apiaddr

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/qianye/groupname"
)

// 长度上限。都比对应列宽小一截,留给多字节字符 —— varchar(64) 是 64 个字符,
// 而这里量的是 rune 数,64 个中文在 utf8mb4 下是 192 字节,列宽是够的。
const (
	maxNameRunes  = 32
	maxRemarkRune = 80
	maxURLLen     = 256

	// maxAddresses 是整表行数上限。
	//
	// 它不是性能约束(几百行谁都扛得住),而是让"整表重排"与"整表下发"的输入
	// 天然有界。最初定 30 的理由是用户侧选择弹窗的可用性;分组可见与展示位置
	// (surfaces)落地之后,单个用户在单个位置实际看到的只是过滤后的子集,
	// 密钥页复制条的桌面卡片还有自己的展示上限 —— 表的总量放到 100
	// (2026-08-30 项目方要求)。
	maxAddresses = 100

	// sortStep 是重排时相邻两行的序号间隔。
	//
	// 留间隔而不是直接用 0,1,2,…:将来若要支持"插到某两条之间"而不重排整表,
	// 中间有空位可用。现在整表重排用不到,但代价是零。
	sortStep = 10

	// 「适用分组」名单的三道上限,与 transfer 的规则表同款口径:
	// 单名上限对齐 users.group 的列宽 varchar(64),名单条数与总长挡住
	// "把一整页分组名粘进来"这类误操作 —— 总长按 rune 计,列宽 varchar(1024)
	// 在 utf8mb4 下量的也是字符数。
	maxUserGroupRunes   = 64
	maxUserGroupEntries = 64
	maxUserGroupsRunes  = 1024
)

// normalizeName 校验并归一化地址名称。
func normalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errNameRequired
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", errNameTooLong
	}
	return name, nil
}

// normalizeRemark 校验并归一化备注。空是合法的。
func normalizeRemark(raw string) (string, error) {
	remark := strings.TrimSpace(raw)
	if utf8.RuneCountInString(remark) > maxRemarkRune {
		return "", errRemarkTooLong
	}
	return remark, nil
}

// normalizeURL 校验并归一化 API 地址。
//
// # 为什么必须在后端归一化,而不是"前端填什么存什么"
//
// 这个值会被原样拼进剪贴板里的连接信息 JSON,由用户粘进桌面客户端。
// 三件事必须在这里挡住:
//
//  1. **scheme 白名单**。`url.Parse` 对 `javascript:…` / `data:…` 一样解析成功,
//     而这个字符串在管理端列表里是要被渲染成可点链接的。只放行 http/https。
//     顺带也挡住 `//evil.com` 这种协议相对写法(Scheme 为空)。
//  2. **不许带凭据**。`https://user:pass@host` 是合法 URL,存进来就是把一份
//     密码明文放进一张会被完整下发给所有登录用户的表里。
//  3. **不许带查询串/片段**。这里要的是"服务端点",不是一次具体请求;
//     `?key=sk-…` 这种误粘贴同样是凭据泄漏,而且客户端拼路径时会拼出坏地址。
//
// 归一化只做一件事:去掉路径尾部的斜杠。客户端拼 `/v1/...` 时自带前导斜杠,
// 存着尾斜杠会拼出 `https://a.com//v1`。
func normalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errURLRequired
	}
	if len(s) > maxURLLen {
		return "", errURLTooLong
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errURLInvalid
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errURLScheme
	}
	if u.User != nil {
		return "", errURLCredentials
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errURLExtra
	}
	if u.Host == "" {
		return "", errURLInvalid
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return u.Scheme + "://" + u.Host + path, nil
}

// 展示位置的合法取值。surfaceConsole 是控制台「API信息」卡片,surfacePicker
// 是密钥页的「复制链接信息 / CC Switch」(复制条与选择弹窗同源)。
//
// 白名单而不是自由串:这个值参与服务端过滤(handleUserList),一个写错的
// 位置名不会报错,只会让这条地址在所有位置都可见或都不可见 —— 取决于比较的
// 方向,而两种都查不出来。
const (
	surfaceConsole = "console"
	surfacePicker  = "picker"
)

var allowedSurfaces = map[string]bool{
	surfaceConsole: true,
	surfacePicker:  true,
}

// normalizeSurfaces 校验并归一化「展示位置」名单:逗号拆分、去空白、折叠
// 大小写、去重、白名单校验,再拼回逗号分隔的存储形态。空串合法 —— 含义是
// 「所有位置可见」,也是存量行与不填时的默认(与 UserGroups 同口径)。
func normalizeSurfaces(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	tokens := strings.Split(raw, ",")
	norm := make([]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		name := strings.ToLower(strings.TrimSpace(tok))
		if name == "" {
			continue
		}
		if !allowedSurfaces[name] {
			return "", errSurfaceInvalid
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		norm = append(norm, name)
	}
	return strings.Join(norm, ","), nil
}

// visibleOnSurface 判断一条地址在给定展示位置是否可见。surface 为空 =
// 调用方不做位置过滤(旧前端、管理端 curl),全部放行。
func visibleOnSurface(surfaces, surface string) bool {
	if surface == "" || strings.TrimSpace(surfaces) == "" {
		return true
	}
	for _, tok := range strings.Split(surfaces, ",") {
		if strings.ToLower(strings.TrimSpace(tok)) == surface {
			return true
		}
	}
	return false
}

// allowedColors 是「颜色」字段的白名单,与前端新建/编辑弹窗的调色板同一份
// 清单(上游「API信息」面板的 14 色)。白名单而不是自由串:这个值会被前端
// 原样拿去查样式表(getBgColorClass 查不到会兜底成默认色,存一个永远走兜底
// 的值没有意义),也会进审计快照。
var allowedColors = map[string]bool{
	"blue": true, "green": true, "cyan": true, "purple": true,
	"pink": true, "red": true, "orange": true, "amber": true,
	"yellow": true, "lime": true, "teal": true, "indigo": true,
	"violet": true, "slate": true,
}

// normalizeColor 校验并归一化颜色。空是合法的 —— 含义是"用前端默认色",
// 也是存量行经 AutoMigrate 补列后的值。折叠大小写与去空白在写入侧做,
// 判定侧(前端查表)拿到的永远是规范形。
func normalizeColor(raw string) (string, error) {
	color := strings.ToLower(strings.TrimSpace(raw))
	if color == "" {
		return "", nil
	}
	if !allowedColors[color] {
		return "", errColorInvalid
	}
	return color, nil
}

// normalizeUserGroups 校验并归一化「适用分组」名单:按逗号拆分、去空白、
// 折叠大小写、去重,再拼回逗号分隔的存储形态。空串是合法的 —— 含义是
// 「所有分组可见」,也是存量行与不填时的默认。
//
// 折叠必须发生在**写入侧**且与判定侧(visibleToUserGroup)同口径:存了 "VIP"
// 而判定拿 "vip" 来比,这条地址就对谁都不可见,管理端却看着配得明明白白。
//
// 分组名里禁空白与分号:名单存成逗号分隔的一列,一个自带分隔符气质的名字
// 在任何一次"按分隔符拆开"时都会裂成两条(transfer 的 checkGroupToken 同款
// 判据)。逗号不必单独禁 —— 它本身就是拆分符,拆完的 token 里不可能再有。
func normalizeUserGroups(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	tokens := strings.Split(raw, ",")
	norm := make([]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		name := groupname.Normalize(tok)
		if name == "" {
			// 空 token 是"vip,,svip"或行尾多打了个逗号,不值得报错打断输入。
			continue
		}
		if strings.ContainsAny(name, " \t\r\n;") {
			return "", errGroupInvalid
		}
		if utf8.RuneCountInString(name) > maxUserGroupRunes {
			return "", errGroupTooLong
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		norm = append(norm, name)
	}
	if len(norm) > maxUserGroupEntries {
		return "", errGroupsTooMany
	}
	joined := strings.Join(norm, ",")
	if utf8.RuneCountInString(joined) > maxUserGroupsRunes {
		return "", errGroupsTooLong
	}
	return joined, nil
}

// visibleToUserGroup 判断一条地址对给定用户分组是否可见。
//
// 两侧都归一后精确比较:userGroup 走 Effective —— 历史行与被直接改过库的账号
// 会把 users.group 留成空串,而它们在业务上就是默认分组的用户(见 groupname
// 包注释);名单侧建行时已 Normalize 过,这里再过一遍只为容忍手工改库写进来
// 的旧格式。
func visibleToUserGroup(userGroups, userGroup string) bool {
	if strings.TrimSpace(userGroups) == "" {
		return true
	}
	target := groupname.Effective(userGroup)
	for _, tok := range strings.Split(userGroups, ",") {
		if groupname.Normalize(tok) == target {
			return true
		}
	}
	return false
}

// normalizeOrderIds 校验整表重排的入参,返回去空白后的 id 序列。
//
// 只做"这串 id 本身合不合法"(非空、不超上限、无重复);"它是否恰好等于库里
// 当前的全集"必须在事务里对着真实数据判(见 applyOrder)——那是一个并发条件,
// 在这里判等于用一份读到的旧快照去证明另一份旧快照。
func normalizeOrderIds(ids []int) ([]int, error) {
	if len(ids) == 0 || len(ids) > maxAddresses {
		return nil, errInvalidParam
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, errInvalidParam
		}
		seen[id] = true
	}
	return ids, nil
}
