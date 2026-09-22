package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	hrod "github.com/xpzouying/xiaohongshu-mcp/humanize/rod"
)

const (
	// loginReadySelector 是已登录时侧栏的用户入口。上游（origin/main login.go:31,123）与本分支重写前
	// 都用它做**唯一**登录判据：有=已登录，没有=未登录。
	// 本机实测两态：已登录=true，未登录=false。
	loginReadySelector = ".main-container .user .link-wrapper .channel"
	// loginQRCodeSelector / loginMaskSelector 只用于识别"登录面已出现"（站点正在要求登录）。
	loginQRCodeSelector = ".login-container .qrcode-img"
	loginMaskSelector   = ".login-container"
	// defaultLoginWaitTimeout 是等扫码的上限；只在没扫码成功时才走到。
	defaultLoginWaitTimeout = 4 * time.Minute
	// defaultQRCodeWaitTimeout 是"二维码元素出现"的失败兜底：出现即返回，放宽只为不误杀慢机器。
	defaultQRCodeWaitTimeout = 120 * time.Second
	// loginSurfaceWaitTimeout 是"已登录 或 登录面出现"的失败兜底：命中即返回。
	// 登录面在未登录首页上几秒内就会出现，所以这条上限只在页面极慢时才用得上。
	loginSurfaceWaitTimeout = 120 * time.Second
)

type LoginAction struct {
	page *hrod.Page
}

func NewLogin(page *hrod.Page) *LoginAction {
	return &LoginAction{page: page}
}

// loginPageState 只用于取展示用的用户名/ID，**不参与登录判定**。
type loginPageState struct {
	Guest    *bool  `json:"guest"`
	UserID   string `json:"userId"`
	Nickname string `json:"nickname"`
}

func parseLoginPageState(raw string) (loginPageState, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return loginPageState{}, errors.New("read login state returned empty value")
	}
	var state loginPageState
	if err := json.Unmarshal([]byte(trimmed), &state); err != nil {
		return loginPageState{}, err
	}
	return state, nil
}

const loginPageStateScript = `() => { const raw=((window.__INITIAL_STATE__||{}).user||{}).userInfo; let info=raw; if(info&&typeof info==="object"&&info.value!==undefined)info=info.value; if(info&&typeof info==="object"&&info.ref&&typeof info.ref==="object")info=info.ref; if(info&&typeof info==="object"&&info.value!==undefined)info=info.value; const obj=info&&typeof info==="object"?info:null; return JSON.stringify({guest:obj&&typeof obj.guest==="boolean"?obj.guest:null,userId:(obj&&(obj.userId||obj.user_id||obj.userid))||"",nickname:(obj&&(obj.nickname||obj.nickName))||""}); }`

