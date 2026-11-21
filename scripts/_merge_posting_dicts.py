#!/usr/bin/env python3
"""把 2libra-发帖规则.md 里已爬取的关键词字典合并进汇总 JSON。

这些字典来自网页前端 JS（发帖页 chunk），已在 2libra-发帖规则.md 中整理，
此处并入同一份 JSON，便于一次性拿到全部「字典数据」。
"""

import json
from pathlib import Path

out = Path("data/2libra-dict.json")
d = json.loads(out.read_text(encoding="utf-8"))

d["posting_rule_dicts"] = {
    "_source": "2libra-发帖规则.md（网页前端 JS 爬取整理，2026-11-20）",
    "title_tag_dict": {
        "redeem": "【兑换码】",
        "vote": "【投票】",
        "bet": "【竞猜】",
        "rewardPool": "【💰】",
        "restrict": "【🔒】",
    },
    "legal_tag_keywords": ["兑换码", "投票", "竞猜", "💰", "🔒"],
    "ai_recommend_keywords": {
        "💼 工作内容": [
            "找工作", "招人", "求职", "招聘", "全职", "兼职", "实习", "社招",
            "校招", "内推", "猎头", "跳槽", "面试", "offer", "简历", "岗位",
            "薪资", "待遇", "月薪", "时薪", "远程办公", "居家办公", "入职",
        ],
        "🎁 兑换码": [
            "兑换码", "邀请码", "优惠码", "激活码", "注册码", "折扣码",
            "优惠券", "代金券", "礼品卡",
        ],
        "🗳️ 投票": [
            "投票", "你们觉得", "对比", "哪个好", "选哪个", "大家觉得",
            "谁更", "PK", "二选一", "三选一", "投票决定", "怎么选",
        ],
        "🎰 竞猜": [
            "竞猜", "猜猜", "猜一猜", "下注", "赔率", "猜结果", "我猜",
            "你们猜", "谁能赢", "谁会赢",
        ],
        "💬 点评": [
            "电影", "推荐", "分享", "打分", "评分", "评价", "评测", "测评",
            "观后感", "读后感", "值得", "好不好", "实测", "开箱", "安利",
            "吐槽", "体验",
        ],
    },
    "extension_menu": [
        {"name": "💼 工作内容", "desc": "—", "suitable_nodes": "适合“求职”“招人啦”等节点", "anonymous_ok": True},
        {"name": "🎁 兑换码", "desc": "添加兑换码/红包", "suitable_nodes": "适合“作品分享”“分享发现”等节点", "anonymous_ok": True},
        {"name": "🗳️ 投票", "desc": "发起投票", "suitable_nodes": "适合一些需要投票的场景", "anonymous_ok": True},
        {"name": "🎰 竞猜", "desc": "发起事件竞猜，用户投入金币参与", "suitable_nodes": "—", "anonymous_ok": False},
        {"name": "💰 金币池", "desc": "用户评论有概率获得金币奖励", "suitable_nodes": "—", "anonymous_ok": False},
        {"name": "💬 点评", "desc": "对任何对象进行点评", "suitable_nodes": "—", "anonymous_ok": True},
        {"name": "👤 匿名发布", "desc": "以匿名身份发布帖子，保护隐私", "suitable_nodes": "—", "anonymous_ok": None},
        {"name": "👁️ 可见性", "desc": "设置帖子的可见范围，可按登录状态、等级或属性限制正文可见", "suitable_nodes": "—", "anonymous_ok": True},
        {"name": "🔒 隐藏内容", "desc": "设置仅满足条件后可见的隐藏内容", "suitable_nodes": "—", "anonymous_ok": False},
    ],
}

out.write_text(json.dumps(d, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
print("已合并发帖规则字典 ->", out, f"({out.stat().st_size} bytes)")
