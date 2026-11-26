package main

import "testing"

// 媒体识别：正文里独占一段的视频链接与裸图片地址都要认出来，别的页面一律
// 不能误认——认错就会把一条普通链接变成一张卡片或一张图。

func TestParseVideoLinkProviders(t *testing.T) {
	cases := []struct {
		raw      string
		provider videoProvider
		id       string
		watch    string
		thumb    string
	}{
		{
			raw:      "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			provider: videoYouTube, id: "dQw4w9WgXcQ",
			watch: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			thumb: "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg",
		},
		{
			raw:      "https://youtu.be/dQw4w9WgXcQ?t=42",
			provider: videoYouTube, id: "dQw4w9WgXcQ",
			watch: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			thumb: "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg",
		},
		{
			raw:      "https://m.youtube.com/shorts/abc123XYZ_-",
			provider: videoYouTube, id: "abc123XYZ_-",
			watch: "https://www.youtube.com/watch?v=abc123XYZ_-",
			thumb: "https://i.ytimg.com/vi/abc123XYZ_-/hqdefault.jpg",
		},
		{
			raw:      "https://www.bilibili.com/video/BV1xx411c7mD?p=2",
			provider: videoBilibili, id: "BV1xx411c7mD",
			watch: "https://www.bilibili.com/video/BV1xx411c7mD",
		},
		{
			raw:      "https://www.bilibili.com/video/av170001/",
			provider: videoBilibili, id: "av170001",
			watch: "https://www.bilibili.com/video/av170001",
		},
		{
			raw:      "https://b23.tv/abcd12",
			provider: videoBilibili, id: "",
			watch: "https://b23.tv/abcd12",
		},
		{
			raw:      "https://cdn.example.com/clip.mp4",
			provider: videoDirect, id: "",
			watch: "https://cdn.example.com/clip.mp4",
		},
	}
	for _, c := range cases {
		ref, ok := parseVideoLink(c.raw)
		if !ok {
			t.Errorf("parseVideoLink(%q) 没认出视频", c.raw)
			continue
		}
		if ref.Kind != mediaVideo || ref.Provider != c.provider || ref.ID != c.id {
			t.Errorf("parseVideoLink(%q) = %+v，想要 provider=%d id=%q", c.raw, ref, c.provider, c.id)
		}
		if ref.WatchURL != c.watch {
			t.Errorf("parseVideoLink(%q) 的播放页 = %q，想要 %q", c.raw, ref.WatchURL, c.watch)
		}
		if ref.ThumbURL != c.thumb {
			t.Errorf("parseVideoLink(%q) 的封面 = %q，想要 %q", c.raw, ref.ThumbURL, c.thumb)
		}
	}
}

// 认不出视频的链接不能硬认：仿冒域名、普通网页、图片都不算视频。
func TestParseVideoLinkRejects(t *testing.T) {
	for _, raw := range []string{
		"https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ",
		"https://notyoutube.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/watch",
		"https://www.bilibili.com/read/cv12345",
		"https://example.com/article",
		"https://example.com/photo.png",
		"ftp://example.com/clip.mp4",
		"javascript:alert(1)",
	} {
		if ref, ok := parseVideoLink(raw); ok {
			t.Errorf("parseVideoLink(%q) 误认成视频：%+v", raw, ref)
		}
	}
}

// 图片按扩展名认，查询串与锚点不参与；不是图片的页面不能当成图。
func TestIsImageLink(t *testing.T) {
	for _, raw := range []string{
		"https://img.example.com/a/photo.png",
		"https://img.example.com/a.JPG?x=1",
		"https://img.example.com/a.gif#top",
		"https://img.example.com/a.webp",
		"https://img.example.com/a.avif",
		"https://img.example.com/a.svg",
	} {
		if !isImageLink(raw) {
			t.Errorf("isImageLink(%q) = false，期望 true", raw)
		}
	}
	for _, raw := range []string{
		"https://example.com/post/work/ABC",
		"https://example.com/photo",
		"https://example.com/clip.mp4",
		"not a url",
	} {
		if isImageLink(raw) {
			t.Errorf("isImageLink(%q) = true，期望 false", raw)
		}
	}
}

// 标题与来源名：卡片上要写得出人认得的东西。
func TestMediaRefTitle(t *testing.T) {
	ref, _ := parseVideoLink("https://youtu.be/dQw4w9WgXcQ")
	if got, want := ref.title(), "YouTube · dQw4w9WgXcQ"; got != want {
		t.Errorf("YouTube 标题 = %q，想要 %q", got, want)
	}
	ref, _ = parseVideoLink("https://b23.tv/abcd12")
	if got, want := ref.title(), "哔哩哔哩视频"; got != want {
		t.Errorf("短链标题 = %q，想要 %q", got, want)
	}
	ref, _ = parseVideoLink("https://cdn.example.com/clips/trailer.mp4")
	if got, want := ref.title(), "trailer.mp4"; got != want {
		t.Errorf("直链视频标题 = %q，想要 %q", got, want)
	}
	if got, want := mediaNameFromURL("https://img.example.com/a/photo.png"), "photo.png"; got != want {
		t.Errorf("图片名 = %q，想要 %q", got, want)
	}
	if got, want := mediaNameFromURL("https://img.example.com/"), "图片"; got != want {
		t.Errorf("取不到名字时 = %q，想要 %q", got, want)
	}
}
