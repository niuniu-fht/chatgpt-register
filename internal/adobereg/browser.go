package adobereg

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"chatgpt-register/internal/proxyutil"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const accountURL = "https://account.adobe.com/"

func registerBrowser(ctx context.Context, in Input) (result *Result, err error) {
	fingerprint := newAdobeFingerprint()
	geo, geoErr := lookupAdobeGeoIP(ctx, in)
	if geoErr != nil {
		if strings.TrimSpace(in.Proxy) != "" {
			return nil, geoErr
		}
		in.logf("直连出口地理信息读取失败，继续使用新加坡浏览器参数: %v", geoErr)
	}
	locale, acceptLanguage := adobeLocale("SG")
	if geo != nil {
		locale, acceptLanguage = adobeLocale(geo.CountryCode)
		in.logf("代理预检通过: 出口 IP=%s, 位置=%s/%s, 时区=%s", geo.IP, geo.CountryCode, geo.City, geo.Timezone.ID)
	}
	profileDir, err := os.MkdirTemp("", "adobereg-profile-*")
	if err != nil {
		return nil, fmt.Errorf("创建浏览器临时配置: %w", err)
	}
	defer os.RemoveAll(profileDir)

	l := launcher.NewUserMode().
		UserDataDir(profileDir).
		RemoteDebuggingPort(0).
		NoSandbox(true).
		Delete("no-startup-window")
	if in.CloakBrowser {
		if strings.TrimSpace(in.BrowserBin) == "" {
			return nil, fmt.Errorf("CloakBrowser 路径为空")
		}
		// Keep these flags aligned with CloakBrowser's official Python wrapper.
		// The patched browser derives a coherent profile from one fresh seed.
		cloakSeed := 10000 + fingerprint.seed%90000
		timezone := "Asia/Singapore"
		if geo != nil && strings.TrimSpace(geo.Timezone.ID) != "" {
			timezone = geo.Timezone.ID
		}
		l = l.Bin(in.BrowserBin).
			Set("fingerprint", strconv.FormatUint(cloakSeed, 10)).
			Set("fingerprint-platform", "windows").
			Set("fingerprint-timezone", timezone).
			Set("fingerprint-locale", locale).
			Set("lang", locale).
			Set("ignore-gpu-blocklist", "")
		if !in.Headless {
			l = l.Set("start-maximized", "")
		}
	} else if strings.TrimSpace(in.BrowserBin) != "" {
		l = l.Bin(in.BrowserBin).Set("window-size", fingerprint.windowSize())
	} else if bin, ok := FindChrome(); ok {
		l = l.Bin(bin).Set("window-size", fingerprint.windowSize())
	}
	if !in.CloakBrowser {
		l = l.Set("disable-blink-features", "AutomationControlled").
			Set("force-webrtc-ip-handling-policy", "disable_non_proxied_udp")
	}
	if in.Headless {
		l = l.Set("headless", "new")
	} else {
		l = l.Headless(false)
	}

	proxyUser, proxyPass := "", ""
	if strings.TrimSpace(in.Proxy) != "" {
		server, user, pass, parseErr := parseProxy(in.Proxy)
		if parseErr != nil {
			return nil, parseErr
		}
		l = l.Set("proxy-server", server)
		// Keep Chromium's UDP/WebRTC and HTTP/3 traffic on the authenticated
		// proxy path; these protocols otherwise bypass an HTTP proxy and can
		// leave Adobe's client shell stuck on its loading screen.
		l = l.Set("force-webrtc-ip-handling-policy", "disable_non_proxied_udp").
			Set("disable-quic", "")
		proxyUser, proxyPass = user, pass
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动浏览器: %w", err)
	}
	// Rod otherwise emulates its built-in Mac/Chrome 114 laptop on every page,
	// overriding CloakBrowser's native UA, language and viewport fingerprint.
	browser := rod.New().ControlURL(controlURL).NoDefaultDevice()
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("连接浏览器: %w", err)
	}
	defer func() {
		defer func() { _ = recover() }()
		_ = browser.Close()
	}()

	if proxyUser != "" || proxyPass != "" {
		handleAuth := browser.HandleAuth(proxyUser, proxyPass)
		go func() {
			defer func() { _ = recover() }()
			_ = handleAuth()
		}()
	}

	// This identity UI uses React Aria. A plain page is intentional here because
	// third-party script injection can prevent the form root from mounting.
	page := browser.MustPage()
	applyAdobeGeo(page, geo)
	if in.CloakBrowser {
		_, version := adobeBrowserVersion(browser)
		in.logf("CloakBrowser 已启用: Chromium %s，原生随机指纹（种子 %d，每次启动生成）", version, 10000+fingerprint.seed%90000)
	} else if version, fingerprintErr := fingerprint.apply(page, browser, acceptLanguage); fingerprintErr != nil {
		return nil, fmt.Errorf("设置浏览器指纹: %w", fingerprintErr)
	} else {
		in.logf("浏览器指纹已应用: Chrome %s, %dx%d, CPU %d, 内存 %dGB", version,
			fingerprint.screenW, fingerprint.screenH, fingerprint.cores, fingerprint.memory)
	}
	if strings.TrimSpace(in.Proxy) != "" {
		browserIP, verifyErr := verifyAdobeBrowserEgress(page, geo.IP)
		if verifyErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrProxyRejected, verifyErr)
		}
		in.logf("浏览器代理出口确认: %s（与代理预检一致）", browserIP)
	}
	go page.EachEvent(
		func(e *proto.RuntimeExceptionThrown) {
			if e.ExceptionDetails != nil {
				in.logf("页面脚本异常: %s", e.ExceptionDetails.Text)
			}
		},
	)()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("浏览器流程异常: %v", recovered)
		}
		if err != nil && ctx.Err() == nil && in.SaveShot != nil {
			if png, shotErr := safeFailureScreenshot(browser, page); shotErr == nil && len(png) > 0 {
				in.SaveShot(png)
			}
		}
	}()

	go func() {
		<-ctx.Done()
		_ = page.Close()
	}()

	page = page.Timeout(45 * time.Second)
	in.logf("打开 Adobe 账户页面")
	if navErr := page.Navigate(accountURL); navErr != nil {
		return nil, fmt.Errorf("打开 Adobe 账户页面: %w", navErr)
	}
	time.Sleep(3 * time.Second)
	if err := openSignup(page, in); err != nil {
		return nil, err
	}
	if err := waitForSignupForm(ctx, page, in); err != nil {
		return nil, err
	}
	in.logf("注册表单已显示")

	in.logf("开始填写邮箱和密码")
	if err := fillInput(page, "#Signup-EmailField", in.Email); err != nil {
		return nil, fmt.Errorf("填写邮箱: %w", err)
	}
	in.logf("邮箱填写完成")
	if err := fillInput(page, "#Signup-PasswordField", in.Password); err != nil {
		return nil, fmt.Errorf("填写密码: %w", err)
	}
	in.logf("密码填写完成")
	if err := advanceToProfile(page, in); err != nil {
		return nil, err
	}
	page = page.CancelTimeout().Timeout(60 * time.Second)
	in.logf("开始填写个人资料")
	if err := fillInput(page, "#Signup-LastNameField", in.LastName); err != nil {
		return nil, fmt.Errorf("填写姓氏: %w", err)
	}
	if err := fillInput(page, "#Signup-FirstNameField", in.FirstName); err != nil {
		return nil, fmt.Errorf("填写名字: %w", err)
	}
	if err := fillInput(page, "#Signup-DateOfBirthChooser-Year", strconv.Itoa(in.BirthYear)); err != nil {
		return nil, fmt.Errorf("填写出生年份: %w", err)
	}
	in.logf("姓名和出生年份填写完成")
	if err := setSelect(page, `select[name="month"]`, strconv.Itoa(in.BirthMonth-1)); err != nil {
		return nil, fmt.Errorf("设置出生月份: %w", err)
	}
	if strings.TrimSpace(in.Country) != "" {
		if err := setSelect(page, `select[name="countryCode"]`, strings.ToUpper(in.Country)); err != nil {
			return nil, fmt.Errorf("设置国家地区: %w", err)
		}
	}
	in.logf("出生月份和国家地区设置完成")
	// Keep marketing email disabled independently of the page default.
	if _, optOutErr := page.CancelTimeout().Timeout(10 * time.Second).Eval(`() => {
  const box = document.querySelector('input[type="checkbox"]');
  if (box && box.checked) box.click();
}`); optOutErr != nil {
		return nil, fmt.Errorf("设置营销邮件选项: %w", optOutErr)
	}

	result = &Result{
		Email: in.Email, Password: in.Password, FirstName: in.FirstName,
		LastName: in.LastName, BirthYear: in.BirthYear,
		BirthMonth: in.BirthMonth, Country: strings.ToUpper(in.Country),
	}
	if in.DryRun {
		result.Status = "ready_to_submit"
		in.logf("资料填写完成；演练模式在提交前结束")
		return result, nil
	}

	in.logf("提交创建账户")
	createButton, buttonErr := page.CancelTimeout().Timeout(15*time.Second).ElementR("button", "创建帐户|Create account")
	if buttonErr != nil {
		return nil, fmt.Errorf("查找创建账户按钮: %w", buttonErr)
	}
	if _, clickErr := createButton.Eval(`() => {
  this.scrollIntoView({block: 'center', inline: 'nearest'});
  this.click();
}`); clickErr != nil {
		return nil, fmt.Errorf("点击创建账户按钮: %w", clickErr)
	}
	in.logf("已点击创建账户，立即检测控制台、页面错误和验证码")
	if err := waitForSuccess(ctx, browser, page, in); err != nil {
		return nil, err
	}
	result.Status = "registered"
	return result, nil
}

