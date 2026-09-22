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
	loginQRCodeSelector      = ".login-container .qrcode-img"
	loginMaskSelector        = ".login-container"
	loginMaskClassSelector   = "[class*='login-mask']"
	loginStateEvalTimeout    = 2 * time.Second
	defaultLoginWaitTimeout  = 4 * time.Minute
	defaultQRCodeWaitTimeout = 30 * time.Second
)

type LoginAction struct {
	page *hrod.Page
}

func NewLogin(page *hrod.Page) *LoginAction {
	return &LoginAction{page: page}
}

type loginPageState struct {
	Authenticated *bool  `json:"authenticated"`
	Guest         *bool  `json:"guest"`
	UserID        string `json:"userId"`
	Nickname      string `json:"nickname"`
}
func (s loginPageState) authenticated() bool {
	return s.Authenticated != nil && *s.Authenticated &&
		s.Guest != nil && !*s.Guest && strings.TrimSpace(s.UserID) != ""
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
const loginPageStateScript = `() => { const raw=((window.__INITIAL_STATE__||{}).user||{}).userInfo; let info=raw; if(info&&typeof info==="object"&&info.value!==undefined)info=info.value; if(info&&typeof info==="object"&&info.ref&&typeof info.ref==="object")info=info.ref; if(info&&typeof info==="object"&&info.value!==undefined)info=info.value; const auth=info&&(info.authenticated===true||info.isAuthenticated===true?true:info.authenticated===false||info.isAuthenticated===false?false:null); return JSON.stringify({authenticated:auth,guest:info&&typeof info.guest==="boolean"?info.guest:null,userId:info&&(info.userId||info.user_id||info.userid)||"",nickname:info&&(info.nickname||info.nickName)||""}); }`
func (a *LoginAction) readPageState(ctx context.Context) (loginPageState, error) {
	res, err := a.page.Context(ctx).Eval(loginPageStateScript)
	if err != nil {
		return loginPageState{}, err
	}
	if res == nil {
		return loginPageState{}, errors.New("read login state returned no value")
	}
	return parseLoginPageState(res.Value.String())
}

func hasLoginMask(page *hrod.Page) (bool, error) {
	for _, selector := range []string{loginMaskSelector, loginMaskClassSelector, loginQRCodeSelector} {
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

func (a *LoginAction) pageState(ctx context.Context) (loginPageState, bool, error) {
	if err := ctx.Err(); err != nil {
		return loginPageState{}, false, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, loginStateEvalTimeout)
	defer cancel()
	page := a.page.Context(probeCtx)
	masked, err := hasLoginMask(page)
	if err != nil {
		return loginPageState{}, false, err
	}
	if err := probeCtx.Err(); err != nil {
		return loginPageState{}, false, err
	}
	if masked {
		return loginPageState{}, true, nil
	}
	state, err := a.readPageState(probeCtx)
	if err != nil {
		return loginPageState{}, false, err
	}
	if err := probeCtx.Err(); err != nil {
		return loginPageState{}, false, err
	}
	return state, false, nil
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	pp := a.page.Context(ctx)

	if !onExplorePage(pp) {
		if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
			return false, errors.Wrap(err, "navigate to explore")
		}
	}
	return waitForLoginState(ctx, pp, 3*time.Second, 300*time.Millisecond)
}

type CurrentUser struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"userId"`
}

func (a *LoginAction) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	state, masked, err := a.pageState(ctx)
	if err != nil {
		return nil, err
	}
	if masked || !state.authenticated() {
		return nil, errors.New("current user not found in page state")
	}
	return &CurrentUser{Nickname: state.Nickname, UserID: strings.TrimSpace(state.UserID)}, nil
}

const loginSurfaceWaitTimeout = 8 * time.Second

func waitForLoginState(ctx context.Context, page *hrod.Page, timeout, pause time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	a := NewLogin(page.Context(ctx))
	for {
		state, masked, err := a.pageState(ctx)
		if err != nil {
			return false, err
		}
		if masked {
			return false, nil
		}
		if state.authenticated() {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		if err := page.Context(ctx).Sleep(pause); err != nil {
			return false, err
		}
	}
}

func waitForLoginSurface(ctx context.Context, page *hrod.Page) (bool, error) {
	return waitForLoginState(ctx, page, loginSurfaceWaitTimeout, 200*time.Millisecond)
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

	if loggedIn, err := a.WaitForLogin(loginCtx); err == nil && loggedIn {
		return nil
	} else if err != nil && loginCtx.Err() == nil {
		return err
	}
	if err := loginCtx.Err(); err != nil {
		return err
	}
	return errors.New("等待登录完成超时")
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

func (a *LoginAction) CurrentQrcodeImage(ctx context.Context) (string, bool, bool, error) {
	state, masked, err := a.pageState(ctx)
	if err != nil {
		return "", false, false, err
	}
	if state.authenticated() && !masked {
		return "", true, false, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, loginStateEvalTimeout)
	defer cancel()
	res, err := a.page.Context(probeCtx).Eval(currentQrcodeProbeScript)
	if err != nil {
		return "", false, false, err
	}
	if res == nil || res.Value.String() == "" {
		return "", false, false, errors.New("probe qrcode returned no value")
	}
	var probe struct{ Present, Visible, Expired bool; Src string }
	if err := json.Unmarshal([]byte(res.Value.String()), &probe); err != nil {
		return "", false, false, err
	}
	if err := probeCtx.Err(); err != nil {
		return "", false, false, err
	}
	if probe.Expired || !probe.Present || !probe.Visible || strings.TrimSpace(probe.Src) == "" {
		return "", false, true, nil
	}
	return probe.Src, false, false, nil
}

const currentQrcodeProbeScript = `() => { const qr=document.querySelector(".login-container .qrcode-img"), text=document.body?document.body.innerText:"", expired=/二维码\s*(?:已)?(?:过期|失效)|刷新二维码|点击刷新(?:二维码)?/.test(text); if(!qr)return JSON.stringify({present:false,visible:false,expired}); const style=window.getComputedStyle(qr),rect=qr.getBoundingClientRect(),visible=qr.isConnected&&style.display!=="none"&&style.visibility!=="hidden"&&style.visibility!=="collapse"&&Number(style.opacity||"1")>0&&rect.width>0&&rect.height>0&&qr.getClientRects().length>0; return JSON.stringify({present:true,visible,expired,src:qr.getAttribute("src")||""}); }`

func (a *LoginAction) WaitForLogin(ctx context.Context) (bool, error) {
	pp := a.page.Context(ctx)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
			state, masked, err := a.pageState(ctx)
			if err != nil {
				return false, err
			}
			if masked {
				continue
			}
			if state.authenticated() {
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