type CurrentUser struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"userId"`
}

// CurrentUser 从当前页面的 __INITIAL_STATE__ 读取登录用户信息（展示用）。
// 需在登录判定成立之后调用：复用已加载的 explore 页，不做额外导航。
func (a *LoginAction) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	res, err := evalJSDirect(ctx, a.page, loginPageStateScript)
	if err != nil {
		return nil, errors.Wrap(err, "read current user state failed")
	}
	if res == nil {
		return nil, errors.New("read login state returned no value")
	}
	state, err := parseLoginPageState(res.Value.String())
	if err != nil {
		return nil, err
	}
	if state.Guest == nil || *state.Guest || strings.TrimSpace(state.Nickname) == "" {
		return nil, errors.New("current user not found in page state")
	}
	return &CurrentUser{Nickname: state.Nickname, UserID: strings.TrimSpace(state.UserID)}, nil
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	pp := a.page.Context(ctx)
	if err := EnsureReadyOn(pp, ExploreURL, XHSReadyLogin, 0); err != nil {
		return false, err
	}
	exists, _, err := pp.Has(loginReadySelector)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// waitForLoginSurface 轮询到"已登录"或"登录面（登录遮罩/二维码）出现"任一成立即返回。
// 登录面出现 = 站点明确在要求登录，再等只会把失败拖到超时，所以立即返回 false。
func waitForLoginSurface(ctx context.Context, page *hrod.Page) (bool, error) {
	deadline := time.Now().Add(loginSurfaceWaitTimeout)
	for {
		exists, _, err := page.Has(loginReadySelector)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
		if surface, serr := hasLoginSurface(page); serr == nil && surface {
			return false, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		if err := page.Sleep(200 * time.Millisecond); err != nil {
			return false, err
		}
	}
}

func hasLoginSurface(page *hrod.Page) (bool, error) {
	for _, selector := range []string{loginMaskSelector, loginQRCodeSelector} {
		exists, _, err := page.Has(selector)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	pp := a.page.Context(ctx)
	if !onExplorePage(pp) {
		if err := pp.Navigate(ExploreURL); err != nil {
			return errors.Wrap(err, "navigate to explore")
		}
	}
	loggedIn, err := waitForLoginSurface(ctx, pp)
	if err != nil {
		return err
	}
	if loggedIn {
		return nil
	}

	loginCtx := ctx
	cancel := func() {}
	if _, ok := loginCtx.Deadline(); !ok {
		loginCtx, cancel = context.WithTimeout(ctx, defaultLoginWaitTimeout)
	}
	defer cancel()

	loggedIn, err = a.WaitForLogin(loginCtx)
	if err != nil && loginCtx.Err() == nil {
		return err
	}
	if !loggedIn {
		return errors.New("等待登录完成超时")
	}
	return nil
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	pp := a.page.Context(ctx)
	if !onExplorePage(pp) {
		if err := pp.Navigate(ExploreURL); err != nil {
			return "", false, errors.Wrap(err, "navigate to explore")
		}
	}
	loggedIn, err := waitForLoginSurface(ctx, pp)
	if err != nil {
		return "", false, err
	}
	if loggedIn {
		return "", true, nil
	}

	qrcode, err := waitForElement(pp, loginQRCodeSelector, defaultQRCodeWaitTimeout)
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode element failed")
	}
	src, err := qrcode.Attribute("src")
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode src failed")
	}
	if src == nil || len(*src) == 0 {
		return "", false, errors.New("qrcode src is empty")
	}

	return *src, false, nil
}

// CurrentQrcodeImage 在已有一个待扫码页面会话时复用它：已登录直接返回，二维码过期单独标记。
func (a *LoginAction) CurrentQrcodeImage(ctx context.Context) (string, bool, bool, error) {
	loggedIn, err := waitForLoginSurface(ctx, a.page.Context(ctx))
	if err != nil {
		return "", false, false, err
	}
	if loggedIn {
		return "", true, false, nil
	}
	res, err := evalJSDirect(ctx, a.page, currentQrcodeProbeScript)
	if err != nil {
		return "", false, false, err
	}
	if res == nil || res.Value.String() == "" {
		return "", false, false, errors.New("probe qrcode returned no value")
	}
	var probe struct {
		Present, Visible, Expired bool
		Src                       string
	}
	if err := json.Unmarshal([]byte(res.Value.String()), &probe); err != nil {
		return "", false, false, err
	}
	if probe.Expired || !probe.Present || !probe.Visible || strings.TrimSpace(probe.Src) == "" {
		return "", false, true, nil
	}
	return probe.Src, false, false, nil
}

const currentQrcodeProbeScript = `() => { const qr=document.querySelector(".login-container .qrcode-img"), text=document.body?document.body.innerText:"", expired=/二维码\s*(?:已)?(?:过期|失效)|刷新二维码|点击刷新(?:二维码)?/.test(text); if(!qr)return JSON.stringify({present:false,visible:false,expired}); const style=window.getComputedStyle(qr),rect=qr.getBoundingClientRect(),visible=qr.isConnected&&style.display!=="none"&&style.visibility!=="hidden"&&style.visibility!=="collapse"&&Number(style.opacity||"1")>0&&rect.width>0&&rect.height>0&&qr.getClientRects().length>0; return JSON.stringify({present:true,visible,expired,src:qr.getAttribute("src")||""}); }`

// WaitForLogin 等扫码完成：只看侧栏登录入口出现（与上游一致）。
func (a *LoginAction) WaitForLogin(ctx context.Context) (bool, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
			exists, _, err := a.page.Has(loginReadySelector)
			if err != nil {
				return false, err
			}
			if exists {
				return true, nil
			}
		}
	}
}

func waitForElement(page *hrod.Page, selector string, timeout time.Duration) (*hrod.Element, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		if err := page.Err(); err != nil {
			return nil, err
		}
		el, err := page.Element(selector)
		if err == nil && el != nil {
			return el, nil
		}
		lastErr = err
		if err := page.Sleep(300 * time.Millisecond); err != nil {
			return nil, err
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("等待元素 %s 超时(%s): %w", selector, timeout, lastErr)
	}
	return nil, fmt.Errorf("等待元素 %s 超时(%s)", selector, timeout)
}