func waitForSignupForm(ctx context.Context, page *rod.Page, in Input) error {
	deadline := time.Now().Add(90 * time.Second)
	lastRouteAttempt := time.Time{}
	for time.Now().Before(deadline) {
		pg := page.CancelTimeout().Timeout(1500 * time.Millisecond)
		if field, err := pg.Element("#Signup-EmailField"); err == nil {
			if visible, _ := field.Visible(); visible {
				return nil
			}
		}
		if link, err := pg.ElementR("a", "创建帐户|创建账户|Create an account"); err == nil {
			if visible, _ := link.Visible(); visible {
				in.logf("登录页仍在显示，再次点击创建账户入口")
				_, _ = link.Eval(`() => this.click()`)
			}
		}
		if lastRouteAttempt.IsZero() || time.Since(lastRouteAttempt) >= 15*time.Second {
			if currentURL := pageURL(pg); strings.Contains(currentURL, "auth.services.adobe.com/") {
				signupURL := strings.SplitN(currentURL, "#", 2)[0] + "#/signup"
				_, _ = pg.Eval(`(url) => { if (location.href !== url) location.href = url; }`, signupURL)
				lastRouteAttempt = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("等待注册表单超时（当前地址=%s）", pageURL(page.CancelTimeout().Timeout(2*time.Second)))
}

func openSignup(page *rod.Page, in Input) error {
	const signupEntryWait = 30 * time.Second
	deadline := time.Now().Add(signupEntryWait)
	lastURL, lastProgress := "", time.Time{}
	for time.Now().Before(deadline) {
		pg := page.CancelTimeout().Timeout(1500 * time.Millisecond)
		if field, err := pg.Element("#Signup-EmailField"); err == nil {
			if visible, _ := field.Visible(); visible {
				return nil
			}
		}
		if link, err := pg.ElementR("a", "创建帐户|创建账户|Create an account"); err == nil {
			if visible, _ := link.Visible(); visible {
				in.logf("创建账户入口已显示，点击进入注册表单")
				if _, clickErr := link.Eval(`() => this.click()`); clickErr != nil {
					return fmt.Errorf("点击创建账户入口: %w", clickErr)
				}
				return nil
			}
		}
		currentURL := pageURL(pg)
		if currentURL != "" && currentURL != lastURL {
			in.logf("页面已跳转到: %s", currentURL)
			lastURL = currentURL
		}
		if lastProgress.IsZero() || time.Since(lastProgress) >= 5*time.Second {
			in.logf("等待 Adobe 创建账户入口加载")
			lastProgress = time.Now()
		}
		time.Sleep(500 * time.Millisecond)
	}

	currentURL := pageURL(page.CancelTimeout().Timeout(2 * time.Second))
	if strings.Contains(currentURL, "auth.services.adobe.com/") {
		signupURL := strings.SplitN(currentURL, "#", 2)[0] + "#/signup"
		in.logf("等待 30 秒后入口仍未显示，使用当前会话的注册路由")
		if err := page.CancelTimeout().Timeout(signupEntryWait).Navigate(signupURL); err != nil {
			return fmt.Errorf("打开注册路由: %w", err)
		}
		return nil
	}
	err := fmt.Errorf("等待创建账户入口超时（当前地址=%s）", currentURL)
	if strings.TrimSpace(in.Proxy) != "" {
		return fmt.Errorf("%w: %v", ErrProxyRejected, err)
	}
	return err
}

func pageURL(page *rod.Page) string {
	result, err := page.Eval(`() => location.href`)
	if err == nil {
		if current := strings.TrimSpace(result.Value.Str()); current != "" {
			return current
		}
	}
	if info, infoErr := page.Info(); infoErr == nil {
		return strings.TrimSpace(info.URL)
	}
	return ""
}

func failureScreenshot(browser *rod.Browser, preferred *rod.Page) ([]byte, error) {
	if preferred != nil {
		if png, err := preferred.CancelTimeout().Timeout(5*time.Second).Screenshot(false, nil); err == nil && len(png) > 0 {
			return png, nil
		}
	}
	pages, err := browser.Context(context.Background()).Pages()
	if err != nil {
		return nil, err
	}
	for i := len(pages) - 1; i >= 0; i-- {
		if png, shotErr := pages[i].CancelTimeout().Timeout(5*time.Second).Screenshot(false, nil); shotErr == nil && len(png) > 0 {
			return png, nil
		}
	}
	return nil, fmt.Errorf("未找到可截图的浏览器页面")
}

func safeFailureScreenshot(browser *rod.Browser, preferred *rod.Page) (png []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			png = nil
			err = fmt.Errorf("失败截图异常: %v", recovered)
		}
	}()
	return failureScreenshot(browser, preferred)
}

func advanceToProfile(page *rod.Page, in Input) error {
	for attempt := 1; attempt <= 3; attempt++ {
		if isAccountAlreadyExistsText(quickBodyText(page.CancelTimeout().Timeout(750 * time.Millisecond))) {
			return ErrAccountAlreadyExists
		}
		pg := page.CancelTimeout().Timeout(8 * time.Second)
		button, err := pg.ElementR("button", `^\s*(Continue|继续)\s*$`)
		if err != nil {
			if profileVisible(page) {
				in.logf("个人资料页面已显示")
				return nil
			}
			return fmt.Errorf("查找 Continue 按钮: %w", err)
		}
		if _, err := button.Eval(`() => {
  this.scrollIntoView({block: 'center', inline: 'nearest'});
  this.click();
}`); err != nil {
			return fmt.Errorf("点击 Continue 按钮: %w", err)
		}
		in.logf("已点击 Continue（第 %d 次），等待个人资料页面", attempt)

		attemptDeadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(attemptDeadline) {
			if profileVisible(page) {
				time.Sleep(750 * time.Millisecond)
				in.logf("个人资料页面已显示")
				return nil
			}
			if isAccountAlreadyExistsText(quickBodyText(page.CancelTimeout().Timeout(750 * time.Millisecond))) {
				return ErrAccountAlreadyExists
			}
			time.Sleep(500 * time.Millisecond)
		}
		in.logf("Continue 页面仍然显示，准备再次点击")
	}
	return fmt.Errorf("连续点击 Continue 后个人资料页面仍未显示")
}

func isAccountAlreadyExistsText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "account exists") ||
		strings.Contains(text, "账户已存在") ||
		strings.Contains(text, "帐号已存在") ||
		strings.Contains(text, "账号已存在")
}

