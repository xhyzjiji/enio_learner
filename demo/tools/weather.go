// Package tools 对应 Python 仓库的 demo/tools/：旅行相关的外部能力。
// Package tools mirrors demo/tools/ in the Python repo: external capabilities for the travel demos.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// WeatherInput 是 get_weather 的入参。字段上的 tag 决定了模型看到的 JSON Schema。
// WeatherInput is the input of get_weather. The struct tags define the JSON Schema the model sees.
//
// 这就是 InferTool 的意义：参数约束只写一遍，写在类型上。
// This is the point of InferTool: parameter constraints are declared once, on the type itself.
type WeatherInput struct {
	City string `json:"city" jsonschema:"required" jsonschema_description:"要查询天气的城市名，例如：珠海"`
}

// wttrResponse 只声明我们关心的字段。/ wttrResponse declares only the fields we care about.
type wttrResponse struct {
	CurrentCondition []struct {
		TempC       string `json:"temp_C"`
		WeatherDesc []struct {
			Value string `json:"value"`
		} `json:"weatherDesc"`
	} `json:"current_condition"`
}

// GetWeather 调用 wttr.in 查询实时天气，对应 Python 的 get_weather。
// GetWeather queries wttr.in for live weather, mirroring get_weather in Python.
func GetWeather(ctx context.Context, in WeatherInput) (string, error) {
	endpoint := fmt.Sprintf("https://wttr.in/%s?format=j1", url.PathEscape(in.City))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build weather request: %w", err)
	}

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("query weather: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("weather api returned status %d", resp.StatusCode)
	}

	var parsed wttrResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode weather response: %w", err)
	}
	if len(parsed.CurrentCondition) == 0 || len(parsed.CurrentCondition[0].WeatherDesc) == 0 {
		return "", fmt.Errorf("no weather data for city %q", in.City)
	}

	current := parsed.CurrentCondition[0]
	return fmt.Sprintf("%s当前天气:%s，气温%s摄氏度", in.City, current.WeatherDesc[0].Value, current.TempC), nil
}

// NewWeatherTool 把 GetWeather 包装成模型可调用的 Tool。
// NewWeatherTool wraps GetWeather into a model-callable Tool.
func NewWeatherTool() (tool.InvokableTool, error) {
	return utils.InferTool("get_weather", "查询指定城市的实时天气 / query the live weather of a city", GetWeather)
}
