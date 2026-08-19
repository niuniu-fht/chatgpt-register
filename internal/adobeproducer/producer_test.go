package adobeproducer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-register/internal/adobereg"
	"chatgpt-register/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestProducerPersistsSuccessAndLog(t *testing.T) {
	db := testDB(t)
	row := models.AdobeRegistration{Email: "success@example.test", FirstName: "Test", LastName: "User", BirthYear: 1994, BirthMonth: 6, Status: "pending"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	p := New(db)
	p.register = func(_ context.Context, in adobereg.Input) (*adobereg.Result, error) {
		in.Log("fixture reached console")
		return &adobereg.Result{Email: in.Email, Password: in.Password, Status: "registered"}, nil
	}
	if err := p.Start(StartOptions{Count: 1, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	if err := db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "registered" || !strings.Contains(row.Log, "fixture reached console") || row.Password == "" {
		t.Fatalf("unexpected row: status=%s log=%q password=%t", row.Status, row.Log, row.Password != "")
	}
}

func TestProducerTreatsExistingAdobeAccountAsRegistered(t *testing.T) {
	db := testDB(t)
	row := models.AdobeRegistration{Email: "existing@example.test", BirthYear: 1994, BirthMonth: 6, Status: "pending"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	p := New(db)
	p.register = func(_ context.Context, _ adobereg.Input) (*adobereg.Result, error) {
		return nil, adobereg.ErrAccountAlreadyExists
	}
	if err := p.Start(StartOptions{Count: 1, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	if err := db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "registered" || row.Note != "Adobe 账户已存在" || !strings.Contains(row.Log, "按注册成功处理") {
		t.Fatalf("unexpected row: status=%q note=%q log=%q", row.Status, row.Note, row.Log)
	}
	progress := p.Snapshot()
	if progress.Registered != 1 || progress.Failed != 0 {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}

func TestProducerPersistsCaptchaFailureAndScreenshot(t *testing.T) {
	db := testDB(t)
	row := models.AdobeRegistration{Email: "captcha@example.test", BirthYear: 1994, BirthMonth: 6, Status: "pending"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	p := New(db)
	p.register = func(_ context.Context, in adobereg.Input) (*adobereg.Result, error) {
		in.SaveShot([]byte("png fixture"))
		return nil, errors.New("captcha_required")
	}
	if err := p.Start(StartOptions{Count: 1, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	if err := db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "register_failed" || row.Note != "需要完成 CAPTCHA 验证" || len(row.Shot) == 0 {
		t.Fatalf("unexpected failure row: status=%s note=%q shot=%d", row.Status, row.Note, len(row.Shot))
	}
}

func TestProducerInjectsYesCaptchaFromSettings(t *testing.T) {
	db := testDB(t)
	for key, value := range map[string]string{
		"yescaptcha_enabled": "1", "yescaptcha_api_key": "fixture-key",
		"yescaptcha_api_url": "https://api.yescaptcha.com",
	} {
		if err := db.Create(&models.Setting{Key: key, Value: value}).Error; err != nil {
			t.Fatal(err)
		}
	}
	row := models.AdobeRegistration{Email: "captcha-config@example.test", BirthYear: 1994, BirthMonth: 6, Status: "pending"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	p := New(db)
	p.register = func(_ context.Context, in adobereg.Input) (*adobereg.Result, error) {
		if in.Captcha == nil {
			t.Fatal("YesCaptcha classifier was not injected")
		}
		return &adobereg.Result{Status: "registered"}, nil
	}
	if err := p.Start(StartOptions{Count: 1, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
}

func TestConfigureDefaultsToCloakBrowserFromEnvironment(t *testing.T) {
	db := testDB(t)
	browserPath := filepath.Join(t.TempDir(), "cloak-browser")
	if err := os.WriteFile(browserPath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADOBE_BROWSER_MODE", "")
	t.Setenv("CLOAK_BROWSER_PATH", browserPath)
	p := New(db)
	opts := StartOptions{}
	if err := p.configureStartOptions(&opts); err != nil {
		t.Fatal(err)
	}
	if opts.BrowserMode != "cloak" || opts.BrowserPath != browserPath {
		t.Fatalf("mode=%q path=%q", opts.BrowserMode, opts.BrowserPath)
	}
}

func TestNewerManagedCloakVersion(t *testing.T) {
	current := filepath.Join("C:", "Users", "Admin", ".cloakbrowser", "chromium-146.0.7680.177.5", "chrome.exe")
	latest := filepath.Join("C:", "Users", "Admin", ".cloakbrowser", "chromium-151.0.7922.108.2-pro", "chrome.exe")
	if !newerManagedCloak(latest, current) {
		t.Fatal("latest managed CloakBrowser cache should replace the older version")
	}
	if newerManagedCloak(current, latest) {
		t.Fatal("older managed CloakBrowser cache must not replace the latest version")
	}
	if newerManagedCloak(`D:\custom\chrome.exe`, current) {
		t.Fatal("custom browser paths must not be auto-replaced")
	}
}

func TestProducerRotatesConfiguredProxyPool(t *testing.T) {
	db := testDB(t)
	settings := []models.Setting{
		{Key: "proxy_enabled", Value: "1"},
		{Key: "proxy_list", Value: "proxy-a.example.test:8001\nhttp://user:secret@proxy-b.example.test:8002"},
	}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		row := models.AdobeRegistration{Email: fmt.Sprintf("proxy-%d@example.test", i), Status: "pending"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := New(db)
	p.stagger = 0
	var mu sync.Mutex
	var proxies []string
	p.register = func(_ context.Context, in adobereg.Input) (*adobereg.Result, error) {
		mu.Lock()
		proxies = append(proxies, in.Proxy)
		mu.Unlock()
		return &adobereg.Result{Status: "registered"}, nil
	}
	if err := p.Start(StartOptions{Count: 2, Concurrency: 2, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	sort.Strings(proxies)
	want := []string{"http://user:secret@proxy-b.example.test:8002", "proxy-a.example.test:8001"}
	sort.Strings(want)
	if len(proxies) != len(want) || proxies[0] != want[0] || proxies[1] != want[1] {
		t.Fatalf("proxies=%q", proxies)
	}
}

func TestProducerRetriesAccountWithNextRejectedProxy(t *testing.T) {
	db := testDB(t)
	settings := []models.Setting{
		{Key: "proxy_enabled", Value: "1"},
		{Key: "proxy_list", Value: "proxy-a.example.test:8001\nproxy-b.example.test:8002"},
	}
	if err := db.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	row := models.AdobeRegistration{Email: "retry-proxy@example.test", BirthYear: 1994, BirthMonth: 6, Country: "SG", Status: "pending"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	p := New(db)
	p.stagger = 0
	var proxies []string
	p.register = func(_ context.Context, in adobereg.Input) (*adobereg.Result, error) {
		proxies = append(proxies, in.Proxy)
		if len(proxies) == 1 {
			return nil, fmt.Errorf("%w: test mismatch", adobereg.ErrProxyRejected)
		}
		return &adobereg.Result{Status: "registered"}, nil
	}
	if err := p.Start(StartOptions{Count: 1, Concurrency: 1, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	if len(proxies) != 2 || proxies[0] == proxies[1] {
		t.Fatalf("expected two different proxy attempts, got %q", proxies)
	}
	if err := db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "registered" {
		t.Fatalf("status=%q note=%q", row.Status, row.Note)
	}
}

func TestStopReturnsActiveAccountToPending(t *testing.T) {
	db := testDB(t)
	row := models.AdobeRegistration{Email: "stop@example.test", BirthYear: 1994, BirthMonth: 6, Status: "pending"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	p := New(db)
	p.register = func(ctx context.Context, _ adobereg.Input) (*adobereg.Result, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := p.Start(StartOptions{Count: 1, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("registration did not start")
	}
	p.Stop()
	if message := p.Snapshot().Message; message != "正在停止" {
		t.Fatalf("message=%q", message)
	}
	waitDone(t, p)
	if err := db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" || row.Note != "" {
		t.Fatalf("status=%q note=%q", row.Status, row.Note)
	}
	if p.Snapshot().Failed != 0 {
		t.Fatalf("stopped task counted as failed: %+v", p.Snapshot())
	}
}

func TestProducerRunsWithConfiguredConcurrency(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 4; i++ {
		row := models.AdobeRegistration{Email: fmt.Sprintf("concurrent-%d@example.test", i), BirthYear: 1994, BirthMonth: 6, Status: "pending"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := New(db)
	p.stagger = 0
	var mu sync.Mutex
	active, peak := 0, 0
	p.register = func(_ context.Context, _ adobereg.Input) (*adobereg.Result, error) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		time.Sleep(80 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return &adobereg.Result{Status: "registered"}, nil
	}
	if err := p.Start(StartOptions{Count: 4, Concurrency: 3, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	if peak != 3 {
		t.Fatalf("peak concurrency=%d, want 3", peak)
	}
	status := p.Snapshot()
	if status.Registered != 4 || status.Failed != 0 {
		t.Fatalf("unexpected progress: %+v", status)
	}
}

func TestStopOneAccountKeepsOtherWorkerRunning(t *testing.T) {
	db := testDB(t)
	rows := []models.AdobeRegistration{
		{Email: "stop-one@example.test", BirthYear: 1994, BirthMonth: 6, Status: "pending"},
		{Email: "keep-running@example.test", BirthYear: 1994, BirthMonth: 6, Status: "pending"},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan string, 2)
	releaseSecond := make(chan struct{})
	p := New(db)
	p.stagger = 0
	p.register = func(ctx context.Context, in adobereg.Input) (*adobereg.Result, error) {
		started <- in.Email
		if in.Email == rows[0].Email {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-releaseSecond:
			return &adobereg.Result{Status: "registered"}, nil
		}
	}
	if err := p.Start(StartOptions{Count: 2, Concurrency: 2, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("both workers did not start")
		}
	}
	if !p.StopAccount(rows[0].ID) {
		t.Fatal("active account was not stopped")
	}
	close(releaseSecond)
	waitDone(t, p)
	statuses := make([]string, len(rows))
	for i := range rows {
		var refreshed models.AdobeRegistration
		if err := db.First(&refreshed, rows[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		statuses[i] = refreshed.Status
	}
	if statuses[0] != "pending" || statuses[1] != "registered" {
		t.Fatalf("statuses=%q,%q", statuses[0], statuses[1])
	}
	status := p.Snapshot()
	if status.Registered != 1 || status.Failed != 0 {
		t.Fatalf("unexpected progress: %+v", status)
	}
}

func TestWorkerPanicOnlyFailsCurrentAccount(t *testing.T) {
	db := testDB(t)
	row := models.AdobeRegistration{Email: "panic@example.test", BirthYear: 1994, BirthMonth: 6, Status: "pending"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	p := New(db)
	p.register = func(context.Context, adobereg.Input) (*adobereg.Result, error) {
		panic("fixture panic")
	}
	if err := p.Start(StartOptions{Count: 1, Concurrency: 1, BrowserMode: "system"}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	var refreshed models.AdobeRegistration
	if err := db.First(&refreshed, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != "register_failed" || !strings.Contains(refreshed.Note, "fixture panic") {
		t.Fatalf("status=%q note=%q", refreshed.Status, refreshed.Note)
	}
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.AdobeRegistration{}, &models.Setting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func waitDone(t *testing.T, p *Producer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !p.Snapshot().Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("producer did not finish")
}
