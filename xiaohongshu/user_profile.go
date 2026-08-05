package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

type UserProfileAction struct {
	page *hrod.Page
}

func NewUserProfileAction(page *hrod.Page) *UserProfileAction {
	pp := page.Timeout(60 * time.Second)
	return &UserProfileAction{page: pp}
}

// UserProfile 获取用户基本信息及帖子
// 对齐上游 main：导航后只等 __INITIAL_STATE__ 出现即可提取，不做 profile probe 轮询
// （probe 在用户主页不稳定，曾导致 60s 超时并清理浏览器）。
func (u *UserProfileAction) UserProfile(ctx context.Context, userID, xsecToken string) (*UserProfileResponse, error) {
	page := u.page.Context(ctx).Timeout(60 * time.Second)

	searchURL := makeUserProfileURL(userID, xsecToken)
	if err := page.Navigate(searchURL); err != nil {
		return nil, fmt.Errorf("navigate to user profile failed: %w", err)
	}
	if err := page.Wait(rod.Eval(`() => window.__INITIAL_STATE__ !== undefined`)); err != nil {
		return nil, fmt.Errorf("wait for profile state failed: %w", err)
	}

	return u.extractUserProfileData(page)
}

// extractUserProfileData 从页面中提取用户资料数据的通用方法
func (u *UserProfileAction) extractUserProfileData(page *hrod.Page) (*UserProfileResponse, error) {
	if err := page.Wait(rod.Eval(`() => window.__INITIAL_STATE__ !== undefined`)); err != nil {
		return nil, fmt.Errorf("wait for profile state failed: %w", err)
	}

	raw, err := page.Eval(`() => {
		const s = window.__INITIAL_STATE__;
		if (!s || !s.user) return "";
		const ud = s.user.userPageData;
		const notes = s.user.notes;
		const at = s.user.activeTab;
		if (!ud) return "NO_USERPAGEDATA";
		if (!notes) return "NO_NOTES";
		// Vue ref 的 deps/sub 含循环引用，先按 value → _value → 原对象解包成纯数据再序列化。
		const unwrap = (o) =>
			o && o.value !== undefined ? o.value :
			o && o._value !== undefined ? o._value : o;
		return JSON.stringify({
			userPageData: unwrap(ud),
			notes: unwrap(notes),
			activeTab: unwrap(at)
		});
	}`)
	if err != nil {
		return nil, fmt.Errorf("extract profile state failed: %w", err)
	}
	rawStr := ""
	if raw != nil {
		rawStr = raw.Value.Str()
	}
	switch rawStr {
	case "":
		return nil, fmt.Errorf("__INITIAL_STATE__.user not found")
	case "NO_USERPAGEDATA":
		return nil, fmt.Errorf("user.userPageData.value not found in __INITIAL_STATE__")
	case "NO_NOTES":
		return nil, fmt.Errorf("user.notes.value not found in __INITIAL_STATE__")
	}
	return parseUserProfileState(rawStr)
}

// userProfileStateSnapshot 一次 Eval 提取的个人主页状态 envelope。
type userProfileStateSnapshot struct {
	UserPageData *struct {
		Interactions []UserInteractions `json:"interactions"`
		BasicInfo    UserBasicInfo      `json:"basicInfo"`
	} `json:"userPageData"`
	Notes [][]Feed `json:"notes"`
	Index int      `json:"index"`
	Query string   `json:"query"`
}

// unwrapProfileField 兼容 Vue ref 的 value/_value 双形态。
// JSON.stringify(ref) 通常经 toJSON 展开为 value；若保留 _value 包装则在此展开。
func unwrapProfileField(rm json.RawMessage) json.RawMessage {
	if len(rm) == 0 || rm[0] != '{' {
		return rm
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rm, &m); err != nil {
		return rm
	}
	if v, ok := m["value"]; ok {
		return v
	}
	if v, ok := m["_value"]; ok {
		return v
	}
	return rm
}

// parseUserProfileState 解析个人主页状态，只返回 active 笔记分区。
func parseUserProfileState(raw string) (*UserProfileResponse, error) {
	var rawSnap struct {
		UserPageData json.RawMessage `json:"userPageData"`
		Notes        json.RawMessage `json:"notes"`
		ActiveTab    json.RawMessage `json:"activeTab"`
	}
	if err := json.Unmarshal([]byte(raw), &rawSnap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal userPageData: %w", err)
	}
	if len(rawSnap.UserPageData) == 0 {
		return nil, fmt.Errorf("user.userPageData.value not found in __INITIAL_STATE__")
	}
	if len(rawSnap.Notes) == 0 {
		return nil, fmt.Errorf("user.notes.value not found in __INITIAL_STATE__")
	}

	var snap userProfileStateSnapshot
	if err := json.Unmarshal(unwrapProfileField(rawSnap.UserPageData), &snap.UserPageData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal userPageData: %w", err)
	}
	if snap.UserPageData == nil {
		return nil, fmt.Errorf("user.userPageData.value not found in __INITIAL_STATE__")
	}
	if err := json.Unmarshal(unwrapProfileField(rawSnap.Notes), &snap.Notes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal notes: %w", err)
	}
	// activeTab 走同一 unwrap 规则，wrapper 形态不再静默降级
	if len(rawSnap.ActiveTab) != 0 {
		var at struct {
			Index int    `json:"index"`
			Query string `json:"query"`
		}
		if err := json.Unmarshal(unwrapProfileField(rawSnap.ActiveTab), &at); err != nil {
			return nil, fmt.Errorf("failed to unmarshal activeTab: %w", err)
		}
		snap.Index = at.Index
		snap.Query = at.Query
	}

	// query 非空且不是默认 note 时 fail-closed（不展平其他分区）
	if snap.Query != "" && snap.Query != "note" {
		return nil, fmt.Errorf("unsupported profile tab: %s", snap.Query)
	}
	// index 越界时明确报错，不展平 fallback
	if snap.Index < 0 || snap.Index >= len(snap.Notes) {
		return nil, fmt.Errorf("profile tab index out of range: %d", snap.Index)
	}

	response := &UserProfileResponse{
		UserBasicInfo: snap.UserPageData.BasicInfo,
		Interactions:  snap.UserPageData.Interactions,
	}
	response.Feeds = append(response.Feeds, snap.Notes[snap.Index]...)
	return response, nil
}

func makeUserProfileURL(userID, xsecToken string) string {
	return fmt.Sprintf("https://www.xiaohongshu.com/user/profile/%s?xsec_token=%s&xsec_source=pc_note", userID, xsecToken)
}

func (u *UserProfileAction) GetMyProfileViaSidebar(ctx context.Context) (*UserProfileResponse, error) {
	page := u.page.Context(ctx).Timeout(60 * time.Second)

	// 创建导航动作
	navigate := NewNavigate(page)

	// 通过侧边栏导航到个人主页
	if err := navigate.ToProfilePage(ctx); err != nil {
		return nil, fmt.Errorf("failed to navigate to profile page via sidebar: %w", err)
	}

	return u.extractUserProfileData(page)
}
