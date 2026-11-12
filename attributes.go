package main

// 六维属性。GET /v1/users/:userName 的 user_attributes 给出六项：
//
//	craft_attr       技艺
//	diligence_attr   勤勉
//	charm_attr       魅力
//	experience_attr  阅历
//	karma_attr       财富
//	luck_attr        幸运
//
// 显示名取自同一用户 GET /v1/users/:userName/about 的 constantsLabel
//（「技艺属性」「勤勉属性」……），去掉「属性」二字；顺序沿用站点
// achievement_count 里这六项的排列，与资料卡的「属性雷达」一致，避免两处
// 雷达图的轴顺序不同。
//
// 值是字符串数字，按字段名原样读取；顺序固定为上面的顺序，雷达图的轴才不会
// 因为响应里键的顺序变化而转来转去。文档没有给单项上限，站内 D20 属性检定
// 按 0～10 取值（compose.go 的「属性下限」同样限制在 0～10），这里沿用 10 作为
// 雷达图满格；超过 10 的值在图上夹到边缘，但图例仍然显示服务端给的原值。

// profileAttributeMax 是属性雷达图的满格值。
const profileAttributeMax = 10

// profileAttribute 是一项属性：Key 是接口字段名，Label 是界面上的名字。
type profileAttribute struct {
	Key   string
	Label string
	Value int
}

// profileAttributeDefs 固定六维属性的顺序与中文名，与绘制雷达图的轴一一对应。
var profileAttributeDefs = []profileAttribute{
	{Key: "craft_attr", Label: "技艺"},
	{Key: "diligence_attr", Label: "勤勉"},
	{Key: "charm_attr", Label: "魅力"},
	{Key: "experience_attr", Label: "阅历"},
	{Key: "karma_attr", Label: "财富"},
	{Key: "luck_attr", Label: "幸运"},
}

// decodeProfileAttributes 读用户对象里的 user_attributes。known 区分「没有这个
// 字段」与「六项都是 0」：前者不展示属性块，后者画一个贴着中心的小六边形。
// 缺少个别项时按 0 补齐，保证雷达图始终是六条轴。
func decodeProfileAttributes(o object) (attrs []profileAttribute, known bool) {
	if o == nil {
		return nil, false
	}
	src := o.sub("user_attributes")
	if src == nil {
		// 兼容：六项直接摊在顶层时也认，不把整块属性丢掉。
		if !o.has(attributeKeys()...) {
			return nil, false
		}
		src = o
	}
	attrs = make([]profileAttribute, 0, len(profileAttributeDefs))
	for _, def := range profileAttributeDefs {
		if _, ok := src[def.Key]; ok {
			known = true
		}
		attrs = append(attrs, profileAttribute{
			Key:   def.Key,
			Label: def.Label,
			Value: rawInt(src[def.Key]),
		})
	}
	if !known {
		return nil, false
	}
	return attrs, true
}

// attributeKeys 返回六个接口字段名，供顶层兼容检查使用。
func attributeKeys() []string {
	keys := make([]string, len(profileAttributeDefs))
	for i, def := range profileAttributeDefs {
		keys[i] = def.Key
	}
	return keys
}

// profileAttributeLevel 把属性值换算成雷达图上的 0～1 比例：负值贴中心，
// 超过满格的值贴外圈。
func profileAttributeLevel(value int) float32 {
	if value <= 0 {
		return 0
	}
	if value >= profileAttributeMax {
		return 1
	}
	return float32(value) / float32(profileAttributeMax)
}
