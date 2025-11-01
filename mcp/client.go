package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Provider AI提供商類型
type Provider string

const (
	ProviderDeepSeek Provider = "deepseek"
	ProviderQwen     Provider = "qwen"
	ProviderOpenAI   Provider = "openai"
	ProviderGemini   Provider = "gemini"
	ProviderCustom   Provider = "custom"
)

// Client AI API配置
type Client struct {
	Provider   Provider
	APIKey     string
	SecretKey  string // 阿裡雲需要
	BaseURL    string
	Model      string
	Timeout    time.Duration
	UseFullURL bool // 是否使用完整URL（不添加/chat/completions）
}

func New() *Client {
	// 默認配置
	var defaultClient = Client{
		Provider: ProviderDeepSeek,
		BaseURL:  "https://api.deepseek.com/v1",
		Model:    "deepseek-chat",
		Timeout:  120 * time.Second, // 增加到120秒，因為AI需要分析大量數據
	}
	return &defaultClient
}

// SetDeepSeekAPIKey 設置DeepSeek API密鑰
func (cfg *Client) SetDeepSeekAPIKey(apiKey string) {
	cfg.Provider = ProviderDeepSeek
	cfg.APIKey = apiKey
	cfg.BaseURL = "https://api.deepseek.com/v1"
	cfg.Model = "deepseek-chat"
}

// SetQwenAPIKey 設置阿裡雲Qwen API密鑰
func (cfg *Client) SetQwenAPIKey(apiKey, secretKey string) {
	cfg.Provider = ProviderQwen
	cfg.APIKey = apiKey
	cfg.SecretKey = secretKey
	cfg.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	cfg.Model = "qwen-plus" // 可選: qwen-turbo, qwen-plus, qwen-max
}

// SetOpenAIAPIKey 設置OpenAI API密鑰
func (cfg *Client) SetOpenAIAPIKey(apiKey, modelName string) {
	cfg.Provider = ProviderOpenAI
	cfg.APIKey = apiKey
	cfg.BaseURL = "https://api.openai.com/v1"
	if modelName != "" {
		cfg.Model = modelName
	} else {
		cfg.Model = "gpt-4o-mini" // 默認使用 gpt-4o-mini，性價比最高
	}
	cfg.Timeout = 120 * time.Second
}

// SetGeminiAPIKey 設置Google Gemini API密鑰
func (cfg *Client) SetGeminiAPIKey(apiKey, modelName string) {
	cfg.Provider = ProviderGemini
	cfg.APIKey = apiKey
	cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	if modelName != "" {
		cfg.Model = modelName
	} else {
		cfg.Model = "gemini-1.5-flash" // 默認使用 gemini-1.5-flash，速度快且經濟
	}
	cfg.Timeout = 120 * time.Second
}

// SetCustomAPI 設置自定義OpenAI兼容API
func (cfg *Client) SetCustomAPI(apiURL, apiKey, modelName string) {
	cfg.Provider = ProviderCustom
	cfg.APIKey = apiKey

	// 檢查URL是否以#結尾，如果是則使用完整URL（不添加/chat/completions）
	if strings.HasSuffix(apiURL, "#") {
		cfg.BaseURL = strings.TrimSuffix(apiURL, "#")
		cfg.UseFullURL = true
	} else {
		cfg.BaseURL = apiURL
		cfg.UseFullURL = false
	}

	cfg.Model = modelName
	cfg.Timeout = 120 * time.Second
}

// SetClient 設置完整的AI配置（高級用戶）
func (cfg *Client) SetClient(client Client) {
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	*cfg = client
}

// CallWithMessages 使用 system + user prompt 調用AI API（推薦）
func (cfg *Client) CallWithMessages(systemPrompt, userPrompt string) (string, error) {
	if cfg.APIKey == "" {
		return "", fmt.Errorf("AI API密鑰未設置，請先調用 SetDeepSeekAPIKey()、SetQwenAPIKey()、SetOpenAIAPIKey() 或 SetGeminiAPIKey()")
	}

	// 重試配置
	maxRetries := 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			fmt.Printf("⚠️  AI API調用失敗，正在重試 (%d/%d)...\n", attempt, maxRetries)
		}

		result, err := cfg.callOnce(systemPrompt, userPrompt)
		if err == nil {
			if attempt > 1 {
				fmt.Printf("✓ AI API重試成功\n")
			}
			return result, nil
		}

		lastErr = err
		// 如果不是網絡錯誤，不重試
		if !isRetryableError(err) {
			return "", err
		}

		// 重試前等待
		if attempt < maxRetries {
			waitTime := time.Duration(attempt) * 2 * time.Second
			fmt.Printf("⏳ 等待%v後重試...\n", waitTime)
			time.Sleep(waitTime)
		}
	}

	return "", fmt.Errorf("重試%d次後仍然失敗: %w", maxRetries, lastErr)
}

