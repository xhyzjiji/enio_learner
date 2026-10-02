package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"private/agent_basedon_eino/internal/agent/store"
)

// slotPattern 匹配参数模板里的槽位，形如 {{path}}。
// slotPattern matches template slots of the form {{path}}.
var slotPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// ParamSpec 描述一个模型可填的参数。
// ParamSpec describes one model-fillable parameter.
type ParamSpec struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ToolDef 是页面定义的一个声明式 CLI 工具。
//
// 模型只能往 ArgsTemplate 预留的槽位里填值，不能改变命令本身，也不能追加参数。
// 这是声明式工具与 execute 的根本区别：前者的攻击面等于"某个参数的取值"，
// 后者的攻击面等于"整个 shell"。
//
// ToolDef is one declarative CLI tool defined in the UI.
//
// The model can only fill the slots reserved in ArgsTemplate; it cannot change the command
// itself or append arguments. That is the fundamental difference from execute: the attack
// surface here is the value of one argument, whereas there it is an entire shell.
type ToolDef struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	Command      string               `json:"command"`
	ArgsTemplate []string             `json:"args_template"`
	Params       map[string]ParamSpec `json:"params"`
	WorkDir      string               `json:"work_dir"`
	TimeoutSec   int                  `json:"timeout_sec"`
	Enabled      bool                 `json:"enabled"`
	CreatedAt    int64                `json:"created_at"`
}

// Store 管理 cli_tools 与 cli_policy 两张表。
// Store manages the cli_tools and cli_policy tables.
type Store struct {
	db *store.DB
}

// NewStore 构造存储层。
// NewStore builds the storage layer.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

// ErrNotFound 表示记录不存在。
// ErrNotFound signals a missing record.
var ErrNotFound = errors.New("cli tool not found")

