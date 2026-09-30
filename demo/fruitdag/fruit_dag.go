// Package fruitdag 对应 LangGraph 的 dag_demo.py：一条纯确定性的水果结账流水线，不涉及 LLM。
// Package fruitdag mirrors dag_demo.py from LangGraph: a purely deterministic fruit checkout pipeline, no LLM involved.
//
// 与 LangGraph 最大的差别：LangGraph 用一个可变的 State 字典贯穿所有节点，
// 而 Eino 让每个节点声明自己的输入输出类型，相邻节点的类型由编译器校验。
// Key difference from LangGraph: LangGraph threads one mutable State dict through every node,
// while Eino has each node declare its own input/output types, checked by the compiler between neighbours.
package fruitdag

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/compose"

	"private/agent_basedon_eino/internal/democli"
)

func init() {
	democli.Register(democli.Demo{
		Name:     "dag",
		Desc:     "水果结账流水线 / fruit checkout pipeline (dag_demo.py)",
		Category: democli.CatCompose,
		Order:    10,
		Needs:    "无 / nothing",
		Run:      Run,
	})
}

// Fruit 是一件待结算的商品。/ Fruit is a single item to be checked out.
type Fruit struct {
	Name       string  `json:"name"`
	Weight     float64 `json:"weight"`
	PricePerKg float64 `json:"price_per_kg"`
}

// Subtotal 是单件商品的小计。/ Subtotal is the per-item computation result.
type Subtotal struct {
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Subtotal float64 `json:"subtotal"`
}

// CheckoutInput 是整条链路的入参。/ CheckoutInput is the input of the whole chain.
type CheckoutInput struct {
	Fruits []Fruit `json:"fruits"`
}

// CheckoutResult 是整条链路的出参。/ CheckoutResult is the output of the whole chain.
type CheckoutResult struct {
	Subtotals   []Subtotal `json:"subtotals"`
	TotalWeight float64    `json:"total_weight"`
	TotalPrice  float64    `json:"total_price"`
}

// parseInput 对应 Python 的 parse_input：做入参校验与归一化。
// parseInput mirrors parse_input in Python: validate and normalise the incoming payload.
//
// Python 版在这里初始化 state 的各个字段；Eino 不需要，因为下游节点的输出类型自带零值。
// The Python version initialises state fields here; Eino doesn't need to, as downstream output types carry their own zero values.
func parseInput(ctx context.Context, in CheckoutInput) (CheckoutInput, error) {
	if len(in.Fruits) == 0 {
		return CheckoutInput{}, fmt.Errorf("fruits is empty")
	}
	for _, f := range in.Fruits {
		if f.Weight < 0 || f.PricePerKg < 0 {
			return CheckoutInput{}, fmt.Errorf("invalid fruit %q: weight and price must be non-negative", f.Name)
		}
	}
	return in, nil
}

// calculateItem 对应 Python 的 calculate_item：算出每件商品的小计。
// calculateItem mirrors calculate_item: compute the subtotal of each item.
func calculateItem(ctx context.Context, in CheckoutInput) ([]Subtotal, error) {
	subtotals := make([]Subtotal, 0, len(in.Fruits))
	for _, f := range in.Fruits {
		subtotals = append(subtotals, Subtotal{
			Name:     f.Name,
			Weight:   f.Weight,
			Subtotal: f.Weight * f.PricePerKg,
		})
	}
	return subtotals, nil
}

// aggregateTotal 对应 Python 的 aggregate_total：汇总总重与总价。
// aggregateTotal mirrors aggregate_total: sum up total weight and total price.
func aggregateTotal(ctx context.Context, subtotals []Subtotal) (CheckoutResult, error) {
	result := CheckoutResult{Subtotals: subtotals}
	for _, s := range subtotals {
		result.TotalWeight += s.Weight
		result.TotalPrice += s.Subtotal
	}
	return result, nil
}

// BuildChain 把三个节点串成一条 Chain 并编译成 Runnable。
// BuildChain wires the three nodes into a Chain and compiles it into a Runnable.
//
// Compile 会校验相邻节点的类型是否衔接得上，类型不匹配在这一步就会报错，而不是等到运行时。
// Compile validates that adjacent node types line up; a mismatch fails here rather than at run time.
func BuildChain(ctx context.Context) (compose.Runnable[CheckoutInput, CheckoutResult], error) {
	chain := compose.NewChain[CheckoutInput, CheckoutResult]().
		AppendLambda(compose.InvokableLambda(parseInput), compose.WithNodeName("parse_input")).
		AppendLambda(compose.InvokableLambda(calculateItem), compose.WithNodeName("calculate_item")).
		AppendLambda(compose.InvokableLambda(aggregateTotal), compose.WithNodeName("aggregate_total"))

	return chain.Compile(ctx)
}

// Run 执行一次结账流程，对应 Python 的 on_demo1()。
// Run executes one checkout, mirroring on_demo1() in Python.
func Run(ctx context.Context) error {
	runnable, err := BuildChain(ctx)
	if err != nil {
		return fmt.Errorf("build chain: %w", err)
	}

	result, err := runnable.Invoke(ctx, CheckoutInput{
		Fruits: []Fruit{
			{Name: "apple", Weight: 1.0, PricePerKg: 3.0},
			{Name: "banana", Weight: 2.0, PricePerKg: 1.5},
			{Name: "mango", Weight: 1.5, PricePerKg: 4.0},
		},
	})
	if err != nil {
		return fmt.Errorf("invoke chain: %w", err)
	}

	pretty, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	fmt.Println("=================================================")
	fmt.Printf("result = %s\n", pretty)
	fmt.Println("=================================================")
	return nil
}
