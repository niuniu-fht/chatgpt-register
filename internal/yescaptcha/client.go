package yescaptcha

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"chatgpt-register/internal/proxyutil"
)

const DefaultBaseURL = "https://api.yescaptcha.com"

type Client struct {
	key        string
	baseURL    string
	httpClient *http.Client
	pollEvery  time.Duration
	maxWait    time.Duration
}

type apiResponse struct {
	ErrorID          int    `json:"errorId"`
	ErrorCode        string `json:"errorCode"`
	ErrorDescription string `json:"errorDescription"`
	TaskID           string `json:"taskId"`
	Status           string `json:"status"`
	Solution         struct {
		Objects []int `json:"objects"`
	} `json:"solution"`
}

func New(key, baseURL string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		key: strings.TrimSpace(key), baseURL: baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		pollEvery:  3 * time.Second, maxWait: 120 * time.Second,
	}
}

func NewWithProxy(key, baseURL, proxy string) (*Client, error) {
	client := New(key, baseURL)
	httpClient, err := proxyutil.HTTPClient(proxy, 30*time.Second)
	if err != nil {
		return nil, err
	}
	client.httpClient = httpClient
	return client, nil
}

func (c *Client) Classify(ctx context.Context, images [][]byte, question string) ([]int, error) {
	if c.key == "" {
		return nil, fmt.Errorf("YesCaptcha API Key 未配置")
	}
	if len(images) == 0 || len(images[0]) == 0 || strings.TrimSpace(question) == "" {
		return nil, fmt.Errorf("验证码题图或问题为空")
	}
	encoded := make([]string, 0, len(images))
	for _, image := range images {
		encoded = append(encoded, base64.StdEncoding.EncodeToString(image))
	}
	var imagePayload any = encoded
	if len(encoded) == 1 {
		imagePayload = encoded[0]
	}
	payload := map[string]any{
		"clientKey": c.key,
		"task": map[string]any{
			"type":     "FunCaptchaClassification",
			"image":    imagePayload,
			"question": strings.TrimSpace(question),
		},
	}
	var created apiResponse
	if err := c.post(ctx, "/createTask", payload, &created); err != nil {
		return nil, err
	}
	if err := responseError(created); err != nil {
		return nil, err
	}
	if len(created.Solution.Objects) > 0 {
		return created.Solution.Objects, nil
	}
	if created.TaskID == "" {
		return nil, fmt.Errorf("YesCaptcha createTask 未返回 taskId")
	}

	deadline := time.NewTimer(c.maxWait)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("YesCaptcha 识别超时")
		case <-ticker.C:
			var result apiResponse
			err := c.post(ctx, "/getTaskResult", map[string]any{
				"clientKey": c.key, "taskId": created.TaskID,
			}, &result)
			if err != nil {
				return nil, err
			}
			if err := responseError(result); err != nil {
				return nil, err
			}
			if result.Status == "ready" {
				if len(result.Solution.Objects) == 0 {
					return nil, fmt.Errorf("YesCaptcha 返回空识别结果")
				}
				return result.Solution.Objects, nil
			}
		}
	}
}

func (c *Client) post(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("YesCaptcha 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("YesCaptcha HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("YesCaptcha 响应解析失败: %w", err)
	}
	return nil
}

func responseError(resp apiResponse) error {
	if resp.ErrorID == 0 {
		return nil
	}
	detail := strings.TrimSpace(resp.ErrorCode + " " + resp.ErrorDescription)
	return fmt.Errorf("YesCaptcha: %s", detail)
}
