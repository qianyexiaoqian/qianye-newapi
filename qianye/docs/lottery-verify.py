#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""抽奖/竞猜公正性离线验证脚本(协议 lot-v1)。

用法:
    curl 'https://<站点>/api/qy/lottery/public/<act_no>/proof?format=ndjson' > proof.ndjson
    python3 lottery-verify.py proof.ndjson

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
  1. 承诺没有被替换  —— 种子/条件/奖档/四个时刻/每一个影响结果的开关都没改过
  2. 名单没有被增删 —— 逐条哈希链连续,且每个参与者手里的回执都在链上
  3. 名单先于种子冻结 —— roster_hash 在揭示之前就已公开
  4. 中奖名单可复算 —— 用公开的种子重跑一遍,结果必须一模一样

它**不能**证明什么(诚实说明,不粉饰):
  - 竞猜的 win_opt_no 是不是符合外部事实。那是链下事实,任何密码学都证不了
    世界杯谁赢了。能做的只有:选项集合与费率在发布时进承诺、结果必须附证据、
    一经写入不可改。这把作弊面压缩到"一次性地公开撒谎"。
  - "同一个人的票标了同一个 user_ref"。ref_salt 永不公开(公开就能枚举反查
    全部参与者身份),因此它不进承诺原像。这只影响 allow_multi_win=false
    这一条约束,不影响每张票中签概率严格相等。
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

# 这些结局意味着"没有开出结果,全额退款"。抽奖在这几种收场下 winners 恒为空,
# 拿它去和复算名单比对只会得到一个必然的 FAIL —— 而真实情况是平台已经全额
# 退款、行为完全诚实。
REFUND_OUTCOMES = {
    "cancelled", "void_min_entries", "void_deadline",
    "void_no_winner", "void_all_correct",
}


def H(*parts):
    """所有哈希的统一形状:SHA-256(部件用 0x1F 连接)。"""
    return hashlib.sha256(SEP.join(parts).encode("utf-8")).hexdigest()


def b(v):
    return "true" if v else "false"


def d(v):
    return str(int(v))


def spec_lines(kind, spec):
    """奖档/选项在 spec_hash 原像里的逐行编码。"""
    if kind == "draw":
        rows = sorted(spec, key=lambda s: s.get("tier", 0))
        return [SEP.join([d(s.get("tier", 0)), s.get("name", ""),
                          d(s.get("amount_quota", 0)), d(s.get("count", 0))]) for s in rows]
    rows = sorted(spec, key=lambda s: s.get("opt_no", 0))
    return [SEP.join([d(s.get("opt_no", 0)), s.get("label", ""),
                      b(s.get("is_catch_all", False))]) for s in rows]


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


def main(path):
    p = load(path)
    failures = 0

    print("活动 %s(%s / %s),算法 %s" % (p["act_no"], p["kind"], p["status"], p["algo"]))
    if p["algo"] != "lot-v1":
        print("  未知算法版本,本脚本只认 lot-v1"); return 2
    if p.get("locked_at"):
        print("  封盘 %s,揭示 %s(名单哈希先公开的那一段就是你能抓快照的窗口)"
              % (p.get("locked_at"), p.get("revealed_at") or "尚未"))

    print("\n1. 承诺:种子/条件/奖档/时刻/开关一个都不能改")
    rh = H("qylot-rules-v1", p["rules_text"])
    failures += not check(rh == p["rules_hash"], "rules_hash", rh)
    sh = H("qylot-spec-v1", *spec_lines(p["kind"], p["spec"]))
    failures += not check(sh == p["spec_hash"], "spec_hash", sh)

    seed = p.get("seed", "")
    if not seed:
        print("  [SKIP] commit_hash —— 种子尚未揭示(活动还没到开奖时刻)")
    else:
        ch = H("qylot-commit-v1", p["act_no"], p["kind"], p["algo"], p["rules_hash"],
               p["spec_hash"], d(p["stake_quota"]), d(p["open_at"]), d(p["close_at"]),
               d(p["draw_at"]), d(p["settle_deadline"]), b(p["allow_multi_win"]),
               d(p["fee_bps"]), p["no_winner_policy"], d(p["min_entries_to_hold"]), seed)
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
        chain = H("qylot-chain-v1", chain, p["act_no"], d(e["seq"]), e["entry_no"],
                  e["user_ref"], d(e["opt_no"]), d(e["amount"]))
        if chain != e["chain_hash"]:
            broken = "第 %d 条的 chain_hash 对不上" % e["seq"]; break
        expect_seq += 1
    failures += not check(broken is None, "逐条哈希链(%d 条)" % len(entries), broken or "")
    if not broken and p.get("chain_head"):
        failures += not check(chain == p["chain_head"], "chain_head")
    print("  提示:请另行核对你自己报名时收到的回执 chain_hash 与这里同一条完全一致。")

    print("\n3. 有效名单在揭示种子之前就已冻结")
    R = sorted([e for e in entries if e["status"] == "success"], key=lambda e: e["entry_no"])
    rows = [SEP.join([e["entry_no"], e["user_ref"], d(e["opt_no"]), d(e["amount"])]) for e in R]
    rhash = H("qylot-roster-v1", p["act_no"], p["commit_hash"], d(len(R)), "\n".join(rows))
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

    final = H("qylot-final-v1", p["act_no"], seed, p["roster_hash"], d(p["roster_count"]), p["algo"])
    print("\n4. 最终随机源 final_seed = %s" % final)

    refunded = p.get("outcome", "") in REFUND_OUTCOMES

    if p["kind"] == "draw":
        expect, i = [], 0
        tick = {e["entry_no"]: hmac.new(bytes.fromhex(final),
                (SEP.join(["qylot-ticket-v1", p["act_no"], e["entry_no"]])).encode("utf-8"),
                hashlib.sha256).hexdigest() for e in R}
        ranked = sorted(R, key=lambda e: (tick[e["entry_no"]], e["entry_no"]))
        if not p["allow_multi_win"]:
            seen, uniq = set(), []
            for e in ranked:
                if e["user_ref"] in seen:
                    continue
                seen.add(e["user_ref"]); uniq.append(e)
            ranked = uniq
        for s in sorted(p["spec"], key=lambda s: s.get("tier", 0)):
            for _ in range(s.get("count", 0)):
                if i >= len(ranked):
                    break  # 票不够则该档空缺,绝不补抽
                expect.append((i, s["tier"], ranked[i]["entry_no"], s["amount_quota"])); i += 1

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
    if len(sys.argv) != 2:
        print(__doc__); sys.exit(2)
    sys.exit(main(sys.argv[1]))
