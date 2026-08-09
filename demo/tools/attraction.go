package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const tavilySearchURL = "https://api.tavily.com/search"
const tavilyDefaultApiKey = ""

// AttractionInput 是 get_attraction 的入参。/ AttractionInput is the input of get_attraction.
type AttractionInput struct {
	City    string `json:"city" jsonschema:"required" jsonschema_description:"城市名，例如：珠海"`
	Weather string `json:"weather" jsonschema:"required" jsonschema_description:"该城市当前的天气，需先由 get_weather 查得"`
}

type tavilyRequest struct {
	Query         string `json:"query"`
	SearchDepth   string `json:"search_depth"`
	IncludeAnswer bool   `json:"include_answer"`
}

type tavilyResponse struct {
	Answer  string `json:"answer"`
	Results []struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	} `json:"results"`
}

// GetAttraction 结合城市与天气搜索景点推荐，对应 Python 的 get_attraction。
// GetAttraction searches attraction recommendations for a city and weather, mirroring get_attraction in Python.
//
// Python 版用的是 tavily-python SDK；这里直接调 REST 接口，省掉一个依赖。
// The Python version uses the tavily-python SDK; here we call the REST endpoint directly to avoid a dependency.
func GetAttraction(ctx context.Context, in AttractionInput) (string, error) {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("environment variable TAVILY_API_KEY is not set")
		//apiKey = tavilyDefaultApiKey
	}

	body, err := json.Marshal(tavilyRequest{
		Query:         fmt.Sprintf("'%s' 在'%s'天气下最值得去的旅游景点推荐及理由", in.City, in.Weather),
		SearchDepth:   "basic",
		IncludeAnswer: true,
	})
	if err != nil {
		return "", fmt.Errorf("build search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilySearchURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("search attraction: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tavily api returned status %d", resp.StatusCode)
	}

	var parsed tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode search response: %w", err)
	}

	// include_answer 为真时 Tavily 会给一段综合回答，优先用它。
	// With include_answer set, Tavily returns a synthesised answer; prefer it.
	if parsed.Answer != "" {
		return parsed.Answer, nil
	}

	if len(parsed.Results) == 0 {
		return "抱歉，没有找到相关的旅游景点推荐。", nil
	}

	var sb strings.Builder
	sb.WriteString("根据搜索，为您找到以下信息:")
	for _, r := range parsed.Results {
		fmt.Fprintf(&sb, "\n- %s: %s", r.Title, r.Content)
	}
	return sb.String(), nil
}

// NewAttractionTool 把 GetAttraction 包装成模型可调用的 Tool。
// NewAttractionTool wraps GetAttraction into a model-callable Tool.
func NewAttractionTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"get_attraction",
		"根据城市和天气搜索推荐的旅游景点 / search attractions for a city given its weather",
		GetAttraction,
	)
}
