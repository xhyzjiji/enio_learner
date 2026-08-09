// Package llm 统一构造 ChatModel，供各个 demo 复用。
// Package llm centralises ChatModel construction so every demo can share it.
//
// Python 版把智谱的 API Key 硬编码在了源码里；这里一律走环境变量。
// The Python version hardcodes the ZhipuAI API key in source; here everything comes from the environment.
package llm

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

const (
	// 智谱开放平台的 OpenAI 兼容端点。/ ZhipuAI's OpenAI-compatible endpoint.
	defaultApiKey  = ""
	defaultBaseURL = "https://open.bigmodel.cn/api/paas/v4"
	defaultModel   = "glm-4.5-air"
	defaultTimeout = 120 * time.Second
)

// NewChatModel 按环境变量构造一个 ChatModel。
// NewChatModel builds a ChatModel from environment variables.
//
// 返回 ToolCallingChatModel 而不是 BaseChatModel，因为后面带工具的 demo 需要 WithTools。
// It returns ToolCallingChatModel rather than BaseChatModel because later tool-using demos need WithTools.
func NewChatModel(ctx context.Context) (model.ToolCallingChatModel, error) {
	apiKey := os.Getenv("ZHIPUAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("environment variable ZHIPUAI_API_KEY is not set")
		//apiKey = defaultApiKey
	}

	temperature := float32(0.5)
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:      apiKey,
		BaseURL:     envOrDefault("ZHIPUAI_BASE_URL", defaultBaseURL),
		Model:       envOrDefault("ZHIPUAI_MODEL", defaultModel),
		Temperature: &temperature,
		Timeout:     defaultTimeout,
	})
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
