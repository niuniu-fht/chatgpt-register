// Package browserboot 负责在程序启动时确保 rod 所需的 Chromium 浏览器已就绪，
// 未就绪则自动下载，并对外暴露下载进度供仪表盘展示。
package browserboot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/go-rod/rod/lib/launcher"
)

var cloakDownloadMu sync.Mutex

// DiscoverCloakBrowser returns the newest managed CloakBrowser binary, or an
// installed system entry when the managed cache is absent.
func DiscoverCloakBrowser() string {
	home, _ := os.UserHomeDir()
	patterns := []string{
		filepath.Join(home, ".cloakbrowser", "chromium-*", "chrome"),
		filepath.Join(home, ".cloakbrowser", "chromium-*", "chrome.exe"),
	}
	var matches []string
	for _, pattern := range patterns {
		found, _ := filepath.Glob(pattern)
		for _, path := range found {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				matches = append(matches, path)
			}
		}
	}
	if len(matches) > 0 {
		return newestManagedCloak(matches)
	}
	if runtime.GOOS == "windows" {
		for _, path := range []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "CloakBrowser", "chrome.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES"), "CloakBrowser", "chrome.exe"),
		} {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path
			}
		}
	}
	for _, path := range []string{"/opt/cloakbrowser/chrome", "/opt/cloakbrowser/cloakbrowser", "/usr/local/bin/cloakbrowser"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// newestManagedCloak uses the numeric directory name without pinning a
// particular release. The directory format is chromium-X.Y.Z...[-pro].
func newestManagedCloak(paths []string) string {
	best := ""
	var bestVersion []int
	for _, path := range paths {
		version, ok := managedCloakVersion(path)
		if !ok {
			continue
		}
		if best == "" || compareVersion(version, bestVersion) > 0 {
			best, bestVersion = path, version
		}
	}
	return best
}

func managedCloakVersion(path string) ([]int, bool) {
	directory := filepath.Base(filepath.Dir(filepath.Clean(path)))
	raw := strings.TrimPrefix(strings.ToLower(directory), "chromium-")
	if raw == directory {
		return nil, false
	}
	raw = strings.TrimSuffix(raw, "-pro")
	parts := strings.Split(raw, ".")
	version := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		version = append(version, value)
	}
	return version, len(version) > 0
}

func compareVersion(a, b []int) int {
	length := len(a)
	if len(b) > length {
		length = len(b)
	}
	for i := 0; i < length; i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

// EnsureCloakBrowser discovers or downloads CloakBrowser using the official
// CLI. The installed release is selected dynamically; no Chromium version is
// embedded in the application.
func EnsureCloakBrowser(ctx context.Context) (string, error) {
	cloakDownloadMu.Lock()
	defer cloakDownloadMu.Unlock()
	if path := DiscoverCloakBrowser(); path != "" {
		return path, nil
	}
	commands := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		commands = []string{"python", "py"}
	}
	var lastErr error
	for _, command := range commands {
		if _, err := exec.LookPath(command); err != nil {
			lastErr = err
			continue
		}
		cmd := exec.CommandContext(ctx, command, "-m", "cloakbrowser", "update")
		output, err := cmd.CombinedOutput()
		if err == nil {
			if path := DiscoverCloakBrowser(); path != "" {
				return path, nil
			}
			lastErr = fmt.Errorf("下载命令完成但未找到浏览器: %s", strings.TrimSpace(string(output)))
			continue
		}
		lastErr = fmt.Errorf("%s: %w (%s)", command, err, strings.TrimSpace(string(output)))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("找不到 Python 运行时")
	}
	return "", fmt.Errorf("CloakBrowser 自动下载失败: %w", lastErr)
}

