package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// 脱敏：日志在入队前过 redactText，网络记录在导出诊断包时再把头和体各过一遍。
// 目标只有一类：令牌、授权码、Cookie 与代理密码不能离开本机。业务字段（2Libra
// 响应信封里的 code、分页的 next_token）保留原值，抹掉它们会让诊断包失去排障
// 价值。
var (
	reCookieHeader = regexp.MustCompile(`(?im)^(\s*(?:set-)?cookie\s*:\s*)([^\r\n]+)`)
	reAuthHeader   = regexp.MustCompile(`(?im)^(\s*(?:proxy-)?authorization\s*:\s*)([^\r\n]+)`)
	reBearer       = regexp.MustCompile(`(?i)\b(bearer\s+)([A-Za-z0-9\-._~+/]+=*)`)
	reSecretKV     = regexp.MustCompile(`(?i)(\b(?:access_token|refresh_token|id_token|code_verifier|client_secret|password)=)([^&\s"'<>]+)`)
	reSecretJSON   = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|code_verifier|client_secret|password)"\s*:\s*")((?:[^"\\]|\\.)*)(")`)
	reOAuthCode    = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^\s"'<>]*/oauth/[^\s"'<>]*[?&]code=)([^&\s"'<>]+)`)
	reUserInfo     = regexp.MustCompile(`([a-z][a-z0-9+.-]*://)([^:/@\s]+):([^@/\s]+)@`)
)

// redactText 抹掉任意文本里的敏感值：日志行、URL、请求头、表单与 JSON 片段。
func redactText(s string) string {
	if s == "" {
		return s
	}
	s = reCookieHeader.ReplaceAllString(s, "$1***")
	s = reAuthHeader.ReplaceAllString(s, "$1***")
	s = reBearer.ReplaceAllString(s, "$1***")
	s = reSecretJSON.ReplaceAllString(s, "$1***$3")
	s = reSecretKV.ReplaceAllString(s, "$1***")
	// 只抹 OAuth 回调地址里的 code，普通业务查询里的 code 不动。
	s = reOAuthCode.ReplaceAllString(s, "$1***")
	s = reUserInfo.ReplaceAllString(s, "$1$2:***@")
	return s
}

// sensitiveJSONKey 是请求 / 响应体里必须替换的键。不包含泛化的 code 与 token。
func sensitiveJSONKey(k string) bool {
	switch strings.ToLower(k) {
	case "access_token", "refresh_token", "id_token", "code_verifier", "client_secret", "password":
		return true
	}
	return false
}

// redactJSONBody 处理网络记录里的请求 / 响应体：能解析成 JSON 时递归替换敏感
// 键；截断或不是 JSON 时退回 redactText，仍然覆盖 `"refresh_token":"..."` 这种
// 片段。
func redactJSONBody(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return redactText(s)
	}
	redactJSONValue(v)
	b, err := json.Marshal(v)
	if err != nil {
		return redactText(s)
	}
	return string(b)
}

func redactJSONValue(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if sensitiveJSONKey(k) {
				x[k] = "***"
				continue
			}
			redactJSONValue(val)
		}
	case []any:
		for _, item := range x {
			redactJSONValue(item)
		}
	}
}
