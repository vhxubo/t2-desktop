package main

import "log"

// setAccountAvatar 把换好的头像同步到账号索引。它与 noteAuthChange 的区别是
// 允许写入空串：重置头像后账号面板也要回到生成的图案，不能因为「空值不覆盖」
// 而继续显示旧头像。只在主线程调用。
func (b *accountBook) setAccountAvatar(id, avatar string) {
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	acc := b.accountLocked(id)
	if acc == nil || acc.Avatar == avatar {
		b.mu.Unlock()
		return
	}
	acc.Avatar = avatar
	b.mu.Unlock()

	if err := b.save(); err != nil {
		log.Printf("账号索引写入失败：%v", err)
	}
	b.redrawPanel()
	if call := b.changedHook(); call != nil {
		call()
	}
}