// callOnce 單次調用AI API（內部使用）
func (cfg *Client) callOnce(systemPrompt, userPrompt string) (string, error) {
	// Gemini 使用不同的API格式
	if cfg.Provider == ProviderGemini {
		return cfg.callGemini(systemPrompt, userPrompt)
	}

	// 構建 messages 數組
	messages := []map[string]string{}

	// 如果有 system prompt，添加 system message
	if systemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": systemPrompt,
		})
	}

	// 添加 user message
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": userPrompt,
	})

	// 構建請求體
	requestBody := map[string]interface{}{
		"model":    cfg.Model,
		"messages": messages,
	}

	// 根據不同 Provider 設置參數
	// OpenAI 某些新模型（如 gpt-5-mini）對參數有嚴格限制，使用默認值
	if cfg.Provider == ProviderOpenAI {
		requestBody["max_completion_tokens"] = 2000
		// 不設置 temperature，使用默認值 1.0
	} else {
		// DeepSeek/Qwen 可以自定義參數
		requestBody["max_tokens"] = 2000
		requestBody["temperature"] = 0.5 // 降低temperature以提高JSON格式穩定性
	}

	// 注意：response_format 參數僅 OpenAI 支持，DeepSeek/Qwen 不支持
	// 我們通過強化 prompt 和後處理來確保 JSON 格式正確

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("序列化請求失敗: %w", err)
	}

	// 創建HTTP請求
	var url string
	if cfg.UseFullURL {
		// 使用完整URL，不添加/chat/completions
		url = cfg.BaseURL
	} else {
		// 默認行為：添加/chat/completions
		url = fmt.Sprintf("%s/chat/completions", cfg.BaseURL)
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("創建請求失敗: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// 根據不同的Provider設置認證方式
	switch cfg.Provider {
	case ProviderDeepSeek:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))
	case ProviderQwen:
		// 阿裡雲Qwen使用API-Key認證
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))
		// 注意：如果使用的不是兼容模式，可能需要不同的認證方式
	case ProviderOpenAI:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))
	default:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))
	}

	// 發送請求
	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("發送請求失敗: %w", err)
	}
	defer resp.Body.Close()

	// 讀取響應
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("讀取響應失敗: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API返回錯誤 (status %d): %s", resp.StatusCode, string(body))
	}

	// 解析響應
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析響應失敗: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("API返回空響應")
	}

	return result.Choices[0].Message.Content, nil
}

// callGemini 調用Gemini API（使用官方 Go SDK）
func (cfg *Client) callGemini(systemPrompt, userPrompt string) (string, error) {
	ctx := context.Background()

	// 創建 Gemini 客戶端
	client, err := genai.NewClient(ctx, option.WithAPIKey(cfg.APIKey))
	if err != nil {
		return "", fmt.Errorf("創建Gemini客戶端失敗: %w", err)
	}
	defer client.Close()

	// 獲取模型
	model := client.GenerativeModel(cfg.Model)

	// 配置生成參數
	model.SetTemperature(0.5)
	model.SetMaxOutputTokens(2000)

	// 合併 system prompt 和 user prompt
	combinedPrompt := systemPrompt
	if systemPrompt != "" && userPrompt != "" {
		combinedPrompt += "\n\n" + userPrompt
	} else if userPrompt != "" {
		combinedPrompt = userPrompt
	}

	// 生成內容
	resp, err := model.GenerateContent(ctx, genai.Text(combinedPrompt))
	if err != nil {
		return "", fmt.Errorf("Gemini API調用失敗: %w", err)
	}

	// 檢查響應
	if resp == nil {
		return "", fmt.Errorf("Gemini API返回空響應")
	}

	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("Gemini API返回空候選結果")
	}

	if resp.Candidates[0].Content == nil {
		return "", fmt.Errorf("Gemini API候選結果無內容")
	}

	if len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("Gemini API內容無部分")
	}

	// 提取文本
	var result strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if text, ok := part.(genai.Text); ok {
			result.WriteString(string(text))
		}
	}

	text := result.String()
	if text == "" {
		return "", fmt.Errorf("Gemini API返回空文本")
	}

	return text, nil
}

// isRetryableError 判斷錯誤是否可重試
func isRetryableError(err error) bool {
	errStr := err.Error()
	// 網絡錯誤、超時、EOF等可以重試
	retryableErrors := []string{
		"EOF",
		"timeout",
		"connection reset",
		"connection refused",
		"temporary failure",
		"no such host",
	}
	for _, retryable := range retryableErrors {
		if strings.Contains(errStr, retryable) {
			return true
		}
	}
	return false
}