// ListTools 返回全部声明式工具定义。
// ListTools returns every declarative tool definition.
func (s *Store) ListTools(ctx context.Context) ([]*ToolDef, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT id, name, description, command, args_template, params, work_dir, timeout_sec, enabled, created_at
		FROM cli_tools ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list cli tools: %w", err)
	}
	defer rows.Close()

	var out []*ToolDef
	for rows.Next() {
		d, err := scanToolDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetTool 返回单个工具定义。
// GetTool returns one tool definition.
func (s *Store) GetTool(ctx context.Context, id string) (*ToolDef, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT id, name, description, command, args_template, params, work_dir, timeout_sec, enabled, created_at
		FROM cli_tools WHERE id = ?`, id)
	d, err := scanToolDef(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func scanToolDef(sc interface{ Scan(...any) error }) (*ToolDef, error) {
	var (
		d         ToolDef
		argsJSON  string
		paramJSON string
	)
	if err := sc.Scan(&d.ID, &d.Name, &d.Description, &d.Command, &argsJSON, &paramJSON,
		&d.WorkDir, &d.TimeoutSec, &d.Enabled, &d.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(argsJSON), &d.ArgsTemplate); err != nil {
		return nil, fmt.Errorf("工具 %s 的参数模板不是合法 JSON / args_template of tool %s is not valid JSON: %w",
			d.Name, d.Name, err)
	}
	if err := json.Unmarshal([]byte(paramJSON), &d.Params); err != nil {
		return nil, fmt.Errorf("工具 %s 的参数声明不是合法 JSON / params of tool %s is not valid JSON: %w",
			d.Name, d.Name, err)
	}
	return &d, nil
}

// SaveTool 新增或覆盖一个工具定义。
// SaveTool inserts or replaces a tool definition.
func (s *Store) SaveTool(ctx context.Context, d *ToolDef) (*ToolDef, error) {
	if err := ValidateToolDef(d); err != nil {
		return nil, err
	}
	if d.ID == "" {
		d.ID = uuid.NewString()
		d.CreatedAt = store.Now()
	}
	args, _ := json.Marshal(d.ArgsTemplate)
	params, _ := json.Marshal(d.Params)
	_, err := s.db.Write().ExecContext(ctx, `
		INSERT INTO cli_tools (id, name, description, command, args_template, params, work_dir, timeout_sec, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, description = excluded.description, command = excluded.command,
			args_template = excluded.args_template, params = excluded.params,
			work_dir = excluded.work_dir, timeout_sec = excluded.timeout_sec, enabled = excluded.enabled`,
		d.ID, d.Name, d.Description, d.Command, string(args), string(params),
		d.WorkDir, d.TimeoutSec, d.Enabled, d.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("save cli tool %s: %w", d.Name, err)
	}
	return d, nil
}

// DeleteTool 删除一个工具定义。
// DeleteTool removes a tool definition.
func (s *Store) DeleteTool(ctx context.Context, id string) error {
	res, err := s.db.Write().ExecContext(ctx, `DELETE FROM cli_tools WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete cli tool %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListPolicy 返回全部 execute 策略规则。
// ListPolicy returns every execute policy rule.
func (s *Store) ListPolicy(ctx context.Context) ([]PolicyRule, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, pattern, action, note FROM cli_policy ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list cli policy: %w", err)
	}
	defer rows.Close()

	var out []PolicyRule
	for rows.Next() {
		var r PolicyRule
		if err := rows.Scan(&r.ID, &r.Pattern, &r.Action, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SavePolicy 新增一条策略规则，写入前先验证正则可编译。
// SavePolicy adds a policy rule, verifying the regexp compiles before writing.
func (s *Store) SavePolicy(ctx context.Context, r PolicyRule) (PolicyRule, error) {
	if _, err := regexp.Compile(r.Pattern); err != nil {
		return r, fmt.Errorf("正则表达式无效 / invalid regexp %q: %w", r.Pattern, err)
	}
	if r.Action != PolicyAllow && r.Action != PolicyDeny {
		return r, fmt.Errorf("action 只能是 allow 或 deny / action must be allow or deny, got %q", r.Action)
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	_, err := s.db.Write().ExecContext(ctx, `
		INSERT INTO cli_policy (id, pattern, action, note, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET pattern = excluded.pattern, action = excluded.action, note = excluded.note`,
		r.ID, r.Pattern, r.Action, r.Note, store.Now())
	if err != nil {
		return r, fmt.Errorf("save cli policy: %w", err)
	}
	return r, nil
}

// DeletePolicy 删除一条策略规则。
// DeletePolicy removes a policy rule.
func (s *Store) DeletePolicy(ctx context.Context, id string) error {
	res, err := s.db.Write().ExecContext(ctx, `DELETE FROM cli_policy WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete cli policy %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ValidateToolDef 校验一个工具定义。
//
// 最关键的一条是槽位与参数声明必须一一对应：模板里有 {{path}} 却没声明 path，
// 模型就无从得知该填什么；声明了 path 却模板里不用，那个参数就是个空洞的接口。
// 两种情况都是配置错误，应当在保存时就说清楚，而不是等模型调用失败。
//
// ValidateToolDef validates a tool definition.
//
// The critical rule is that slots and parameter declarations must correspond exactly: a
// {{path}} in the template with no declared path leaves the model no way to know what to
// supply, while a declared path unused by the template is a hollow interface. Both are
// configuration errors and should be spelled out at save time, not discovered when the model
// calls the tool.
func ValidateToolDef(d *ToolDef) error {
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("工具名不能为空 / tool name must not be empty")
	}
	if strings.TrimSpace(d.Command) == "" {
		return errors.New("命令不能为空 / command must not be empty")
	}
	if strings.ContainsAny(d.Command, ";|&$`") {
		return fmt.Errorf(
			"命令 %q 含 shell 元字符。声明式工具走 exec 直接执行，不经 shell，"+
				"需要管道请拆成多个工具或改用 execute / command %q contains shell metacharacters. "+
				"Declarative tools are executed directly without a shell; split into several tools or use "+
				"execute if you need a pipeline", d.Command, d.Command)
	}

	declared := make(map[string]bool, len(d.Params))
	for name := range d.Params {
		declared[name] = true
	}
	used := make(map[string]bool)
	for _, arg := range d.ArgsTemplate {
		for _, m := range slotPattern.FindAllStringSubmatch(arg, -1) {
			used[m[1]] = true
			if !declared[m[1]] {
				return fmt.Errorf(
					"参数模板使用了槽位 {{%s}}，但 params 里没有声明它 / the template uses slot {{%s}} "+
						"which is not declared in params", m[1], m[1])
			}
		}
	}
	for name := range declared {
		if !used[name] {
			return fmt.Errorf(
				"参数 %s 已声明但参数模板里没有用到 / parameter %s is declared but never used in the template",
				name, name)
		}
	}
	return nil
}

// BuildTools 把已启用的工具定义构造成可挂载的 Eino 工具。
//
// 这里必须用 utils.NewTool 配 schema.NewParamsOneOfByParams，不能用 utils.InferTool：
// 后者靠编译期泛型从 Go 结构体推导 schema，而这些工具是用户在页面上定义的，
// 编译时根本不存在对应的类型（T030 闸门验证的正是这一点）。
//
// BuildTools turns enabled definitions into mountable Eino tools.
//
// This must use utils.NewTool with schema.NewParamsOneOfByParams rather than utils.InferTool:
// the latter derives its schema from a Go struct via compile-time generics, whereas these tools
// are defined by the user in the UI and have no corresponding type at compile time. Gate T030
// verified exactly this.
func BuildTools(ctx context.Context, defs []*ToolDef, runner *Runner, maxBytes int) ([]tool.BaseTool, error) {
	var out []tool.BaseTool
	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		t, err := buildTool(d, runner, maxBytes)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	_ = ctx
	return out, nil
}

func buildTool(d *ToolDef, runner *Runner, maxBytes int) (tool.BaseTool, error) {
	params := make(map[string]*schema.ParameterInfo, len(d.Params))
	for name, spec := range d.Params {
		params[name] = &schema.ParameterInfo{
			Type:     schema.DataType(orDefault(spec.Type, "string")),
			Desc:     spec.Description,
			Required: spec.Required,
		}
	}
	info := &schema.ToolInfo{
		Name:        d.Name,
		Desc:        d.Description,
		ParamsOneOf: schema.NewParamsOneOfByParams(params),
	}

	def := *d // 值拷贝，避免闭包捕获到后续被修改的定义。/ value copy so the closure cannot capture a later mutation
	handler := func(ctx context.Context, args map[string]any) (string, error) {
		argv, err := renderArgs(&def, args)
		if err != nil {
			return "", err
		}
		res, err := runner.Run(ctx, &ExecRequest{
			Command:        def.Command,
			Args:           argv,
			WorkDir:        def.WorkDir,
			Timeout:        time.Duration(def.TimeoutSec) * time.Second,
			MaxOutputBytes: maxBytes,
		})
		if err != nil {
			return "", err
		}
		return res.Output, nil
	}
	return utils.NewTool[map[string]any, string](info, handler), nil
}

// renderArgs 把模型提供的参数填入模板槽位。
//
// 填充结果作为 argv 的一个元素整体传出，不做任何 shell 解析。所以即使模型把
// "; rm -rf ~" 填进某个槽位，它也只是那条命令的一个普通字符串参数。
//
// renderArgs fills model-supplied values into the template slots.
//
// Each result becomes one whole element of argv with no shell parsing at any point. So even if
// the model fills a slot with "; rm -rf ~", it remains an ordinary string argument of that
// command.
func renderArgs(d *ToolDef, args map[string]any) ([]string, error) {
	for name, spec := range d.Params {
		if !spec.Required {
			continue
		}
		if v, ok := args[name]; !ok || v == nil || v == "" {
			return nil, fmt.Errorf("缺少必填参数 %s / required parameter %s is missing", name, name)
		}
	}

	out := make([]string, 0, len(d.ArgsTemplate))
	for _, tmpl := range d.ArgsTemplate {
		var missing string
		rendered := slotPattern.ReplaceAllStringFunc(tmpl, func(slot string) string {
			name := slotPattern.FindStringSubmatch(slot)[1]
			v, ok := args[name]
			if !ok || v == nil {
				missing = name
				return ""
			}
			return fmt.Sprint(v)
		})
		// 可选参数没提供时，整个参数项丢弃而不是留一个空串：
		// 很多命令收到空串参数的行为与不传该参数完全不同。
		// When an optional parameter is absent the whole argument is dropped rather than left as
		// an empty string: many commands behave quite differently given "" versus nothing.
		if missing != "" {
			continue
		}
		out = append(out, rendered)
	}
	return out, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