func profileVisible(page *rod.Page) bool {
	profile, err := page.CancelTimeout().Timeout(1200 * time.Millisecond).Element("#Signup-LastNameField")
	if err != nil {
		return false
	}
	visible, err := profile.Visible()
	return err == nil && visible
}

func fillInput(page *rod.Page, selector, value string) error {
	pg := page.CancelTimeout().Timeout(10 * time.Second)
	element, err := pg.Element(selector)
	if err != nil {
		return fmt.Errorf("未找到输入框 %s: %w", selector, err)
	}
	if err := element.WaitVisible(); err != nil {
		return fmt.Errorf("输入框 %s 未显示: %w", selector, err)
	}
	_, err = element.Eval(`(value) => {
  this.focus();
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
  setter.call(this, value);
  this.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'insertText', data: value}));
  this.dispatchEvent(new Event('change', {bubbles: true}));
  this.blur();
}`, value)
	if err != nil {
		return fmt.Errorf("写入输入框 %s: %w", selector, err)
	}
	return nil
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

func setSelect(page *rod.Page, selector, value string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		pg := page.CancelTimeout().Timeout(8 * time.Second)
		el, err := pg.Element(selector)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		_, err = el.Eval(`(value) => {
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set;
			setter.call(this, value);
			this.dispatchEvent(new Event('input', {bubbles: true}));
			this.dispatchEvent(new Event('change', {bubbles: true}));
		}`, value)
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return lastErr
}

func waitForSuccess(ctx context.Context, browser *rod.Browser, page *rod.Page, in Input) error {
	deadline := time.Now().Add(90 * time.Second)
	captchaAttempted := false
	lastProgressLog := time.Now()
	for time.Now().Before(deadline) {
		if adobeConsoleOpen(browser, in.Email) {
			in.logf("已进入 Adobe 账户控制台")
			return nil
		}
		pg := page.CancelTimeout().Timeout(750 * time.Millisecond)
		info, infoErr := pg.Info()
		if infoErr == nil {
			if isAccountConsoleURL(info.URL) {
				pg := page.CancelTimeout().Timeout(10 * time.Second)
				if _, headingErr := pg.ElementR("h1", "欢迎|Welcome"); headingErr == nil {
					return nil
				}
			}
		}
		text := quickBodyText(pg)
		lower := strings.ToLower(text)
		if isAdobeConsoleText(text, in.Email) {
			in.logf("已进入 Adobe 账户控制台")
			return nil
		}
		if isAccountAlreadyExistsText(text) {
			return ErrAccountAlreadyExists
		}
		if captchaText(lower) || quickCaptchaFrame(pg) || quickStartPuzzle(pg) {
			if in.Captcha == nil {
				return fmt.Errorf("captcha_required")
			}
			if !captchaAttempted {
				in.logf("检测到验证码窗口")
				captchaAttempted = true
				if err := solveArkoseCaptcha(ctx, page, in); err != nil {
					return fmt.Errorf("验证码识别: %w", err)
				}
				deadline = time.Now().Add(60 * time.Second)
				lastProgressLog = time.Now()
				continue
			}
			if clicked, _ := clickVisibleDeepButton(page, "Try again", 400*time.Millisecond); clicked {
				in.logf("上一组验证码未通过，已点击 Try again，继续下一组")
				captchaAttempted = false
				time.Sleep(2 * time.Second)
				continue
			}
		}
		if time.Since(lastProgressLog) >= 10*time.Second {
			in.logf("仍在等待 Adobe 返回结果，已持续 %d 秒", 90-int(time.Until(deadline).Seconds()))
			lastProgressLog = time.Now()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("等待 Adobe 账户控制台超时")
}

func adobeConsoleOpen(browser *rod.Browser, email string) (ready bool) {
	defer func() { _ = recover() }()
	if adobeConsoleTargetOpen(browser) {
		return true
	}
	pages, err := browser.Context(context.Background()).Pages()
	if err != nil {
		return false
	}
	for _, candidate := range pages {
		if adobeConsolePage(candidate, email) {
			return true
		}
	}
	return false
}

func adobeConsoleTargetOpen(browser *rod.Browser) (ready bool) {
	defer func() { _ = recover() }()
	targets, targetErr := proto.TargetGetTargets{}.Call(browser)
	if targetErr == nil {
		for _, target := range targets.TargetInfos {
			if target != nil && isAccountConsoleURL(target.URL) {
				return true
			}
		}
	}
	return false
}

// A closed or replaced target can panic inside Rod. Keep that failure scoped to
// one tab so a later Adobe Account tab can still be inspected.
func adobeConsolePage(page *rod.Page, email string) (ready bool) {
	defer func() { _ = recover() }()
	pg := page.CancelTimeout().Timeout(1500 * time.Millisecond)
	if info, err := pg.Info(); err == nil && isAccountConsoleURL(info.URL) {
		return true
	}
	return isAdobeConsoleText(quickBodyText(pg), email)
}

func isAdobeConsoleText(text, email string) bool {
	lower := strings.ToLower(strings.Join(strings.Fields(text), " "))
	welcome := strings.Contains(lower, "welcome to your account") ||
		strings.Contains(text, "欢迎使用您的账户") || strings.Contains(text, "欢迎来到您的账户")
	identity := strings.TrimSpace(email) == "" || strings.Contains(lower, strings.ToLower(strings.TrimSpace(email)))
	return welcome && identity
}

func quickStartPuzzle(page *rod.Page) bool {
	_, err := deepSearchFirst(page, "Start puzzle", 700*time.Millisecond)
	return err == nil
}

func quickBodyText(page *rod.Page) string {
	result, err := page.Eval(`() => document.body ? document.body.innerText : ''`)
	if err != nil {
		return ""
	}
	return result.Value.Str()
}

func quickCaptchaFrame(page *rod.Page) bool {
	result, err := page.Eval(`() => Array.from(document.querySelectorAll('iframe')).some(frame => {
  const value = ((frame.src || '') + ' ' + (frame.title || '')).toLowerCase();
  const rect = frame.getBoundingClientRect();
  const style = getComputedStyle(frame);
  const visible = rect.width >= 100 && rect.height >= 100 &&
    style.display !== 'none' && style.visibility !== 'hidden' && Number(style.opacity || 1) > 0;
  return visible && (value.includes('arkose') || value.includes('funcaptcha') ||
    value.includes('verification challenge') || value.includes('puzzle'));
})`)
	return err == nil && result.Value.Bool()
}

func isAccountConsoleURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Hostname(), "account.adobe.com")
}

func hasCaptchaFrame(page *rod.Page) bool {
	return pageHasCaptcha(page, 0)
}

func pageHasCaptcha(page *rod.Page, depth int) bool {
	if depth > 2 {
		return false
	}
	pg := page.CancelTimeout().Timeout(2 * time.Second)
	if info, err := pg.Info(); err == nil && captchaText(info.URL) {
		return true
	}
	if body, err := pg.Element("body"); err == nil {
		if text, textErr := body.Text(); textErr == nil && captchaText(text) {
			return true
		}
	}
	frames, err := pg.Elements("iframe")
	if err != nil {
		return false
	}
	for _, frame := range frames {
		src, _ := frame.Attribute("src")
		title, _ := frame.Attribute("title")
		value := strings.ToLower(valueOrEmpty(src) + " " + valueOrEmpty(title))
		if captchaText(value) {
			return true
		}
		if child, frameErr := frame.Frame(); frameErr == nil && pageHasCaptcha(child, depth+1) {
			return true
		}
	}
	return false
}

func captchaText(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "arkose") || strings.Contains(value, "funcaptcha") ||
		strings.Contains(value, "solve a few puzzles") || strings.Contains(value, "start puzzle")
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// FindChrome returns an installed stable Chrome binary when available.
func FindChrome() (string, bool) {
	candidates := []string{"chrome", "google-chrome", "/usr/bin/google-chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err == nil && strings.Contains(strings.ToLower(filepath.Base(path)), "chrome") {
			return path, true
		}
	}
	return "", false
}

func parseProxy(raw string) (server, user, pass string, err error) {
	return proxyutil.ServerAndAuth(raw)
}
