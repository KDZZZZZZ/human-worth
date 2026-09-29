package completion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/agent"
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

// Complete 不输出原始供应商错误正文，避免日志混入密钥、材料或票数。
func (p *Provider) Complete(ctx context.Context, body []byte) (*agent.CompletionResponse, string, error) {
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
	var out agent.CompletionResponse
	if json.Unmarshal(b, &out) != nil || len(out.Choices) != 1 {
		return nil, "failed", errors.New("invalid model response")
	}
	if out.Choices[0].FinishReason != "stop" && out.Choices[0].FinishReason != "tool_calls" {
		return nil, "failed", errors.New("incomplete model response")
	}
	return &out, "succeeded", nil
}

func (p *Provider) Model() string    { return p.config.Model }
func (p *Provider) Protocol() string { return p.config.Protocol }

// Probe 用极小请求验证测试供应商协议，不包含业务材料或真人数据。
func (p *Provider) Probe(ctx context.Context) error {
	body, _ := json.Marshal(agent.CompletionRequest{Model: p.config.Model, MaxTokens: 32, Messages: []agent.Message{{Role: "user", Content: "Return exactly: OK"}}})
	response, _, err := p.Complete(ctx, body)
	if err != nil {
		return err
	}
	if !strings.Contains(response.Choices[0].Message.Content, "OK") {
		return fmt.Errorf("unexpected probe response")
	}
	return nil
}
