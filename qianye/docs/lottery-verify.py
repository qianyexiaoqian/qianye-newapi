#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""抽奖/竞猜公正性离线验证脚本(协议 lot-v1 / lot-v2)。

用法:
    curl 'https://<站点>/api/qy/lottery/public/<act_no>/proof?format=ndjson' > proof.ndjson
    python3 lottery-verify.py proof.ndjson

    # 只想知道"我为什么没中":
    python3 lottery-verify.py proof.ndjson --explain <你的 entry_no>

`format=ndjson` 下发的是**完整且自洽**的一份:第一行是文档头(承诺、名单哈希、
种子、结果……),之后每行一条参与记录。分页的 JSON 版只适合界面展示 ——
少一条链就断,而"断了"与"被篡改了"在验证结果上无法区分,所以本脚本对
不完整的输入一律**跳过**链与名单两步,绝不给一个可能是假的绿勾。

为了少一次踩坑,本脚本三种输入都收:
  · NDJSON(推荐,唯一能拿到全量条目的形式)
  · 裸 JSON 文档(界面上那一份,分页;超过一页时链与名单会被跳过)
  · 带 {success,message,data} 信封的 JSON(直接把接口响应存下来的那一份)

零依赖(只用标准库 hashlib / hmac / json)。**刻意不联网**:验证脚本一旦要
回站点取数据,"自己验"就退化成"让平台说它验过了"。

它能证明什么:
  1. 承诺没有被替换  —— 种子/条件/奖档/概率表/四个时刻/每一个影响结果的开关都没改过
  2. 名单没有被增删 —— 逐条哈希链连续,且每个参与者手里的回执都在链上
  3. 名单先于种子冻结 —— roster_hash 在揭示之前就已公开
  4. 中奖名单可复算 —— 用公开的种子重跑一遍,结果必须一模一样
  5. **概率制下"我为什么没中"同样可复算** —— 落选者与中奖者用的是同一组公开
     输入、走的是同一段代码,平台无法制造一个只有失败者看不到的暗门

它**不能**证明什么(诚实说明,不粉饰):
  - 竞猜的 win_opt_no 是不是符合外部事实。那是链下事实,任何密码学都证不了
    世界杯谁赢了。能做的只有:选项集合与费率在发布时进承诺、结果必须附证据、
    一经写入不可改。这把作弊面压缩到"一次性地公开撒谎"。
  - "同一个人的票标了同一个 user_ref"。ref_salt 永不公开(公开就能枚举反查
    全部参与者身份),因此它不进承诺原像。在 lot-v1 的名次制下这只影响
    allow_multi_win=false 那一条约束;在概率制下它**完全不影响任何判定**
    (概率制强制不去重),比 v1 还干净。
  - **文本奖(兑换码 / CDK)的具体内容**。它在开奖之后才由管理员填入,发布时
    根本不存在,因此没有任何承诺。本脚本能验的是"这一档发文本奖、叫什么、
    公开说明是什么、有几份"在发布时就钉死且事后没被改;至于"我拿到的那串码
    是不是当初承诺的那一串" —— 承诺那一刻它还不存在,所以不可验证。
    证据链里因此只有一个 fulfilled 布尔,**没有内容、也没有内容的哈希**
    (公开哈希等于给了离线爆破面,兑换码的熵撑不住)。
  - 数据库层的篡改无法被**阻止**,只能被**不可抵赖地检出**:平台改不掉已经
    分发给 N 个用户的报名回执,也改不掉揭示前已公开的 roster_hash。
  - 一场被取消/流局的抽奖里"管理员是不是看了结果才决定不开"。种子在取消后
    同样公开,所以本脚本会把**本应中奖的名单**算出来打印给你 —— 那是判断
    这件事的唯一材料,但判断本身要人来做。
