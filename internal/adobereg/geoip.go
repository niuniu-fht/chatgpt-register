package adobereg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"chatgpt-register/internal/proxyutil"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

type adobeGeoInfo struct {
	IP          string  `json:"ip"`
	Success     bool    `json:"success"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	City        string  `json:"city"`
	Lat         float64 `json:"latitude"`
	Lon         float64 `json:"longitude"`
	Timezone    struct {
		ID string `json:"id"`
	} `json:"timezone"`
}

func lookupAdobeGeoIP(ctx context.Context, in Input) (*adobeGeoInfo, error) {
	client, err := proxyutil.HTTPClient(in.Proxy, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("初始化代理出口检查: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://ipwho.is/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-SG,en;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/146.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("代理出口检查失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("读取代理出口信息: %w", err)
	}
	var geo adobeGeoInfo
	if err := json.Unmarshal(body, &geo); err != nil {
		return nil, fmt.Errorf("解析代理出口信息: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !geo.Success || net.ParseIP(strings.TrimSpace(geo.IP)) == nil {
		return nil, fmt.Errorf("代理出口检查返回异常: HTTP %d", resp.StatusCode)
	}
	return &geo, nil
}

func adobeLocale(countryCode string) (locale, acceptLanguage string) {
	switch strings.ToUpper(strings.TrimSpace(countryCode)) {
	case "SG":
		return "en-SG", "en-SG,en;q=0.9"
	case "US":
		return "en-US", "en-US,en;q=0.9"
	case "GB":
		return "en-GB", "en-GB,en;q=0.9"
	case "JP":
		return "ja-JP", "ja-JP,ja;q=0.9,en;q=0.7"
	default:
		return "en-US", "en-US,en;q=0.9"
	}
}

func applyAdobeGeo(page *rod.Page, geo *adobeGeoInfo) {
	if geo == nil {
		return
	}
	if geo.Timezone.ID != "" {
		_ = (proto.EmulationSetTimezoneOverride{TimezoneID: geo.Timezone.ID}).Call(page)
	}
	lat, lon, accuracy := geo.Lat, geo.Lon, 50.0
	_ = (proto.EmulationSetGeolocationOverride{
		Latitude: &lat, Longitude: &lon, Accuracy: &accuracy,
	}).Call(page)
	locale, _ := adobeLocale(geo.CountryCode)
	_ = (proto.EmulationSetLocaleOverride{Locale: locale}).Call(page)
}

func verifyAdobeBrowserEgress(page *rod.Page, expectedIP string) (string, error) {
	checkPage := page.Timeout(30 * time.Second)
	if err := checkPage.Navigate("https://api.ipify.org?format=json"); err != nil {
		return "", fmt.Errorf("浏览器代理出口检查失败: %w", err)
	}
	if err := checkPage.WaitLoad(); err != nil {
		return "", fmt.Errorf("等待浏览器代理出口响应: %w", err)
	}
	element, err := checkPage.Element("body")
	if err != nil {
		return "", fmt.Errorf("读取浏览器代理出口响应: %w", err)
	}
	text, err := element.Text()
	if err != nil {
		return "", fmt.Errorf("读取浏览器代理出口响应: %w", err)
	}
	var result struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &result); err != nil {
		return "", fmt.Errorf("解析浏览器代理出口响应: %w", err)
	}
	result.IP = strings.TrimSpace(result.IP)
	if net.ParseIP(result.IP) == nil {
		return "", fmt.Errorf("浏览器代理出口 IP 格式异常")
	}
	if expectedIP != "" && result.IP != strings.TrimSpace(expectedIP) {
		return result.IP, fmt.Errorf("浏览器出口与代理预检不一致: 浏览器=%s, 代理=%s", result.IP, expectedIP)
	}
	return result.IP, nil
}
