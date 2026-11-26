package main

import (
	"net/url"
	"regexp"
	"strings"
)

// Links to another 2Libra post are opened in place (in-app detail view),
// not handed to the system browser. Everything else keeps the normal
// external-link behaviour.
var (
	shortIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)
	linkInText     = regexp.MustCompile(`(?:https?://|www\.)[^\s<>"'，。；！？、）\]]+|/post/[A-Za-z0-9_\-/%.]+`)
)

// parseSiteLink normalises a link to the 2Libra site and checks its host.
// It accepts absolute http(s) links, protocol-relative links and site-relative
// paths, but never a lookalike host, a foreign scheme or a URL carrying
// userinfo. Both the in-app post link and the user profile link below use it,
// so a link the app claims as its own is never one someone else planted.
func parseSiteLink(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	// Markdown autolinks and Chinese prose leave punctuation behind.
	raw = strings.TrimRight(raw, ".,;:!?)]}，。；！？、》\"'")
	if raw == "" {
		return nil, false
	}
	if strings.HasPrefix(strings.ToLower(raw), "www.") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return nil, false
	}
	switch {
	case u.Scheme == "" && u.Host == "":
		// A "/post/..." or "/user/..." path is relative to the site root.
		// A bare "post/..." is relative to the current page and must not be
		// claimed by the app.
		if !strings.HasPrefix(u.Path, "/") {
			return nil, false
		}
	case u.Scheme == "" && u.Host != "":
		// //2libra.com/... protocol-relative link.
		if !is2LibraHost(u) {
			return nil, false
		}
	case u.Scheme == "https" || u.Scheme == "http":
		if !is2LibraHost(u) {
			return nil, false
		}
	default:
		return nil, false
	}
	return u, true
}

// parseInAppTopicLink recognises absolute, www and site-relative links to a
// post: /post/<short_id> or /post/<node.slug>/<short_id>, optionally with a
// ?commentId= (or #comment-<id>) anchor. It never accepts lookalike hosts.
func parseInAppTopicLink(raw string) (Item, bool) {
	u, ok := parseSiteLink(raw)
	if !ok {
		return Item{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// Only /post/<short_id> and /post/<node.slug>/<short_id> exist. Deeper
	// paths are someone else's route and must not be mistaken for a post.
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "post" {
		return Item{}, false
	}
	shortID := parts[len(parts)-1]
	if !shortIDPattern.MatchString(shortID) {
		return Item{}, false
	}
	slug := ""
	if len(parts) > 2 {
		slug = parts[len(parts)-2]
	}
	commentID := u.Query().Get("commentId")
	if commentID == "" {
		commentID = u.Query().Get("comment_id")
	}
	if commentID == "" {
		commentID = commentFromFragment(u.Fragment)
	}
	origin := topicURL(shortID, slug)
	if commentID != "" {
		origin += "?commentId=" + url.QueryEscape(commentID)
	}
	return Item{
		Key:       shortID,
		ShortID:   shortID,
		CommentID: commentID,
		URL:       origin,
	}, true
}

// parseUserAboutLink recognises a link to a 2Libra user's profile page:
// /user/<name>/about, the target of a "follow" notification. It accepts the
// same shapes as a post link (absolute, www, protocol-relative, site-relative)
// and rejects lookalike hosts, so a foreign "/user/..." path is not opened as
// a local profile.
func parseUserAboutLink(raw string) (string, bool) {
	u, ok := parseSiteLink(raw)
	if !ok {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "user" || parts[2] != "about" {
		return "", false
	}
	name, err := url.PathUnescape(parts[1])
	if err != nil || name == "" {
		return "", false
	}
	return name, true
}

// is2LibraHost rejects lookalike hosts (2libra.com.evil.com), media hosts
// (r2.2libra.com) and non-default ports. It also runs for protocol-relative
// links, which url.Parse leaves with an empty scheme.
func is2LibraHost(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if host != "2libra.com" && host != "www.2libra.com" {
		return false
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return false
	}
	return true
}

func commentFromFragment(fragment string) string {
	for _, prefix := range []string{"comment-", "comment_", "reply-"} {
		if rest := strings.TrimPrefix(fragment, prefix); rest != fragment && shortIDPattern.MatchString(rest) {
			return rest
		}
	}
	return ""
}

// inAppTopicLinks finds post links in plain-text comments, which are shown
// as text (not Markdown) and therefore have no clickable inline link.
func inAppTopicLinks(content string) []Item {
	var out []Item
	seen := make(map[string]bool)
	for _, match := range linkInText.FindAllString(content, -1) {
		it, ok := parseInAppTopicLink(match)
		if !ok || seen[it.ShortID] {
			continue
		}
		seen[it.ShortID] = true
		out = append(out, it)
		if len(out) == 3 {
			break
		}
	}
	return out
}
