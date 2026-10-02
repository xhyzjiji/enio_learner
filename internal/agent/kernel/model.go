package kernel

import (
	"context"
	"fmt"
	"time"

	extopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"private/agent_basedon_eino/internal/agent/config"
)

const (
	// modelTimeout 给单次模型调用兜底。
	// modelTimeout bounds a single model invocation.
	modelTimeout = 180 * time.Second
)

// ModelFactory 按模型名构造 ChatModel。
//
// 模型名来自数据库中的运行时配置，API Key 来自环境变量——两者分开不是洁癖：
// 模型名需要页面可改所以进库，API Key 进库就意味着一份明文密钥躺在磁盘上。
//
// ModelFactory builds a ChatModel by model name.
//
// The model name comes from the runtime configuration in the database while the API key comes
// from the environment. Keeping them apart is not fastidiousness: the model name must be
// editable from the UI so it belongs in the database, whereas putting the API key there would
// leave a plaintext secret sitting on disk.
type ModelFactory struct{}

// NewChatModel 构造一个支持工具调用的 ChatModel。
// NewChatModel builds a tool-calling ChatModel.
func (ModelFactory) NewChatModel(ctx context.Context, name string) (model.ToolCallingChatModel, error) {
	apiKey, err := config.APIKey()
	if err != nil {
		return nil, err
	}
	temperature := float32(0.6)
	m, err := extopenai.NewChatModel(ctx, &extopenai.ChatModelConfig{
		APIKey:      apiKey,
		BaseURL:     config.BaseURL(),
		Model:       name,
		Temperature: &temperature,
		Timeout:     modelTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("构造模型 %s 失败 / cannot build model %s: %w", name, name, err)
	}
	return m, nil
}
