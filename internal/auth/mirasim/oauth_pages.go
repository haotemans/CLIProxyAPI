// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): fixed HTML pages for the
// browser login (start/provider chooser, email code entry, callback pages).
package mirasim

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// pastedCallbackField names the start page's form field. The form is GET-only,
// so the pasted callback URL travels in the query string; the request log
// masks every query value whose name contains "token", and this name does so
// the credentials in it are masked with the rest.
const pastedCallbackField = "token_url"

// emailAddressField names the start page's address field and emailCodeField
// names the code-entry page's code field. Both forms are GET-only; the values
// travel in the query string and are partially masked by request logging.
const (
	emailAddressField = "token_email"
	emailCodeField    = "token_code"
)

// pastedCallbackResult reads the Mirasim callback URL an operator pasted into
// the start page. It refuses anything that is not recognisably that callback
// (such as the authorize URL or a truncated copy), so a slip on the
// operator's part is reported without spending the login. The host part is
// ignored: it is 127.0.0.1 as Mirasim sent it, or the panel address if the
// operator already edited it.
func pastedCallbackResult(raw string) (browserCallbackResult, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 4*64<<10 {
		return browserCallbackResult{}, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(parsed.Path, BrowserCallbackPath) {
		return browserCallbackResult{}, false
	}
	result := callbackResultFromValues(parsed.Query())
	if result.state == "" || (result.accessToken == "" && result.errorMessage == "") {
		return browserCallbackResult{}, false
	}
	return result, true
}

type browserProviderButton struct {
	ID      string
	Label   string
	Default bool
}

type startPageData struct {
	State       string
	Providers   []browserProviderButton
	CallbackURL string
	Field       string
	EmailField  string
	Minutes     int
}

// The form action and the provider links are relative so that they resolve
// against the address this page was opened on, including any path prefix a
// reverse proxy adds in front of this server.
var startPageTemplate = template.Must(template.New("start").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
	`<style>body{font:16px system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1.25rem;color:#171717}h1{font-size:1.6rem;margin-bottom:.25rem}` +
	`p{color:#333;margin:.4rem 0}.en{color:#777;font-size:.88rem}ol{padding-left:1.25rem}li{margin:1.5rem 0}` +
	`.button,button{display:inline-block;background:#171717;color:#fff;border:2px solid #171717;border-radius:6px;padding:.6rem 1rem;font:inherit;text-decoration:none;cursor:pointer}` +
	`.button.default{border-color:#d97706;box-shadow:0 0 0 2px #d97706}` +
	`.providers{display:flex;flex-direction:column;gap:.5rem;margin:.75rem 0}` +
	`input{box-sizing:border-box;width:100%;padding:.55rem;margin:.6rem 0;font:14px ui-monospace,monospace;border:1px solid #bbb;border-radius:6px}` +
	`.note{border-left:3px solid #d97706;padding-left:.75rem;margin-top:2rem}</style>` +
	`<title>Mirasim 登录 / Mirasim sign-in</title></head><body>` +
	`<h1>Mirasim 登录</h1><p class="en">Mirasim sign-in</p><ol>` +
	`<li><p>选择一种登录方式：</p>` +
	`<p class="en">Choose a sign-in method. It opens in a new tab.</p>` +
	`<div class="providers">{{range .Providers}}<a class="button{{if .Default}} default{{end}}" data-provider="{{.ID}}" href="authorize?state={{$.State}}&amp;provider={{.ID}}" target="_blank" rel="noopener noreferrer">Continue with {{.Label}}{{if .Default}} (default){{end}}</a>{{end}}</div>` +
	`<p>没有绑定第三方账号？用邮箱验证码登录：</p>` +
	`<p class="en">No provider bound to the account? Sign in with a code sent to your email.</p>` +
	`<form method="get" action="email/send"><input type="hidden" name="state" value="{{.State}}"><input type="email" name="{{.EmailField}}" required autocomplete="email" spellcheck="false" placeholder="you@example.com">` +
	`<button type="submit">发送验证码 / Email me a code</button></form></li>` +
	`<li><p>授权完成后，那个标签页会显示“无法访问此网站”或“拒绝连接”。复制它地址栏里的完整地址，粘贴到下面，然后点“完成登录”。</p>` +
	`<p class="en">After authorizing, that tab shows a "can't be reached" page. Copy the full address from its address bar, paste it below and press the button.</p>` +
	`<form method="get" action="callback"><input type="text" name="{{.Field}}" required autocomplete="off" spellcheck="false" placeholder="{{.CallbackURL}}&amp;access_token=…">` +
	`<button type="submit">完成登录 / Complete sign-in</button></form></li></ol>` +
	`<div class="note"><p>如果授权后直接显示“Mirasim sign-in complete”，就不需要第 2 步。不要使用管理面板里的“提交回调 URL”，它处理不了 Mirasim 的回调。</p>` +
	`<p class="en">If authorizing already shows "Mirasim sign-in complete", skip step 2. Do not use Management Center's "submit callback URL" box; it cannot complete a Mirasim sign-in.</p>` +
	`<p>本次登录 {{.Minutes}} 分钟内有效。</p><p class="en">This sign-in expires {{.Minutes}} minutes after it was started.</p></div>` +
	`</body></html>`))

