package yescaptcha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClassifyPollsUntilReady(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/createTask" {
			task := body["task"].(map[string]any)
			if task["type"] != "FunCaptchaClassification" || task["question"] != "Pick the bread" || task["image"] == "" {
				t.Fatalf("unexpected task: %#v", task)
			}
			_, _ = w.Write([]byte(`{"errorId":0,"taskId":"fixture-task"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errorId":0,"status":"ready","solution":{"objects":[4]}}`))
	}))
	defer server.Close()

	client := New("fixture-key", server.URL)
	client.pollEvery = time.Millisecond
	objects, err := client.Classify(context.Background(), [][]byte{[]byte("png")}, "Pick the bread")
	if err != nil || len(objects) != 1 || objects[0] != 4 || calls != 2 {
		t.Fatalf("objects=%v calls=%d err=%v", objects, calls, err)
	}
}

func TestClassifySupportsImmediateResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errorId":0,"status":"ready","solution":{"objects":[2]}}`))
	}))
	defer server.Close()
	objects, err := New("fixture-key", server.URL).Classify(context.Background(), [][]byte{[]byte("png")}, "Pick any square")
	if err != nil || len(objects) != 1 || objects[0] != 2 {
		t.Fatalf("objects=%v err=%v", objects, err)
	}
}

func TestNewWithProxyRejectsInvalidProxy(t *testing.T) {
	if _, err := NewWithProxy("fixture-key", "", "ftp://proxy.example.test:21"); err == nil {
		t.Fatal("expected invalid proxy error")
	}
}
