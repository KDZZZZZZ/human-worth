package challengeworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ProviderConfig 从仓库外的 0600 文件读取，密钥仅留在可信 worker，不进入任务包。
type ProviderConfig struct {
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	Protocol string `json:"protocol"`
	APIKey   string `json:"api_key"`
}
type Provider struct {
	config   ProviderConfig
	client   *http.Client
	endpoint string
}

func LoadProvider(path string) (*Provider, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("model configuration must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("model configuration unavailable")
	}
	defer f.Close()
	var cfg ProviderConfig
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid model configuration")
	}
	return NewProvider(cfg, nil)
}

// NewProvider 固定供应商地址和协议，禁止自动重试和携带凭据跟随重定向。
func NewProvider(cfg ProviderConfig, client *http.Client) (*Provider, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || cfg.Model == "" || cfg.APIKey == "" || cfg.Protocol != "completion" {
		return nil, errors.New("invalid model provider")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return nil, errors.New("model provider requires HTTPS")
	}
	base := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	u.Path = base + "/chat/completions"
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Provider{config: cfg, client: &copy, endpoint: u.String()}, nil
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type completionRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Tools       []any     `json:"tools,omitempty"`
}
type completionResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// complete 不输出原始供应商错误正文，避免日志混入密钥、材料或票数。
func (p *Provider) complete(ctx context.Context, body []byte) (*completionResponse, string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "failed", errors.New("invalid model request")
	}
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return nil, "unknown", errors.New("model outcome unknown")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, "failed", errors.New("model request rejected")
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(b) > 2<<20 || !json.Valid(b) {
		return nil, "unknown", errors.New("model outcome unknown")
	}
	var out completionResponse
	if json.Unmarshal(b, &out) != nil || len(out.Choices) != 1 {
		return nil, "failed", errors.New("invalid model response")
	}
	if out.Choices[0].FinishReason != "stop" && out.Choices[0].FinishReason != "tool_calls" {
		return nil, "failed", errors.New("incomplete model response")
	}
	return &out, "succeeded", nil
}
