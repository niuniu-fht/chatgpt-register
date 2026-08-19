package adobeproducer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"chatgpt-register/internal/adobereg"
	"chatgpt-register/internal/models"
	"chatgpt-register/internal/proxyutil"
	"chatgpt-register/internal/yescaptcha"

	"gorm.io/gorm"
)

type RegisterFunc func(context.Context, adobereg.Input) (*adobereg.Result, error)

type StartOptions struct {
	Count       int      `json:"count"`
	Concurrency int      `json:"concurrency"`
	Headless    bool     `json:"headless"`
	BrowserMode string   `json:"browser_mode"`
	BrowserPath string   `json:"browser_path"`
	Proxies     []string `json:"-"`
}

type Progress struct {
	Running     bool      `json:"running"`
	Target      int       `json:"target"`
	Pending     int       `json:"pending"`
	RunningNum  int       `json:"running_num"`
	Concurrency int       `json:"concurrency"`
	Registered  int       `json:"registered"`
	Failed      int       `json:"failed"`
	Message     string    `json:"message"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Producer struct {
	db       *gorm.DB
	register RegisterFunc
	stagger  time.Duration

	mu     sync.Mutex
	dbMu   sync.Mutex
	prog   Progress
	cancel context.CancelFunc
	jobs   map[uint]context.CancelFunc
	pxMu   sync.Mutex
	pxIdx  int
}

func New(db *gorm.DB) *Producer {
	return &Producer{db: db, register: adobereg.Register, stagger: 2 * time.Second, jobs: make(map[uint]context.CancelFunc)}
}

func (p *Producer) Start(opts StartOptions) error {
	if opts.Count < 1 {
		return fmt.Errorf("生产数量必须大于 0")
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Concurrency > 5 {
		opts.Concurrency = 5
	}
	if err := p.configureStartOptions(&opts); err != nil {
		return err
	}
	var available int64
	if err := p.db.Model(&models.AdobeRegistration{}).Where("status = ?", "pending").Count(&available).Error; err != nil {
		return err
	}
	if available == 0 {
		return fmt.Errorf("没有待生产的 Adobe 账号")
	}
	if int64(opts.Count) > available {
		opts.Count = int(available)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prog.Running {
		return fmt.Errorf("Adobe 生产任务正在运行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.prog = Progress{
		Running: true, Target: opts.Count, Pending: opts.Count,
		Concurrency: opts.Concurrency, Message: "准备生产", UpdatedAt: time.Now(),
	}
	go p.run(ctx, opts)
	return nil
}

func (p *Producer) StopAccount(id uint) bool {
	p.mu.Lock()
	cancel, ok := p.jobs[id]
	if ok {
		p.prog.Message = fmt.Sprintf("正在停止账号 %d", id)
		p.prog.UpdatedAt = time.Now()
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	p.dbMu.Lock()
	_ = p.db.Model(&models.AdobeRegistration{}).Where("id = ? AND status = ?", id, "registering").
		Updates(map[string]any{"status": "pending", "note": ""}).Error
	p.dbMu.Unlock()
	return true
}

func (p *Producer) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	if p.prog.Running {
		p.prog.Message = "正在停止"
		p.prog.UpdatedAt = time.Now()
	}
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		p.dbMu.Lock()
		_ = p.db.Model(&models.AdobeRegistration{}).
			Where("status = ?", "registering").
			Updates(map[string]any{"status": "pending", "note": ""}).Error
		p.dbMu.Unlock()
	}
}

func (p *Producer) Snapshot() Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prog
}

func (p *Producer) run(ctx context.Context, opts StartOptions) {
	defer func() {
		p.mu.Lock()
		p.prog.Running = false
		p.prog.RunningNum = 0
		p.prog.Pending = max(0, p.prog.Target-p.prog.Registered-p.prog.Failed)
		if ctx.Err() != nil {
			p.prog.Message = "已停止"
		} else {
			p.prog.Message = "生产完成"
		}
		p.prog.UpdatedAt = time.Now()
		p.cancel = nil
		p.mu.Unlock()
	}()

	var accounts []models.AdobeRegistration
	if err := p.db.Where("status = ?", "pending").Order("id asc").Limit(opts.Count).Find(&accounts).Error; err != nil {
		p.setMessage("读取待生产账号失败: " + err.Error())
		return
	}
	workerCount := min(opts.Concurrency, len(accounts))
	jobs := make(chan models.AdobeRegistration)
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerIndex int) {
			defer wg.Done()
			if workerIndex > 0 && p.stagger > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(workerIndex) * p.stagger):
				}
			}
			for account := range jobs {
				if ctx.Err() != nil {
					return
				}
				jobCtx, cancel := context.WithCancel(ctx)
				p.beginAccount(account.ID, account.Email, cancel)
				err := p.produceOneGuarded(jobCtx, &account, opts)
				canceled := jobCtx.Err() != nil
				cancel()
				p.finishAccount(account.ID, err == nil, canceled)
				if ctx.Err() != nil {
					return
				}
			}
		}(i)
	}
sendLoop:
	for _, account := range accounts {
		select {
		case <-ctx.Done():
			break sendLoop
		case jobs <- account:
		}
	}
	close(jobs)
	wg.Wait()
}

func (p *Producer) produceOneGuarded(ctx context.Context, account *models.AdobeRegistration, opts StartOptions) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("注册流程异常: %v", recovered)
			status, note := "register_failed", err.Error()
			if ctx.Err() != nil {
				status, note = "pending", ""
			}
			_ = p.updateAccount(account.ID, map[string]any{"status": status, "note": note})
		}
	}()
	return p.produceOne(ctx, account, opts)
}

func (p *Producer) produceOne(ctx context.Context, account *models.AdobeRegistration, opts StartOptions) error {
	password := account.Password
	if password == "" {
		password = adobereg.GenPassword(18)
	}
	firstName, lastName := account.FirstName, account.LastName
	if firstName == "" || lastName == "" {
		inferredFirst, inferredLast := adobereg.RandomName()
		if firstName == "" {
			firstName = inferredFirst
		}
		if lastName == "" {
			lastName = inferredLast
		}
	}
	updates := map[string]any{
		"password": password, "first_name": firstName, "last_name": lastName,
		"status": "registering", "note": "", "shot": nil,
	}
	if err := p.updateAccount(account.ID, updates); err != nil {
		return fmt.Errorf("更新账号运行状态: %w", err)
	}

	var logMu sync.Mutex
	var logBuf strings.Builder
	if strings.TrimSpace(account.Log) != "" {
		logBuf.WriteString(account.Log)
		if !strings.HasSuffix(account.Log, "\n") {
			logBuf.WriteByte('\n')
		}
		logBuf.WriteString(time.Now().Format("2006-01-02 15:04:05") + " --- 新一轮生产 ---\n")
	}
	appendLog := func(line string) {
		logMu.Lock()
		logBuf.WriteString(time.Now().Format("2006-01-02 15:04:05") + " " + line + "\n")
		snapshot := logBuf.String()
		logMu.Unlock()
		_ = p.updateAccount(account.ID, map[string]any{"log": snapshot})
	}
	appendLog("开始 Adobe 注册")
	if opts.BrowserMode == "cloak" {
		appendLog("浏览器模式: CloakBrowser")
	} else {
		appendLog("浏览器模式: 系统 Chrome")
	}
	proxyAttempts := len(opts.Proxies)
	if proxyAttempts == 0 {
		proxyAttempts = 1
	}
	var result *adobereg.Result
	var err error
	for attempt := 1; attempt <= proxyAttempts; attempt++ {
		proxy := p.nextProxy(opts)
		if proxy != "" {
			appendLog(fmt.Sprintf("代理尝试 %d/%d: %s", attempt, proxyAttempts, proxyutil.Redacted(proxy)))
		} else {
			appendLog("代理模式: 直连")
		}
		var captcha adobereg.CaptchaClassifier
		if p.setting("yescaptcha_enabled") == "1" {
			key := strings.TrimSpace(p.setting("yescaptcha_api_key"))
			if key != "" {
				client, proxyErr := yescaptcha.NewWithProxy(key, p.setting("yescaptcha_api_url"), proxy)
				if proxyErr != nil {
					err = fmt.Errorf("初始化验证码代理: %w", proxyErr)
				} else {
					captcha = client
					appendLog("YesCaptcha 已启用")
				}
			} else {
				appendLog("YesCaptcha 已启用但 API Key 为空")
			}
		}
		if strings.TrimSpace(os.Getenv("YESCAPTCHA_EXTENSION_PATH")) != "" {
			// The browser extension owns CAPTCHA interaction when configured;
			// keeping the API classifier off prevents duplicate clicks.
			captcha = nil
			appendLog("YesCaptcha 浏览器插件已启用，使用有头浏览器自动识别")
		}
		if err == nil {
			for seatAttempt := 1; ; seatAttempt++ {
				result, err = p.register(ctx, adobereg.Input{
					Email: account.Email, Password: password,
					FirstName: firstName, LastName: lastName,
					BirthYear: account.BirthYear, BirthMonth: account.BirthMonth,
					Country: account.Country, Headless: opts.Headless, Captcha: captcha,
					Proxy:      proxy,
					BrowserBin: opts.BrowserPath, CloakBrowser: opts.BrowserMode == "cloak",
					Log: func(format string, args ...any) {
						appendLog(fmt.Sprintf(format, args...))
					},
					SaveShot: func(png []byte) {
						_ = p.updateAccount(account.ID, map[string]any{"shot": png})
					},
				})
				if !errors.Is(err, adobereg.ErrCloakSeatBusy) {
					break
				}
				appendLog(fmt.Sprintf("CloakBrowser 席位仍被占用，第 %d 次等待；15 秒后重试当前账号", seatAttempt))
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-time.After(15 * time.Second):
				}
				if ctx.Err() != nil {
					break
				}
			}
		}
		if err == nil || errors.Is(err, adobereg.ErrAccountAlreadyExists) ||
			errors.Is(err, context.Canceled) || ctx.Err() != nil {
			break
		}
		if errors.Is(err, adobereg.ErrProxyRejected) && attempt < proxyAttempts {
			appendLog(fmt.Sprintf("当前代理出口校验失败，切换下一条代理: %v", err))
			err = nil
			continue
		}
		break
	}
	if err != nil {
		if errors.Is(err, adobereg.ErrAccountAlreadyExists) {
			appendLog("Adobe 提示账户已存在，按注册成功处理")
			if updateErr := p.updateAccount(account.ID, map[string]any{
				"status": "registered", "note": "Adobe 账户已存在", "log": logBuf.String(),
			}); updateErr != nil {
				return fmt.Errorf("保存注册成功状态: %w", updateErr)
			}
			return nil
		}
		note := friendlyError(err)
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			appendLog("任务已停止，账号已恢复为待生产")
			_ = p.updateAccount(account.ID, map[string]any{
				"status": "pending", "note": "", "log": logBuf.String(),
			})
			return err
		}
		appendLog("失败: " + note)
		_ = p.updateAccount(account.ID, map[string]any{
			"status": "register_failed", "note": note, "log": logBuf.String(),
		})
		return err
	}
	if result == nil || result.Status != "registered" {
		err = errors.New("未进入 Adobe 账户控制台")
		appendLog("失败: " + err.Error())
		_ = p.updateAccount(account.ID, map[string]any{
			"status": "register_failed", "note": err.Error(), "log": logBuf.String(),
		})
		return err
	}
	appendLog("注册成功，已进入 Adobe 账户控制台")
	if err := p.updateAccount(account.ID, map[string]any{
		"status": "registered", "note": "", "log": logBuf.String(),
	}); err != nil {
		return fmt.Errorf("保存注册成功状态: %w", err)
	}
	return nil
}

func (p *Producer) nextProxy(opts StartOptions) string {
	if len(opts.Proxies) == 0 {
		return ""
	}
	p.pxMu.Lock()
	proxy := opts.Proxies[p.pxIdx%len(opts.Proxies)]
	p.pxIdx++
	p.pxMu.Unlock()
	return proxy
}

func (p *Producer) updateAccount(id uint, updates map[string]any) error {
	p.dbMu.Lock()
	defer p.dbMu.Unlock()
	return p.db.Model(&models.AdobeRegistration{}).Where("id = ?", id).Updates(updates).Error
}

func (p *Producer) setting(key string) string {
	var item models.Setting
	if err := p.db.Where("key = ?", key).First(&item).Error; err != nil {
		return ""
	}
	return item.Value
}

func friendlyError(err error) string {
	if strings.Contains(err.Error(), "captcha_required") {
		return "需要完成 CAPTCHA 验证"
	}
	if errors.Is(err, context.Canceled) {
		return "任务已停止"
	}
	return err.Error()
}

func (p *Producer) beginAccount(id uint, email string, cancel context.CancelFunc) {
	p.mu.Lock()
	p.jobs[id] = cancel
	p.prog.RunningNum++
	p.prog.Pending = max(0, p.prog.Target-p.prog.Registered-p.prog.Failed-p.prog.RunningNum)
	p.prog.Message = fmt.Sprintf("并发注册中：%d 个账号（最新 %s）", p.prog.RunningNum, email)
	p.prog.UpdatedAt = time.Now()
	p.mu.Unlock()
}

func (p *Producer) finishAccount(id uint, success, canceled bool) {
	p.mu.Lock()
	delete(p.jobs, id)
	p.prog.RunningNum = max(0, p.prog.RunningNum-1)
	if success && !canceled {
		p.prog.Registered++
	} else if !canceled {
		p.prog.Failed++
	}
	p.prog.Pending = max(0, p.prog.Target-p.prog.Registered-p.prog.Failed-p.prog.RunningNum)
	if p.prog.RunningNum > 0 {
		p.prog.Message = fmt.Sprintf("并发注册中：%d 个账号", p.prog.RunningNum)
	}
	p.prog.UpdatedAt = time.Now()
	p.mu.Unlock()
}

func (p *Producer) setMessage(message string) {
	p.mu.Lock()
	p.prog.Message = message
	p.prog.UpdatedAt = time.Now()
	p.mu.Unlock()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