// Status 浏览器就绪 / 下载状态快照。
type Status struct {
	Ready       bool   `json:"ready"`       // 浏览器是否已就绪（可以生产）
	Downloading bool   `json:"downloading"` // 是否正在下载
	Percent     int    `json:"percent"`     // 当前阶段进度 0-100
	Phase       string `json:"phase"`       // checking / downloading / unzip / ready / error
	Message     string `json:"message"`     // 面向用户的提示
	Error       string `json:"error"`       // 失败原因
}

// Manager 管理浏览器下载状态，实现 rod launcher 的 utils.Logger 接口以捕获下载进度。
type Manager struct {
	mu   sync.RWMutex
	st   Status
	once sync.Once
}

func New() *Manager {
	return &Manager{st: Status{Phase: "checking", Message: "正在检查浏览器..."}}
}

// Snapshot 返回状态副本。
func (m *Manager) Snapshot() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.st
}

// Ready 浏览器是否已就绪。
func (m *Manager) Ready() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.st.Ready
}

func (m *Manager) set(f func(*Status)) {
	m.mu.Lock()
	f(&m.st)
	m.mu.Unlock()
}

// Println 实现 launcher.Browser.Logger（utils.Logger）接口，解析 fetchup 的进度事件。
// 事件形如：("Download:", url) / ("Progress:", "50%") / ("Unzip:", dir) / ("Downloaded:", to)。
func (m *Manager) Println(vs ...interface{}) {
	if len(vs) == 0 {
		return
	}
	tag := strings.TrimSpace(fmt.Sprint(vs[0]))
	switch {
	case strings.HasPrefix(tag, "Progress:"):
		if len(vs) > 1 {
			ps := strings.TrimSuffix(strings.TrimSpace(fmt.Sprint(vs[1])), "%")
			if n, err := strconv.Atoi(ps); err == nil {
				m.set(func(s *Status) {
					s.Downloading = true
					if s.Phase != "unzip" {
						s.Phase = "downloading"
					}
					s.Percent = n
					if s.Phase == "unzip" {
						s.Message = fmt.Sprintf("正在解压浏览器 %d%%", n)
					} else {
						s.Message = fmt.Sprintf("正在下载浏览器 %d%%", n)
					}
				})
			}
		}
	case strings.HasPrefix(tag, "Download:"):
		m.set(func(s *Status) {
			s.Downloading = true
			s.Phase = "downloading"
			s.Percent = 0
			s.Message = "开始下载浏览器..."
		})
	case strings.HasPrefix(tag, "Unzip:"):
		m.set(func(s *Status) {
			s.Downloading = true
			s.Phase = "unzip"
			s.Percent = 0
			s.Message = "正在解压浏览器..."
		})
	case strings.HasPrefix(tag, "Downloaded:"):
		m.set(func(s *Status) {
			s.Percent = 100
			s.Message = "下载完成，正在校验..."
		})
	}
}

// EnsureAsync 后台确保浏览器就绪（幂等，仅执行一次）。
func (m *Manager) EnsureAsync() {
	m.once.Do(func() { go m.ensure() })
}

func (m *Manager) ensure() {
	b := launcher.NewBrowser()
	b.Logger = m

	// 已存在且可用 → 直接就绪，不下载。
	if err := b.Validate(); err == nil {
		m.set(func(s *Status) {
			*s = Status{Ready: true, Percent: 100, Phase: "ready", Message: "浏览器已就绪"}
		})
		return
	}

	m.set(func(s *Status) {
		s.Ready = false
		s.Downloading = true
		s.Phase = "downloading"
		s.Message = "缺少浏览器，正在下载..."
	})

	if _, err := b.Get(); err != nil {
		m.set(func(s *Status) {
			s.Ready = false
			s.Downloading = false
			s.Phase = "error"
			s.Error = err.Error()
			s.Message = "浏览器下载失败，请检查网络后重启程序"
		})
		return
	}

	m.set(func(s *Status) {
		*s = Status{Ready: true, Percent: 100, Phase: "ready", Message: "浏览器已就绪"}
	})
}