func renderStartPage(data startPageData) ([]byte, error) {
	var body bytes.Buffer
	if err := startPageTemplate.Execute(&body, data); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// emailCodePageResponse renders the code-entry form for one login. Only the
// login's own state and its remaining code sends are placed in it, so neither
// the address nor the code ever reaches the browser through the page. A
// rate-limited resend renders the same form, with limited set when the
// interval is what blocked it.
func emailCodePageResponse(state string, status int, retry, limited bool, remaining int) (int, []byte) {
	var body bytes.Buffer
	data := emailCodePageData{State: state, Field: emailCodeField, Retry: retry, Limited: limited, Remaining: remaining, Max: maxEmailCodeSends, Minutes: int(browserLoginTTL.Minutes())}
	if err := emailCodePageTemplate.Execute(&body, data); err != nil {
		return http.StatusInternalServerError, []byte(callbackNotFoundPage)
	}
	return status, body.Bytes()
}

type emailCodePageData struct {
	State     string
	Field     string
	Retry     bool
	Limited   bool
	Remaining int
	Max       int
	Minutes   int
}

// emailCodePageTemplate asks for the code Mirasim mailed. The action is
// relative so it resolves against the address this page was opened on.
var emailCodePageTemplate = template.Must(template.New("email-code").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
	`<style>body{font:16px system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1.25rem;color:#171717}h1{font-size:1.6rem;margin-bottom:.25rem}` +
	`p{color:#333;margin:.4rem 0}.en{color:#777;font-size:.88rem}.notice{color:#92400e}` +
	`button{display:inline-block;background:#171717;color:#fff;border:2px solid #171717;border-radius:6px;padding:.6rem 1rem;font:inherit;cursor:pointer}` +
	`input{box-sizing:border-box;width:100%;padding:.55rem;margin:.6rem 0;font:14px ui-monospace,monospace;border:1px solid #bbb;border-radius:6px}</style>` +
	`<title>Mirasim 验证码登录 / Mirasim sign-in code</title></head><body>` +
	`<h1>输入验证码</h1><p class="en">Enter the sign-in code</p>` +
	`<p>如果该邮箱已有 Mirasim 账号，验证码已发送。请在下方输入。</p>` +
	`<p class="en">If that address has a Mirasim account, a sign-in code was mailed to it. Enter it below.</p>` +
	`{{if .Retry}}<p class="notice">验证码未通过，请重试。</p><p class="en notice">That code was not accepted. Try again.</p>{{end}}` +
	`{{if .Limited}}<p class="notice">请求验证码过于频繁：两次发送需间隔一分钟。已收到的验证码仍可在下方输入。</p><p class="en notice">Too many code requests: sends are one minute apart. A code you already received can still be entered below.</p>{{end}}` +
	`<form method="get" action="verify"><input type="hidden" name="state" value="{{.State}}"><input type="text" name="{{.Field}}" required autocomplete="one-time-code" inputmode="numeric" spellcheck="false" placeholder="123456">` +
	`<button type="submit">完成登录 / Verify code</button></form>` +
	`{{if .Remaining}}<form method="get" action="send"><input type="hidden" name="state" value="{{.State}}"><button type="submit">重新发送验证码 / Resend code</button></form>` +
	`<p>本次登录还可发送 {{.Remaining}} 次验证码（最多 {{.Max}} 次），两次发送间隔一分钟。</p><p class="en">{{.Remaining}} of {{.Max}} code sends remain for this sign-in; sends are one minute apart.</p>{{else}}` +
	`<p class="notice">本次登录的验证码发送次数已用完；如仍未收到验证码，请重新开始登录。</p><p class="en notice">No code sends remain for this sign-in; if the mail does not arrive, start the sign-in again.</p>{{end}}` +
	`<p>本次登录 {{.Minutes}} 分钟内有效。</p><p class="en">This sign-in expires {{.Minutes}} minutes after it was started.</p>` +
	`</body></html>`))

// BrowserHeaders is the fixed security header set for every browser page.
func BrowserHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "text/html; charset=utf-8")
	headers.Set("Cache-Control", "no-store")
	headers.Set("Referrer-Policy", "no-referrer")
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	return headers
}

// FormPageHeaders allows the form pages to submit back to this same origin.
func FormPageHeaders() http.Header {
	headers := BrowserHeaders()
	headers.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	return headers
}