"""

import hashlib
import hmac
import json
import sys

SEP = "\x1f"

# 概率的分母:全部概率都以百万分比(ppm)表达。
PPM_DEN = 1000000

# 这些结局意味着"没有开出结果,全额退款"。抽奖在这几种收场下 winners 恒为空,
# 拿它去和复算名单比对只会得到一个必然的 FAIL —— 而真实情况是平台已经全额
# 退款、行为完全诚实。
REFUND_OUTCOMES = {
    "cancelled", "void_min_entries", "void_deadline",
    "void_no_winner", "void_all_correct",
}

SUPPORTED_ALGOS = {"lot-v1", "lot-v2"}


def H(*parts):
    """所有哈希的统一形状:SHA-256(部件用 0x1F 连接)。"""
    return hashlib.sha256(SEP.join(parts).encode("utf-8")).hexdigest()


def b(v):
    return "true" if v else "false"


def d(v):
    return str(int(v))


def g(doc, key, default=0):
    """取一个可能被 omitempty 省掉的键。

    省略与零值在原像里的编码完全一致,所以取零值是安全的 —— 这一条必须成立,
    否则 JSON 体积优化会静默改变哈希。
    """
    v = doc.get(key)
    return default if v is None else v


# ─────────────────────────── spec 原像 ───────────────────────────

def spec_lines_v1(kind, spec):
    """lot-v1 的奖档/选项逐行编码。**已冻结,一个字节都不能改。**"""
    if kind == "draw":
        rows = sorted(spec, key=lambda s: g(s, "tier"))
        return [SEP.join([d(g(s, "tier")), g(s, "name", ""),
                          d(g(s, "amount_quota")), d(g(s, "count"))]) for s in rows]
    rows = sorted(spec, key=lambda s: g(s, "opt_no"))
    return [SEP.join([d(g(s, "opt_no")), g(s, "label", ""),
                      b(g(s, "is_catch_all", False))]) for s in rows]


def spec_lines_v2(kind, spec):
    """lot-v2 的奖档/选项逐行编码。

    奖档行多出六个分量:奖品类型、中奖概率、公开的文本说明,以及双色球的
    两个命中门槛与占池比例。**非双色球活动这些位恒为 0,但仍然占一个分量位**
    —— 少一个占位就等于允许管理员把一场普通抽奖悄悄改成双色球。

    竞猜的选项行在 v2 里没有变化(只有域前缀不同)。
    """
    if kind == "draw":
        rows = sorted(spec, key=lambda s: g(s, "tier"))
        return [SEP.join([
            d(g(s, "tier")), g(s, "name", ""), g(s, "prize_type", ""),
            d(g(s, "amount_quota")), d(g(s, "count")), d(g(s, "win_ppm")),
            g(s, "text_desc", ""),
            d(g(s, "red_match")), d(g(s, "blue_match")), d(g(s, "pool_share_bps")),
        ]) for s in rows]
    rows = sorted(spec, key=lambda s: g(s, "opt_no"))
    return [SEP.join([d(g(s, "opt_no")), g(s, "label", ""),
                      b(g(s, "is_catch_all", False))]) for s in rows]


def commit_hash_v1(p, seed):
    return H("qylot-commit-v1", p["act_no"], p["kind"], p["algo"], p["rules_hash"],
             p["spec_hash"], d(p["stake_quota"]), d(p["open_at"]), d(p["close_at"]),
             d(p["draw_at"]), d(p["settle_deadline"]), b(p["allow_multi_win"]),
             d(p["fee_bps"]), p["no_winner_policy"], d(p["min_entries_to_hold"]), seed)


def commit_hash_v2(p, seed):
    """lot-v2 的承诺原像:v1 的全部分量 + 定档方式 + 整段期次快照。"""
    return H("qylot-commit-v2", p["act_no"], p["kind"], p["algo"], p["rules_hash"],
             p["spec_hash"], d(p["stake_quota"]), d(p["open_at"]), d(p["close_at"]),
             d(p["draw_at"]), d(p["settle_deadline"]), b(p["allow_multi_win"]),
             d(p["fee_bps"]), p["no_winner_policy"], d(p["min_entries_to_hold"]),
             g(p, "draw_mode", ""), g(p, "series_no", ""), d(g(p, "issue_no")),
             d(g(p, "pool_seed_quota")), d(g(p, "pool_carry_quota")),
             d(g(p, "pool_open_quota")), d(g(p, "pool_share_bps")),
             d(g(p, "ball_red_pool")), d(g(p, "ball_red_pick")),
             d(g(p, "ball_blue_pool")), d(g(p, "ball_blue_pick")), seed)


def chain_next(algo, prev, p, e):
    if algo == "lot-v2":
        return H("qylot-chain-v2", prev, p["act_no"], d(e["seq"]), e["entry_no"],
                 e["user_ref"], d(g(e, "opt_no")), d(g(e, "amount")), g(e, "pick", ""))
    return H("qylot-chain-v1", prev, p["act_no"], d(e["seq"]), e["entry_no"],
             e["user_ref"], d(g(e, "opt_no")), d(g(e, "amount")))


def roster_hash(algo, p, rows):
    if algo == "lot-v2":
        lines = [SEP.join([e["entry_no"], e["user_ref"], d(g(e, "opt_no")),
                           d(g(e, "amount")), g(e, "pick", "")]) for e in rows]
        return H("qylot-roster-v2", p["act_no"], p["commit_hash"], d(len(rows)),
                 "\n".join(lines))
    lines = [SEP.join([e["entry_no"], e["user_ref"], d(g(e, "opt_no")),
                       d(g(e, "amount"))]) for e in rows]
    return H("qylot-roster-v1", p["act_no"], p["commit_hash"], d(len(rows)),
             "\n".join(lines))


# ─────────────────────────── 票面与摇号 ───────────────────────────
#
# 这一段在 v1 与 v2 里**逐字节相同**。这不是省事,而是一条主张:
# 概率制没有引入任何新的随机源,它只是换了一把尺子去读同一张票。

def ticket(final, act_no, entry_no):
    return hmac.new(bytes.fromhex(final),
                    SEP.join(["qylot-ticket-v1", act_no, entry_no]).encode("utf-8"),
                    hashlib.sha256).hexdigest()


def roll_ppm(tick):
    """把票面折算成六位摇号结果 r ∈ [0, 999999]。

    r = floor(前 64 位 × 10^6 / 2^64)。Python 原生大整数直接算,
    Go 是 bits.Mul64 取高 64 位,两者精确相等。

    取 64 位而不是全 256 位,是因为 r 必须是一个能念给用户听的数字:
    「你的摇号结果 384217,二等奖区间是 [1000, 11000)」是一句人话。
    代价是相对偏差 < 2^-44,可忽略且被公示。
    """
    if len(tick) < 16:
        return PPM_DEN
    return (int(tick[:16], 16) * PPM_DEN) >> 64


def bands_of(spec):
    """各档按 tier 升序累加 win_ppm,得到互不相交的左闭右开区间。

    返回 (bands, error)。累计超过 100% 直接判错 —— 那意味着两档的区间重叠,
    而"一张票同时中两档"在派奖层会静默丢掉第二个奖。**绝不猜一个解释。**
    """
    out, acc = [], 0
    for s in sorted(spec, key=lambda s: g(s, "tier")):
        ppm = int(g(s, "win_ppm"))
        if ppm < 0:
            return None, "第 %d 档的概率为负" % g(s, "tier")
        if acc + ppm > PPM_DEN:
            return None, "累计到第 %d 档已达 %d ppm(超过 100%%)" % (g(s, "tier"), acc + ppm)
        out.append({
            "tier": g(s, "tier"), "lo": acc, "hi": acc + ppm,
            "count": int(g(s, "count")), "amount": int(g(s, "amount_quota")),
            "prize_type": g(s, "prize_type", "quota") or "quota",
        })
        acc += ppm
    return out, None


def prize_shares(band, w):
    """某一档 W 个中签者各自拿多少。

    未超募时每人拿 amount;超募时按**逐笔向零截断 + 残差归最后一位**均分本档
    预算 —— 与竞猜奖池分配逐字节同一套口径,不新增任何舍入约定。

    为什么是均分而不是按票面顺序截断前 count 名:截断制下一张票的实际中奖概率
    是 win_ppm × min(1, count/W),依赖当期人数 —— 卡片上公示的"中奖概率 1%"
    在超募时就是假的。均分制下 P(命中) 严格等于公示值,浮动的是金额。

    文本奖不摊薄(兑换码劈不开),全部命中者都中,金额恒为 0。
    """
    if band["prize_type"] == "text":
        return [0] * w
    if w <= band["count"]:
        return [band["amount"]] * w
    budget = band["amount"] * band["count"]
    if budget < w:
        # fail-closed,与生产实现 prizeShares 同一个口径。
        #
        # 预算摊到人均不足 1 时,生产侧返回 ErrPoolNotConserved 并把整场挂起
        # ——那一场**根本没有开出去**。这里若照常算出一份"有人 0 元"的名单并判
        # PASS,验证器就把一个 fail-closed 的现场解释成了合法结果:用户会以为
        # 平台已正常开奖、金额本来就该是 0。同一份输入,两个实现必须给同一个结论。
        raise ValueError(
            "第 %s 档预算 %d 摊给 %d 人后有人分到 0 —— "
            "生产实现在这一步会拒绝开奖,这份证据链不该存在"
            % (band["tier"], budget, w))
    out, acc = [], 0
    for i in range(w):
        pay = (budget - acc) if i == w - 1 else budget // w
        acc += pay
        out.append(pay)
    return out


def ball_draw(final, act_no, color, pool_n, pick_k):
    """双色球摇号:给每个球号算一次 HMAC,按 (哈希, 号码) 排序取前 k。

    **零实现自由度**是这个算法唯一的理由。取模 + 撞重重取需要第三方精确复现
    计数器的推进规则、蓝球的起始下标、以及撞重时是否消耗随机数 —— 每一处
    细微差异都会摇出完全不同的号码,而验证者无法判断是谁错了。
    排序法任何语言十行以内,而且与名次制用的是同一个原语。

    平局用号码本身定死。升序返回。
    """
    if pool_n <= 0 or pick_k <= 0:
        return []
    pick_k = min(pick_k, pool_n)
    key = bytes.fromhex(final)
    hs = []
    for n in range(1, pool_n + 1):
        h = hmac.new(key, SEP.join(["qylot-ball-v2", act_no, color, d(n)]).encode("utf-8"),
                     hashlib.sha256).hexdigest()
        hs.append((h, n))
    hs.sort()
    return sorted(n for _, n in hs[:pick_k])


def split_pick(pick):
    """把选号串拆成红球与蓝球。格式:`03,05,12|08`。"""
    if "|" not in pick:
        return None, None
    red, blue = pick.split("|", 1)

    def group(raw):
        raw = raw.strip()
        if raw == "":
            return []
        return [int(x) for x in raw.split(",")]

    try:
        return group(red), group(blue)
    except ValueError:
        return None, None


def ball_tier_of(draw_reds, draw_blues, pick, tiers):
    """判定一张票中了哪一档。

    红蓝命中数都是**下界**,按 tier 升序命中即停 —— 一张票只中一档。
    返回 (tier, red_match, blue_match),tier == 0 表示未中奖。
    """
    my_reds, my_blues = split_pick(pick)
    if my_reds is None:
        return 0, 0, 0
    rm = len(set(draw_reds) & set(my_reds))
    bm = len(set(draw_blues) & set(my_blues))
    # 红蓝命中数都是**下界**,按 tier 升序命中即停 —— 一张票只中一档。
    for t in sorted(tiers, key=lambda s: g(s, "tier")):
        if rm >= int(g(t, "red_match")) and bm >= int(g(t, "blue_match")):
            return g(t, "tier"), rm, bm
    return 0, rm, bm


def ball_split_even(budget, hits):
    """把预算均分给中签者:逐笔向零截断,残差归 entry_no 字节序最大者。

    与竞猜奖池分配逐字节同一套口径,不新增任何舍入约定。
    """
    out = [0] * len(hits)
    if not hits:
        return out
    if budget < len(hits):
        # fail-closed,与生产实现 ballSplitEven 同一个口径(见 prize_shares 的理由)。
        raise ValueError(
            "预算 %d 摊给 %d 人后有人分到 0 —— "
            "生产实现在这一步会拒绝开奖,这份证据链不该存在" % (budget, len(hits)))
    base = budget // len(hits)
    out = [base] * len(hits)
    mx = max(range(len(hits)), key=lambda i: hits[i]["entry_no"])
    out[mx] += budget - base * len(hits)
    return out


def ball_winners(p, R):
    """双色球复算:先摇号,再逐票匹配定档。

    与概率制的区别在**随机量的作用域**:概率制是每张票各摇一次,
    双色球是全场共摇一次、一张票中不中由它自己选的号与开奖号的匹配数决定。
    随机源仍然是同一个 final_seed,只是换了一个域前缀。

    这也是为什么界面上那几颗球必须由你自己重摇一遍才算数:它们是**产生结果的
    原因**,而不是结果产生之后编出来的动画。
    """
    seed = p.get("seed", "")
    final = H("qylot-final-v1", p["act_no"], seed, p["roster_hash"],
              d(p["roster_count"]), p["algo"])
    reds = ball_draw(final, p["act_no"], "red", int(g(p, "ball_red_pool")),
                     int(g(p, "ball_red_pick")))
    blues = ball_draw(final, p["act_no"], "blue", int(g(p, "ball_blue_pool")),
                      int(g(p, "ball_blue_pick")))

    # 本期真正可派发的池子:开局基数(已进承诺)+ 本期投注入池部分。
    # 平台无法在这里报一个更大的数 —— 两项都是公开数据算出来的。
    pool_in = 0
    if int(g(p, "pool_share_bps")) > 0 and p.get("outcome", "") not in REFUND_OUTCOMES:
        pool_in = int(g(p, "pool_quota")) * int(g(p, "pool_share_bps")) // 10000
    pool_open = int(g(p, "pool_open_quota")) + pool_in

    tiers = sorted(p["spec"], key=lambda s: g(s, "tier"))
    by_tier = {}
    for e in R:
        tier, _, _ = ball_tier_of(reds, blues, g(e, "pick", ""), tiers)
        if tier:
            by_tier.setdefault(tier, []).append(e)

    expect, pos, total = [], 0, 0
    for t in tiers:
        hits = by_tier.get(g(t, "tier"), [])
        if not hits:
            continue
        share = int(g(t, "pool_share_bps"))
        try:
            if share > 0:
                amounts = ball_split_even(pool_open * share // 10000, hits)
            elif len(hits) <= int(g(t, "count")):
                amounts = [int(g(t, "amount_quota"))] * len(hits)
            else:
                amounts = ball_split_even(int(g(t, "amount_quota")) * int(g(t, "count")), hits)
        except ValueError as e:
            # fail-closed 与生产实现同口径:报成"复算失败"而不是抛栈,
            # 调用方据此返回退出码 1(FAIL),不是 0。
            return None, "奖级 %s:%s" % (g(t, "tier"), e), reds, blues
        for e, amount in zip(hits, amounts):
            total += amount
            expect.append((pos, g(t, "tier"), e["entry_no"], amount))
            pos += 1
    if total > pool_open:
        return None, "派出去的总额 %d 超过了本期池子 %d" % (total, pool_open), reds, blues
    return expect, None, reds, blues


def rank_winners(p, R, allow_multi_win, tick):
    """名次制(draw_mode=rank,也是 lot-v1 的唯一玩法)的复算。"""
    ranked = sorted(R, key=lambda e: (tick[e["entry_no"]], e["entry_no"]))
    if not allow_multi_win:
        seen, uniq = set(), []
        for e in ranked:
            if e["user_ref"] in seen:
                continue
            seen.add(e["user_ref"])
            uniq.append(e)
        ranked = uniq
    expect, i = [], 0
    for s in sorted(p["spec"], key=lambda s: g(s, "tier")):
        for _ in range(int(g(s, "count"))):
            if i >= len(ranked):
                break  # 票不够则该档空缺,绝不补抽
            expect.append((i, g(s, "tier"), ranked[i]["entry_no"], int(g(s, "amount_quota"))))
            i += 1
    return expect, None


def prob_winners(p, R, tick):
    """概率制(draw_mode=prob)的复算。

    **每一条 entry 都在这里被判定,落选者与中奖者走同一行代码。**
    这不是写法上的偏好:它是"平台无法制造一个只有失败者看不到的暗门"
    这条主张在代码里的落点。
    """
    bands, err = bands_of(p["spec"])
    if err is not None:
        return None, err

    hits = {}
    for e in R:
        r = roll_ppm(tick[e["entry_no"]])
        for band in bands:
            if band["lo"] <= r < band["hi"]:
                hits.setdefault(band["tier"], []).append(e)
                break
        # 落在全部区间之外 = 未中奖。这是一等公民结果,不是异常分支。

    expect, pos = [], 0
    for band in bands:
        h = hits.get(band["tier"], [])
        if not h:
            continue
        try:
            shares = prize_shares(band, len(h))
        except ValueError as e:
            # fail-closed 与生产实现 prizeShares 同口径:报成"复算失败"
            # 而不是抛栈,调用方据此返回退出码 1(FAIL),不是 0。
            return None, str(e)
        for e, amount in zip(h, shares):
            expect.append((pos, band["tier"], e["entry_no"], amount))
            pos += 1
    return expect, None


# ─────────────────────────── 输入 ───────────────────────────

def load(path):
    """读入证据链。NDJSON / 裸 JSON / 带信封的 JSON 三种都收。

    三种都收不是"贴心",是因为前两版文档里的命令产出的东西脚本自己解析不了 ——
    公正性承诺一旦在用户侧不可执行,它就等于不存在。
    """
    with open(path, encoding="utf-8") as fh:
        text = fh.read().strip()
    if text == "":
        raise SystemExit("文件是空的")

    lines = [ln for ln in text.splitlines() if ln.strip() != ""]
    if len(lines) > 1:
        # NDJSON:第一行是文档头,其余是条目。
        doc = json.loads(lines[0])
        if "act_no" not in doc:
            raise SystemExit("第一行不是证据链文档头,请用 ?format=ndjson 重新下载")
        entries = [json.loads(ln) for ln in lines[1:]]
        bad = [e for e in entries if "error" in e]
        if bad:
            raise SystemExit("下载在中途出错(%s),这一份不完整,请重新下载" % bad[0]["error"])
        doc["entries"] = entries
        return doc

    doc = json.loads(text)
    # 接口响应信封 {success, message, data}。
    if "data" in doc and "act_no" not in doc:
        doc = doc["data"]
    if "act_no" not in doc:
        raise SystemExit("这不是一份证据链文档(没有 act_no)")
    return doc


def check(ok, label, detail=""):
    print(("  [OK]   " if ok else "  [FAIL] ") + label + (("  " + detail) if detail and not ok else ""))
    return ok


# ─────────────────────────── 单人复算 ───────────────────────────

def explain(p, entry_no):
    """回答一个问题:这张票为什么中/没中。

    概率制如果不能在"我没中"的那一刻被立刻看懂,历史公正查询就退化成了
    平台的一面之词。所以这不是一个调试开关,是这套设计的交付物之一。
    """
    algo = p["algo"]
    seed = p.get("seed", "")
    if not seed:
        print("尚未揭示种子,还算不出票面。"); return 2
    entries = p["entries"]
    if len(entries) != p.get("total", len(entries)):
        print("只取到 %d / %d 条,名单不完整,算出来的票面必然是错的。"
              "请用 ?format=ndjson 重新下载。" % (len(entries), p["total"]))
        return 2

    R = sorted([e for e in entries if e["status"] == "success"], key=lambda e: e["entry_no"])
    mine = [e for e in R if e["entry_no"] == entry_no]
    if not mine:
        print("票号 %s 不在有效名单里。" % entry_no)
        print("(它可能扣费失败、或在封盘之后才落定 —— 请在 entries 里按 entry_no 查它的 status。)")
        return 1
    me = mine[0]

    final = H("qylot-final-v1", p["act_no"], seed, p["roster_hash"],
              d(p["roster_count"]), algo)
    tick = ticket(final, p["act_no"], me["entry_no"])
    print("票号 %s" % entry_no)
    print("  final_seed = %s" % final)
    print("  票面       = %s" % tick)

    mode = g(p, "draw_mode", "") or "rank"
    if p["kind"] != "draw":
        print("  这是竞猜,不抽签:你押的是 %s 号选项,本场结果是 %s 号。"
              % (g(me, "opt_no"), p.get("win_opt_no")))
        return 0

    if mode == "prob":
        r = roll_ppm(tick)
        bands, err = bands_of(p["spec"])
        if err is not None:
            print("  概率表本身不合法(%s),这一场根本不该被开出去。" % err)
            return 1
        print("  摇号结果 r = %d(取值范围 0 ~ 999999)" % r)
        print("  本场各档的摇号区间:")
        hit = None
        for band in bands:
            mark = " "
            if band["lo"] <= r < band["hi"]:
                mark, hit = "*", band
            print("   %s 第 %d 档  [%d, %d)  概率 %.4f%%"
                  % (mark, band["tier"], band["lo"], band["hi"],
                     (band["hi"] - band["lo"]) * 100.0 / PPM_DEN))
        if hit is None:
            print("  结论:r 落在**全部区间之外** —— 这就是你没中的全部原因。")
            print("  注意这个结论只依赖 final_seed、act_no 和你自己的票号,"
                  "与别人报了多少张票无关。")
        else:
            print("  结论:r 落在第 %d 档的区间内,你中了这一档。" % hit["tier"])
        return 0

    if mode == "ball":
        reds = ball_draw(final, p["act_no"], "red", int(g(p, "ball_red_pool")),
                         int(g(p, "ball_red_pick")))
        blues = ball_draw(final, p["act_no"], "blue", int(g(p, "ball_blue_pool")),
                          int(g(p, "ball_blue_pick")))
        tier, rm, bm = ball_tier_of(reds, blues, g(me, "pick", ""),
                                    sorted(p["spec"], key=lambda s: g(s, "tier")))
        print("  你选的号 = %s" % g(me, "pick", "(无)"))
        print("  本期开奖号 = %s | %s"
              % (",".join("%02d" % n for n in reds), ",".join("%02d" % n for n in blues)))
        print("  命中红球 %d 个、蓝球 %d 个" % (rm, bm))
        print("  本场奖级门槛(红/蓝,均为下界):")
        for t in sorted(p["spec"], key=lambda s: g(s, "tier")):
            print("     第 %d 档  红 >= %d,蓝 >= %d"
                  % (g(t, "tier"), int(g(t, "red_match")), int(g(t, "blue_match"))))
        print("  结论:%s" % ("未达最低奖级,没中" if tier == 0 else "你中了第 %d 档" % tier))
        print("  注意开奖号只依赖 final_seed 与号池大小 —— 它在你下注之前就已经"
              "被种子决定了,只是当时谁都算不出来(名单还没冻结)。")
        return 0

    if mode == "rank":
        ticks = {e["entry_no"]: ticket(final, p["act_no"], e["entry_no"]) for e in R}
        ranked = sorted(R, key=lambda e: (ticks[e["entry_no"]], e["entry_no"]))
        if not p["allow_multi_win"]:
            seen, uniq = set(), []
            for e in ranked:
                if e["user_ref"] in seen:
                    continue
                seen.add(e["user_ref"])
                uniq.append(e)
            ranked = uniq
        idx = [i for i, e in enumerate(ranked) if e["entry_no"] == entry_no]
        if not idx:
            print("  你的票在去重时被跳过了(同一个 user_ref 只保留票面最小的那张)。")
            return 0
        i, total_slots = idx[0], 0
        print("  全场按票面升序,你排第 %d 名(0 基)" % i)
        for s in sorted(p["spec"], key=lambda s: g(s, "tier")):
            lo, hi = total_slots, total_slots + int(g(s, "count"))
            print("    第 %d 档取第 %d ~ %d 名" % (g(s, "tier"), lo, hi - 1))
            total_slots = hi
        print("  结论:%s" % ("你没中(名次在全部奖档之外)" if i >= total_slots else "你中奖了"))
        return 0

    print("  定档方式 %s 不在本脚本的支持范围内。" % mode)
    return 2


# ─────────────────────────── 主流程 ───────────────────────────

def cancelled_before_commit(p):
    """这一场是否在“承诺与冻结名单”之前就收场了。

    发布前就被取消的活动从未做过承诺、从未冻结过名单，
    commit_hash / roster_hash 合法地为空串。把它们无条件拿去与复算值比对，
    得到的只能是一个必然的红叉 —— 而真实情况是平台什么都没承诺过、
    也什么都没发生。本脚本的原则是“断了与被篡改了在验证结果上无法
    区分，所以一律跳过，绝不给一个可能是假的绿勾”；反方向同理 ——
    也绝不给一个假的红叉，那会把一场完全诚实的收场在公开页面上
    渲染成“平台篡改了证据链”。

    判据必须同时满足两条，缺一不可：
      * 结局是取消 / 流局（REFUND_OUTCOMES）；
      * locked_at 为空（从未封盘）。
    一场封过盘的活动如果 commit_hash / roster_hash 空了，那就是承诺被
    抹掉了，必须照旧报 FAIL。
    """
    return not p.get("locked_at") and p.get("outcome", "") in REFUND_OUTCOMES


def main(path, explain_entry=None):
    p = load(path)
    algo = p["algo"]

    print("活动 %s(%s / %s),算法 %s" % (p["act_no"], p["kind"], p["status"], algo))
    if algo not in SUPPORTED_ALGOS:
        # 未知版本绝不给一个可能是假的绿勾。
        print("  未知算法版本,本脚本只认 %s" % " / ".join(sorted(SUPPORTED_ALGOS)))
        return 2

    mode = g(p, "draw_mode", "") or ("rank" if p["kind"] == "draw" else "")
    if p["kind"] == "draw" and mode not in ("rank", "prob", "ball"):
        print("  定档方式 %s 的复算分支尚未合入本脚本。" % mode)
        print("  **不给结论** —— 一个验不了的绿勾比没有绿勾糟糕得多。")
        return 2

    if explain_entry:
        return explain(p, explain_entry)

    failures = 0
    if p.get("locked_at"):
        print("  封盘 %s,揭示 %s(名单哈希先公开的那一段就是你能抓快照的窗口)"
              % (p.get("locked_at"), p.get("revealed_at") or "尚未"))
    if p["kind"] == "draw":
        print("  定档方式:%s" % {
            "rank": "名次制(按票面排序抽前 N 位)",
            "prob": "概率制(每张票各摇一次,按公示概率定档)",
            "ball": "双色球(全场共摇一次,按红蓝命中数定档)",
        }[mode])

    print("\n1. 承诺:种子/条件/奖档/概率/时刻/开关一个都不能改")
    rh = H("qylot-rules-v1", p["rules_text"])
    failures += not check(rh == p["rules_hash"], "rules_hash", rh)
    if algo == "lot-v2":
        sh = H("qylot-spec-v2", *spec_lines_v2(p["kind"], p["spec"]))
    else:
        sh = H("qylot-spec-v1", *spec_lines_v1(p["kind"], p["spec"]))
    failures += not check(sh == p["spec_hash"], "spec_hash", sh)

    if mode == "prob":
        _, err = bands_of(p["spec"])
        failures += not check(err is None, "各档概率之和不超过 100%", err or "")

    seed = p.get("seed", "")
    if not seed:
        print("  [SKIP] commit_hash —— 种子尚未揭示(活动还没到开奖时刻)")
    elif not p.get("commit_hash") and cancelled_before_commit(p):
        # 从未发布就被取消的活动根本没做过承诺，commit_hash 合法地为空串。
        # 无条件复算再与空串比对必然不等，给出的是一个假的红叉。
        print("  [SKIP] commit_hash —— 活动在发布前就被取消，从未做过承诺")
    else:
        ch = commit_hash_v2(p, seed) if algo == "lot-v2" else commit_hash_v1(p, seed)
        failures += not check(ch == p["commit_hash"], "commit_hash", ch)

    entries = sorted(p["entries"], key=lambda e: e["seq"])
    if len(entries) != p["total"]:
        # 只拿到一页。链与名单在这份数据上算出来的任何结论都是错的,如实跳过 ——
        # 并且**绝不返回 0**:0 的含义是"验过了、没问题",而这份数据根本没验完。
        print("\n只取到 %d / %d 条,链与名单无法验证。" % (len(entries), p["total"]))
        print("请用 ?format=ndjson 重新下载完整的一份再验。")
        return 2

    print("\n2. 哈希链:没有条目被插入、删除或改动(含失败条目)")
    chain, expect_seq, broken = p["commit_hash"], 1, None
    for e in entries:
        if e["seq"] != expect_seq:
            broken = "序号在 %d 处断开(应为 %d)" % (e["seq"], expect_seq); break
        if e["prev_hash"] != chain:
            broken = "第 %d 条的 prev_hash 对不上" % e["seq"]; break
        chain = chain_next(algo, chain, p, e)
        if chain != e["chain_hash"]:
            broken = "第 %d 条的 chain_hash 对不上" % e["seq"]; break
        expect_seq += 1
    failures += not check(broken is None, "逐条哈希链(%d 条)" % len(entries), broken or "")
    if not broken and p.get("chain_head"):
        failures += not check(chain == p["chain_head"], "chain_head")
    print("  提示:请另行核对你自己报名时收到的回执 chain_hash 与这里同一条完全一致。")

    print("\n3. 有效名单在揭示种子之前就已冻结")
    R = sorted([e for e in entries if e["status"] == "success"], key=lambda e: e["entry_no"])
    rhash = roster_hash(algo, p, R)
    if not p.get("roster_hash") and cancelled_before_commit(p):
        # 封盘前就被取消：名单从未被冻结，roster_hash 合法地为空串，
        # 同样不能拿它去与一个复算值比对。
        print("  [SKIP] roster_hash —— 活动在封盘前就被取消，名单从未被冻结")
        roster_ok = True
    else:
        roster_ok = rhash == p["roster_hash"] and len(R) == p["roster_count"]
        failures += not check(rhash == p["roster_hash"], "roster_hash", rhash)
        failures += not check(len(R) == p["roster_count"], "roster_count")
    print("  关键:上面的 roster_hash 必须与你在封盘后、开奖前自行抓取的那一份一致。")

    if not seed:
        print("\n尚未开奖,可验证的部分到此为止。")
        return 1 if failures else 0
    if not roster_ok:
        # 名单本身就对不上,再往下复算只会得到一个"当然不一样"的结果,
        # 或者更糟——一个碰巧相同的 [OK],让人以为只有名单那一项有问题。
        print("\n名单与已公开的快照对不上,后面的复算没有意义,到此为止。")
        return 1

    final = H("qylot-final-v1", p["act_no"], seed, p["roster_hash"], d(p["roster_count"]), algo)
    print("\n4. 最终随机源 final_seed = %s" % final)

    refunded = p.get("outcome", "") in REFUND_OUTCOMES

    if p["kind"] == "draw":
        tick = {e["entry_no"]: ticket(final, p["act_no"], e["entry_no"]) for e in R}
        reds, blues = [], []
        if mode == "prob":
            expect, err = prob_winners(p, R, tick)
        elif mode == "ball":
            expect, err, reds, blues = ball_winners(p, R)
            # **这一步才是双色球的检验点**:界面上那几颗球必须是产生结果的原因,
            # 而不是结果产生之后编出来的动画。开奖号不进承诺,它由公开的种子
            # 摇出来 —— 所以自己重摇一遍再与平台公布的比对。
            got = p.get("ball_result", "")
            mine = "%s|%s" % (",".join("%02d" % n for n in reds),
                              ",".join("%02d" % n for n in blues))
            failures += not check(mine == got, "开奖号可由种子独立重摇", mine)
            print("   你自己摇出来的号:%s" % mine)
        else:
            expect, err = rank_winners(p, R, p["allow_multi_win"], tick)
        if err is not None:
            print("\n5. 复算失败:%s" % err)
            print("   这一场根本不该被开出去,请保留这份 proof。")
            return 1

        if refunded:
            # 取消 / 流局:没有开出结果,winners 恒为空,拿它比对必然 FAIL,
            # 而真实情况是全额退款。这里改为把「本应中奖的名单」打印出来 ——
            # 它是判断"管理员是不是看了结果才决定不开"的唯一材料。
            print("\n5. 本场以「%s」收场,没有开出结果;下面是按公开种子复算出的"
                  "**本应中奖名单**,供你判断这次取消是否可疑:" % p["outcome"])
            for pos, tier, entry_no, amount in expect:
                print("     #%d  第 %d 档  %s  %d" % (pos, tier, entry_no, amount))
            print("   并核对每一位参与者都收到了等额退款:")
            refunds = {x["entry_no"]: x["amount"] for x in p["payouts"]
                       if x.get("kind") == "refund"}
            failures += not check(refunds == {e["entry_no"]: e["amount"] for e in R},
                                  "全额退回本金", str(refunds))
        else:
            print("\n5. 重算中奖名单")
            got = [(w["pos"], w["tier"], w["entry_no"], w["amount"])
                   for w in sorted(p["winners"], key=lambda w: w["pos"])]
            failures += not check(expect == got, "中奖名单(%d 位)" % len(expect), str(expect))
            if mode in ("prob", "ball"):
                print("   **每一条**参与都被判定过一次:%d 张有效票里 %d 张中奖、%d 张落选。"
                      % (len(R), len(expect), len(R) - len(expect)))
                print("   想看某一张票为什么没中:python3 %s %s --explain <entry_no>"
                      % (sys.argv[0], path))
            texts = [w for w in p["winners"] if w.get("prize_type") == "text"]
            if texts:
                print("   本场有 %d 位中的是文本奖(兑换码 / 实物说明)。" % len(texts))
                print("   可验证的是:这一档的名称、公开说明与份数在发布时已进承诺、事后没被改。")
                print("   **不可验证的是**:中奖者收到的那串具体内容 —— 它在开奖之后才由管理员"
                      "填入,承诺那一刻还不存在,所以证据链里只有一个 fulfilled 布尔。")
                unfulfilled = [w for w in texts if not w.get("fulfilled")]
                if unfulfilled:
                    print("   其中 %d 位尚未被标记履行。" % len(unfulfilled))
    else:
        print("\n5. 重算奖池分配")
        pool = sum(e["amount"] for e in R)
        failures += not check(pool == p["pool_quota"], "奖池总额")
        W = [e for e in R if e["opt_no"] == p["win_opt_no"]]
        win = sum(e["amount"] for e in W)
        # 只比对赔付那一类。被排除条目的退款(封盘时还没落定的那些)不属于分配
        # 结果,混进来会让一场诚实的结算被判成"逐笔赔付对不上"。
        pays = {x["entry_no"]: x["amount"] for x in p["payouts"]
                if x["amount"] > 0 and x.get("kind") == "win"}
        refunds = {x["entry_no"]: x["amount"] for x in p["payouts"]
                   if x["amount"] > 0 and x.get("kind") == "refund"}
        if win == 0 or win == pool or refunded:
            # 全部猜错 / 无输家 / 无对手盘 → 全额退回本金,手续费一分不收。
            failures += not check(p["fee_quota"] == 0, "手续费必须为 0")
            failures += not check(refunds == {e["entry_no"]: e["amount"] for e in R},
                                  "全额退回本金", str(refunds))
        else:
            fee = pool * p["fee_bps"] // 10000
            failures += not check(fee == p["fee_quota"], "手续费", str(fee))
            net, acc, exp = pool - fee, 0, {}
            for idx, e in enumerate(W):
                pay = (net - acc) if idx == len(W) - 1 else net * e["amount"] // win
                acc += pay
                if pay > 0:
                    exp[e["entry_no"]] = pay
            failures += not check(exp == pays, "逐笔赔付")
            failures += not check(acc + fee == pool, "守恒式 Σpay + fee == pool")

    print("\n结论:" + ("全部通过。" if failures == 0 else "有 %d 项对不上,请保留这份 proof。" % failures))
    return 1 if failures else 0


if __name__ == "__main__":
    args = sys.argv[1:]
    entry = None
    if "--explain" in args:
        i = args.index("--explain")
        if i + 1 >= len(args):
            print("--explain 后面要跟一个 entry_no"); sys.exit(2)
        entry = args[i + 1]
        args = args[:i] + args[i + 2:]
    if len(args) != 1:
        print(__doc__); sys.exit(2)
    sys.exit(main(args[0], entry))
