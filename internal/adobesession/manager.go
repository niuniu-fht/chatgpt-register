package adobesession

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const defaultStartURL = "https://account.adobe.com/"

type StartInput struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	StartURL   string `json:"start_url"`
	ProfileDir string `json:"profile_dir"`
}

type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

type Snapshot struct {
	Running     bool           `json:"running"`
	Phase       string         `json:"phase"`
	Message     string         `json:"message"`
	CurrentURL  string         `json:"current_url"`
	ChromePath  string         `json:"chrome_path"`
	Email       string         `json:"email"`
	Environment map[string]any `json:"environment,omitempty"`
	Events      []Event        `json:"events"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
}

type Manager struct {
	mu          sync.RWMutex
	running     bool
	phase       string
	message     string
	currentURL  string
	chromePath  string
	email       string
	environment map[string]any
	events      []Event
	startedAt   *time.Time
	cancel      context.CancelFunc
	browser     *rod.Browser
	page        *rod.Page
}

func New() *Manager {
	return &Manager{phase: "idle", message: "等待启动", events: make([]Event, 0, 64)}
}

func (m *Manager) Start(in StartInput) error {
	in.Email = strings.TrimSpace(in.Email)
	if !strings.Contains(in.Email, "@") {
		return errors.New("邮箱格式无效")
	}
	if in.Password == "" {
		return errors.New("密码不能为空")
	}
	if strings.TrimSpace(in.StartURL) == "" {
		in.StartURL = defaultStartURL
	}
	if !strings.HasPrefix(in.StartURL, "https://") {
		return errors.New("入口地址必须使用 https://")
	}
	chromePath, ok := findChrome()
	if !ok {
		return errors.New("未检测到 Google Chrome 正式版")
	}
	if strings.TrimSpace(in.ProfileDir) == "" {
		in.ProfileDir = filepath.Join(".", "adobe-chrome-profile")
	}
	profileDir, err := filepath.Abs(in.ProfileDir)
	if err != nil {
		return fmt.Errorf("解析配置目录: %w", err)
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("已有 Adobe 注册会话正在运行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	m.running = true
	m.phase = "starting"
	m.message = "正在启动本机 Chrome"
	m.currentURL = ""
	m.chromePath = chromePath
	m.email = in.Email
	m.environment = nil
	m.events = nil
	m.startedAt = &now
	m.cancel = cancel
	m.mu.Unlock()

	m.addEvent("system", "已创建独立 Chrome 会话")
	go m.run(ctx, in, profileDir, chromePath)
	return nil
}

func (m *Manager) Stop() {
	m.mu.RLock()
	cancel := m.cancel
	browser := m.browser
	m.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if browser != nil {
		_ = browser.Close()
	}
	m.finish("stopped", "会话已停止")
}

func (m *Manager) Focus() error {
	m.mu.RLock()
	page := m.page
	running := m.running
	m.mu.RUnlock()
	if !running || page == nil {
		return errors.New("当前没有运行中的浏览器会话")
	}
	_, err := page.Activate()
	return err
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	events := append([]Event(nil), m.events...)
	env := make(map[string]any, len(m.environment))
	for k, v := range m.environment {
		env[k] = v
	}
	return Snapshot{
		Running: m.running, Phase: m.phase, Message: m.message,
		CurrentURL: m.currentURL, ChromePath: m.chromePath, Email: m.email,
		Environment: env, Events: events, StartedAt: m.startedAt,
	}
}

func (m *Manager) run(ctx context.Context, in StartInput, profileDir, chromePath string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.fail(fmt.Errorf("浏览器会话异常: %v", recovered))
		}
	}()
	l := launcher.NewUserMode().Bin(chromePath).UserDataDir(profileDir).
		RemoteDebuggingPort(0).Headless(false).Set("disable-extensions").
		Set("no-first-run").Set("no-default-browser-check")
	controlURL, err := l.Launch()
	if err != nil {
		m.fail(fmt.Errorf("启动 Chrome: %w", err))
		return
	}
	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		m.fail(fmt.Errorf("连接 Chrome: %w", err))
		return
	}
	m.mu.Lock()
	m.browser = browser
	m.mu.Unlock()
	defer func() {
		_ = browser.Close()
		m.mu.Lock()
		m.browser, m.page, m.cancel = nil, nil, nil
		if m.running {
			m.running = false
			if m.phase != "error" {
				m.phase, m.message = "closed", "Chrome 窗口已关闭"
			}
		}
		m.mu.Unlock()
	}()

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		m.fail(fmt.Errorf("创建页面: %w", err))
		return
	}
	m.mu.Lock()
	m.page, m.phase, m.message = page, "navigating", "正在进入 Adobe 官方注册入口"
	m.mu.Unlock()

	waitEvents := page.EachEvent(
		func(e *proto.NetworkResponseReceived) { m.onResponse(e) },
		func(e *proto.NetworkLoadingFailed) { m.onRequestFailed(e) },
		func(e *proto.RuntimeExceptionThrown) {
			if e.ExceptionDetails != nil && relevantConsole(e.ExceptionDetails.Text) {
				m.addEvent("error", "页面异常: "+truncate(e.ExceptionDetails.Text, 260))
			}
		},
		func(e *proto.RuntimeConsoleAPICalled) {
			if e.Type != proto.RuntimeConsoleAPICalledTypeError && e.Type != proto.RuntimeConsoleAPICalledTypeWarning {
				return
			}
			parts := make([]string, 0, len(e.Args))
			for _, arg := range e.Args {
				value, err := page.ObjectToJSON(arg)
				if err == nil {
					parts = append(parts, fmt.Sprint(value.Val()))
				}
			}
			text := strings.Join(parts, " ")
			if relevantConsole(text) {
				m.addEvent("console", truncate(text, 260))
			}
		},
	)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				m.addEvent("error", fmt.Sprintf("事件监听异常: %v", recovered))
			}
		}()
		waitEvents()
	}()

	if err := page.Timeout(90 * time.Second).Navigate(in.StartURL); err != nil {
		m.fail(fmt.Errorf("打开入口: %w", err))
		return
	}
	m.updateURL(page)
	m.addEvent("navigation", "Adobe 官方入口已打开")
	if err := m.openSignup(page); err != nil {
		m.fail(err)
		return
	}
	m.updateURL(page)
	m.checkEnvironment(page)

	page = page.Timeout(60 * time.Second)
	email, err := firstElement(page, []string{"#Signup-EmailField", `[data-id="Signup-EmailField"]`, `input[name="username"][type="email"]`})
	if err != nil {
		m.fail(fmt.Errorf("定位邮箱字段: %w", err))
		return
	}
	password, err := firstElement(page, []string{"#Signup-PasswordField", `[data-id="Signup-PasswordField"]`, `input[type="password"]`})
	if err != nil {
		m.fail(fmt.Errorf("定位密码字段: %w", err))
		return
	}
	if err := email.Input(in.Email); err != nil {
		m.fail(fmt.Errorf("填写邮箱: %w", err))
		return
	}
	if err := password.Input(in.Password); err != nil {
		m.fail(fmt.Errorf("填写密码: %w", err))
		return
	}
	m.addEvent("form", "邮箱与密码已填写")
	m.mu.Lock()
	m.phase, m.message = "waiting_user", "请在 Chrome 中继续；出现 Arkose 时手动完成"
	m.mu.Unlock()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.addEvent("system", "收到停止指令")
			return
		case <-ticker.C:
			if _, err := page.Info(); err != nil {
				return
			}
			m.updateURL(page)
		}
	}
}

func (m *Manager) openSignup(page *rod.Page) error {
	time.Sleep(2 * time.Second)
	info, _ := page.Info()
	if info != nil && strings.Contains(strings.ToLower(info.URL), "signup") {
		return nil
	}
	pg := page.Timeout(12 * time.Second)
	for _, pattern := range []string{"创建帐户|创建账户", "Create an account|Create account"} {
		if link, err := pg.ElementR("a", pattern); err == nil {
			if err := link.Click(proto.InputMouseButtonLeft, 1); err == nil {
				m.addEvent("navigation", "已点击创建帐户")
				time.Sleep(2 * time.Second)
				return nil
			}
		}
	}
	info, _ = page.Info()
	if info != nil && strings.Contains(info.URL, "auth.services.adobe.com/") {
		signupURL := strings.SplitN(info.URL, "#", 2)[0] + "#/signup"
		if err := page.Timeout(45 * time.Second).Navigate(signupURL); err == nil {
			m.addEvent("navigation", "已切换到注册路由")
			return nil
		}
	}
	return errors.New("页面上未找到创建帐户入口")
}

func (m *Manager) checkEnvironment(page *rod.Page) {
	obj, err := page.Eval(`() => {
		const controller = new window.AbortController();
		return {
			fetchNative: String(window.fetch).includes('[native code]'),
			signalConstructor: controller.signal?.constructor?.name || null,
			signalTag: Object.prototype.toString.call(controller.signal),
			signalInstanceOf: controller.signal instanceof window.AbortSignal,
			aborted: controller.signal.aborted
		};
	}`)
	if err != nil {
		m.addEvent("error", "AbortSignal 环境检查失败: "+err.Error())
		return
	}
	env, ok := obj.Value.Val().(map[string]any)
	if !ok {
		m.addEvent("error", "AbortSignal 环境检查返回格式异常")
		return
	}
	m.mu.Lock()
	m.environment = env
	m.mu.Unlock()
	if valid, _ := env["signalInstanceOf"].(bool); valid {
		m.addEvent("environment", "AbortSignal 类型检查正常")
	} else {
		m.addEvent("error", "AbortSignal 类型检查异常")
	}
}

func (m *Manager) onResponse(e *proto.NetworkResponseReceived) {
	if e == nil || e.Response == nil {
		return
	}
	u, status := e.Response.URL, int(e.Response.Status)
	switch {
	case strings.Contains(u, "/signin/v2/accounts") && !strings.Contains(u, "/signin/v2/users/accounts"):
		m.addEvent("registration", fmt.Sprintf("注册接口 HTTP %d", status))
		if status >= 200 && status < 300 {
			m.mu.Lock()
			m.phase, m.message = "success", "Adobe 创建账户接口返回成功"
			m.mu.Unlock()
		}
	case strings.Contains(u, "/signin/v2/users/accounts"):
		m.addEvent("network", fmt.Sprintf("邮箱账户检查 HTTP %d", status))
	case strings.Contains(u, "/signin/v1/passwords/validity"):
		m.addEvent("network", fmt.Sprintf("密码规则检查 HTTP %d", status))
	case strings.Contains(u, "/signin/v1/passwords/leak_verification"):
		m.addEvent("network", fmt.Sprintf("密码泄露检查 HTTP %d", status))
	case strings.Contains(u, "/signin/v3/domains/"):
		m.addEvent("network", fmt.Sprintf("邮箱域名策略检查 HTTP %d", status))
	case strings.Contains(u, "arks-client.adobe.com") || strings.Contains(u, "/fc/gt2/public_key/"):
		m.addEvent("arkose", fmt.Sprintf("Arkose HTTP %d: %s", status, stripQuery(u)))
	}
}

func (m *Manager) onRequestFailed(e *proto.NetworkLoadingFailed) {
	if e != nil && (strings.Contains(strings.ToLower(e.ErrorText), "arkose") || e.Type == proto.NetworkResourceTypeFetch) {
		m.addEvent("error", "请求失败: "+truncate(e.ErrorText, 220))
	}
}

func (m *Manager) updateURL(page *rod.Page) {
	if info, err := page.Info(); err == nil && info != nil {
		m.mu.Lock()
		m.currentURL = publicURL(info.URL)
		m.mu.Unlock()
	}
}

func (m *Manager) addEvent(kind, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, Event{Time: time.Now(), Kind: kind, Message: message})
	if len(m.events) > 200 {
		m.events = append([]Event(nil), m.events[len(m.events)-200:]...)
	}
}

func (m *Manager) fail(err error) {
	m.addEvent("error", err.Error())
	m.mu.Lock()
	m.running, m.phase, m.message = false, "error", err.Error()
	m.mu.Unlock()
}

func (m *Manager) finish(phase, message string) {
	m.mu.Lock()
	m.running, m.phase, m.message, m.cancel = false, phase, message, nil
	m.mu.Unlock()
}

func firstElement(page *rod.Page, selectors []string) (*rod.Element, error) {
	var last error = errors.New("element not found")
	for _, selector := range selectors {
		el, err := page.Element(selector)
		if err == nil {
			visible, visibleErr := el.Visible()
			if visibleErr == nil && visible {
				return el, nil
			}
		}
		if err != nil {
			last = err
		}
	}
	return nil, last
}

func findChrome() (string, bool) {
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

func relevantConsole(s string) bool {
	s = strings.ToLower(s)
	for _, keyword := range []string{"abortsignal", "captcha", "arkose", "api_request_error", "failed to execute 'fetch'"} {
		if strings.Contains(s, keyword) {
			return true
		}
	}
	return false
}

func stripQuery(s string) string {
	if i := strings.IndexByte(s, '?'); i >= 0 {
		return s[:i]
	}
	return s
}

func publicURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return stripQuery(raw)
	}
	u.RawQuery = ""
	return u.String()
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