// The callback pages are fixed documents with no interpolation, so no
// callback value can reach a browser through them.
const (
	callbackPagePrefix = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<style>body{font:16px system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1.25rem;color:#171717}h1{font-size:1.6rem}p{color:#555}</style>`

	callbackCompletePage = callbackPagePrefix + `<title>Mirasim sign-in complete</title></head><body>` +
		`<h1>Mirasim sign-in complete</h1><p>Return to CLIProxyAPI. You may close this window.</p></body></html>`

	callbackFailedPage = callbackPagePrefix + `<title>Mirasim sign-in failed</title></head><body>` +
		`<h1>Mirasim sign-in failed</h1><p>Mirasim did not return a usable sign-in. No credentials were saved. You may close this window and try again.</p></body></html>`

	callbackStatePage = callbackPagePrefix + `<title>Invalid OAuth state</title></head><body>` +
		`<h1>Invalid OAuth state</h1><p>This sign-in callback does not belong to the login in progress.</p></body></html>`

	callbackUsedPage = callbackPagePrefix + `<title>Callback already used</title></head><body>` +
		`<h1>This callback was already used</h1><p>Start the login again if you need another sign-in.</p></body></html>`

	callbackNotFoundPage = callbackPagePrefix + `<title>Not found</title></head><body>` +
		`<h1>Not found</h1><p>This is not a Mirasim sign-in address.</p></body></html>`

	startExpiredPage = callbackPagePrefix + `<title>Sign-in expired</title></head><body>` +
		`<h1>This sign-in has expired</h1><p>Start the Mirasim login again from Management Center.</p>` +
		`<p>登录已过期或不存在，请回到管理面板重新开始 Mirasim 登录。</p></body></html>`

	callbackPastePage = callbackPagePrefix + `<title>Not a Mirasim callback</title></head><body>` +
		`<h1>That is not the Mirasim callback address</h1><p>Go back and paste the full address shown in the address bar of the tab that could not be reached after authorizing. The sign-in is still waiting.</p>` +
		`<p>粘贴的内容不是 Mirasim 的回调地址。请返回上一页，粘贴授权后那个“无法访问”标签页地址栏里的完整地址。本次登录仍然有效。</p></body></html>`

	authorizeProviderPage = callbackPagePrefix + `<title>Sign-in method unavailable</title></head><body>` +
		`<h1>That sign-in method is not available</h1><p>Go back to the Mirasim sign-in page and choose one of the methods listed there. The sign-in is still waiting.</p>` +
		`<p>该登录方式当前不可用。请返回登录页面重新选择，本次登录仍然有效。</p></body></html>`

	authorizeUnavailablePage = callbackPagePrefix + `<title>Sign-in unavailable</title></head><body>` +
		`<h1>Mirasim sign-in is temporarily unavailable</h1><p>The Mirasim authentication service address is not usable. Ask the operator to check it, then start the sign-in again.</p></body></html>`

	emailAddressPage = callbackPagePrefix + `<title>Invalid email address</title></head><body>` +
		`<h1>Enter a valid email address</h1><p>Go back to the Mirasim sign-in page and enter the address of an account that signs in by mail. The sign-in is still waiting.</p>` +
		`<p>请输入有效的邮箱地址。请返回登录页面重新填写，本次登录仍然有效。</p></body></html>`

	emailAddressPinnedPage = callbackPagePrefix + `<title>Address already bound</title></head><body>` +
		`<h1>This sign-in is bound to another address</h1><p>The first address a code was mailed to owns this sign-in. Start the Mirasim login again from Management Center to use a different address.</p>` +
		`<p>本次登录已绑定到最先收到验证码的邮箱，不能再改用其他地址。如需更换，请回到管理面板重新开始 Mirasim 登录。</p></body></html>`

	emailSendFailedPage = callbackPagePrefix + `<title>Code not sent</title></head><body>` +
		`<h1>Mirasim did not send the code</h1><p>The sign-in is still waiting. Wait a minute and request another code from the Mirasim sign-in page.</p>` +
		`<p>Mirasim 未能发送验证码。本次登录仍然有效，请稍后重试。</p></body></html>`

	emailNoCodePage = callbackPagePrefix + `<title>No code requested</title></head><body>` +
		`<h1>No code was requested</h1><p>Go back to the Mirasim sign-in page and request a code first. The sign-in is still waiting.</p>` +
		`<p>尚未请求验证码。请返回登录页面先发送验证码，本次登录仍然有效。</p></body></html>`

	emailAttemptsPage = callbackPagePrefix + `<title>Too many code attempts</title></head><body>` +
		`<h1>Too many code attempts</h1><p>This sign-in failed. Start the Mirasim login again from Management Center.</p>` +
		`<p>验证码尝试次数过多，本次登录已失败。请回到管理面板重新开始 Mirasim 登录。</p></body></html>`
)
