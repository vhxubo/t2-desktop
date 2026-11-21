#!/usr/bin/env python3
"""抓取 2Libra 开放 API 的全部字典 / 参考数据，汇总成一个 JSON 文件。

字典类端点（均为匿名可读）：
- GET /v1/nodes                     节点（板块）树，顶层自带 children
- GET /v1/emojis                    表情短码 -> 图片地址
- GET /v1/meta/guardians            守护者名单
- GET /v1/tags/hot?period=...       热门标签（day / week / month）
- GET /v1/scores/categories         评分分类字典（需开通，匿名可能 403）

用法：
    python scripts/fetch_dict.py [输出路径]

默认输出：data/2libra-dict.json
"""

import json
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

BASE = "https://2libra.com/api"
UA = "Mozilla/5.0 (compatible; t2-desktop-dict-fetcher/1.0)"


def get(path: str):
    """请求一个 GET 端点，返回 (data, error)。"""
    url = BASE + path
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            body = resp.read().decode("utf-8")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
        return None, {"http_status": e.code, "body": body[:2000]}
    except Exception as e:  # noqa: BLE001 - 脚本容错，原样记录
        return None, {"error": repr(e)}

    try:
        env = json.loads(body)
    except json.JSONDecodeError:
        return None, {"error": "non-json response", "body": body[:2000]}

    if env.get("c") != 0:
        return None, {
            "http_status": env.get("c"),
            "code": env.get("code"),
            "message": env.get("m"),
        }
    return env.get("d"), None


def main():
    out = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("data/2libra-dict.json")

    endpoints = [
        ("nodes", "/v1/nodes"),
        ("emojis", "/v1/emojis"),
        ("guardians", "/v1/meta/guardians"),
        ("tags_hot_day", "/v1/tags/hot?period=day"),
        ("tags_hot_week", "/v1/tags/hot?period=week"),
        ("tags_hot_month", "/v1/tags/hot?period=month"),
        ("scores_categories", "/v1/scores/categories"),
    ]

    result = {
        "fetched_at": datetime.now(timezone.utc).isoformat(),
        "source": BASE,
        "endpoints": {},
        "errors": {},
    }

    for name, path in endpoints:
        data, err = get(path)
        if err is None:
            result["endpoints"][name] = data
            count = len(data) if isinstance(data, (list, dict)) else "?"
            print(f"[ok]   {name:20s} {path} (n={count})")
        else:
            result["errors"][name] = {"path": path, **err}
            print(f"[fail] {name:20s} {path} -> {err}")
        time.sleep(0.3)  # 温和一点，远低于匿名每路由 60/min 的限流

    # 节点树完整性核对：顶层 children 已含子节点，这里再用 ?parent= 抽查确认无更深层。
    nodes = result["endpoints"].get("nodes")
    if isinstance(nodes, list):
        parent_check = {}
        for top in nodes:
            if not isinstance(top, dict):
                continue
            slug = top.get("slug")
            if not slug:
                continue
            sub, err = get(f"/v1/nodes?parent={slug}")
            if err is None:
                parent_check[slug] = sub
            else:
                parent_check[slug] = {"error": err}
            time.sleep(0.2)
        result["endpoints"]["nodes_by_parent"] = parent_check
        print(f"[ok]   nodes_by_parent      抽查 {len(parent_check)} 个顶层节点")

    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(
        json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print(f"\n已写入 {out} ({out.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
