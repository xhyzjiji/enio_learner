// Package secrets 管理第三方工具使用的凭证。
//
// 它和模型密钥（ZHIPUAI_API_KEY）不是一回事，区别在于**谁来用**：模型密钥只有本
// 进程自己用，放在环境变量里就够了；而工具凭证要给 execute 启动的子进程用，子进程
// 环境走白名单（见 tools/cli/exec.go 的 allowedEnvKeys），拿不到本进程的任何变量。
// 于是 export 一个 TAVILY_API_KEY 再让技能去读 $TAVILY_API_KEY，结果永远是空串——
// 而且不报错：curl 照常发得出去，只是换回一个 401，排查时很难想到是这里。
//
// 选择落文件而不是落库，有两条理由：
//   - agent.db 会被备份、复制、随仓库挪到别的机器，凭证混在里面就跟着走一圈；
//     独立文件可以单独授权、单独删除，也不会被误 commit。
//   - 子进程需要的是一个"能读出来的东西"。存库的话还得再找一条路把值递过去，
//     而最顺手的那条路——注入环境变量——恰恰是上面刚刚避开的。
//
// 必须说清楚它防的是什么：它**不防模型读取**。只要 execute 开着，模型随时可以
// cat 这个文件，这是 execute 的本质，不是本包的疏漏。它防的是**凭证进入对话历史**：
// 用户直接粘进聊天框的密钥会落进 messages 表，此后每一轮都被重新送回模型上下文，
// 并随会话导出和日志扩散——被动、持续、看不见。文件里的凭证只在模型主动去读的
// 那一次才暴露，而那一次会留在工具调用记录里，你看得见。
//
// Package secrets manages credentials used by third-party tools.
//
// These are distinct from the model API key (ZHIPUAI_API_KEY), and the difference is WHO USES
// them: the model key is consumed by this process alone, so an environment variable suffices;
// tool credentials are consumed by child processes spawned through execute, whose environment is
// whitelisted (see allowedEnvKeys in tools/cli/exec.go) and therefore inherits nothing. Exporting
// TAVILY_API_KEY and having a skill read $TAVILY_API_KEY yields an empty string every time — and
// raises no error: curl still fires, it just comes back 401, which is hard to trace to this.
//
// Files rather than the database, for two reasons:
//   - agent.db gets backed up, copied and carried to other machines along with the repository,
//     and credentials mixed into it travel along. A standalone file can be permissioned and
//     deleted on its own, and will not be committed by accident.
//   - A child process needs something it can read. Storing in the database would require another
//     path to hand the value over, and the most convenient such path — injecting it into the
//     environment — is exactly what was just avoided.
//
// What this does NOT protect against must be stated plainly: it does not stop the model from
// reading a credential. While execute is enabled the model can cat the file at any time; that is
// the nature of execute, not an oversight here. What it does prevent is credentials ENTERING THE
// CONVERSATION HISTORY: a key pasted into the chat box lands in the messages table, is replayed
// into the model's context on every subsequent turn, and spreads through session exports and
// logs — passively, continuously, invisibly. A credential in a file is exposed only on the one
// occasion the model deliberately reads it, and that occasion is visible in the tool-call record.
package secrets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// dirPerm 与 filePerm 是这套机制存在的全部理由，所以显式写死，不依赖 umask。
	// dirPerm and filePerm are the entire reason this mechanism exists, so they are set
	// explicitly rather than left to the umask.
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// nameRE 限定凭证名的形状。
//
// 名字会直接变成文件名，所以必须让 ".." 和 "/" 在语法上就写不出来，而不是靠逐个
// 排查分隔符——黑名单总有漏网的写法。限定成大写字母、数字和下划线正好做到这一点，
// 顺带让名字看起来就是大家熟悉的环境变量样式。
//
// nameRE constrains the shape of a credential name.
//
// The name becomes a filename directly, so ".." and "/" must be unexpressible by grammar rather
// than screened out separator by separator — a blacklist always misses a spelling. Restricting
// to uppercase letters, digits and underscores achieves exactly that, and incidentally makes
// names look like the environment variables people already expect.
var nameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// ErrNotFound 表示该凭证未配置。
// ErrNotFound signals that the credential is not configured.
var ErrNotFound = errors.New("secret not found")

// Info 描述一条已配置的凭证。**不含值**，而且永远不会含值。
// Info describes one configured credential. It carries NO value, and never will.
type Info struct {
	Name string `json:"name"`
	// Path 是凭证文件的绝对路径。它不是秘密，而且必须给出来——
	// 命令里要靠它写出 $(cat <path>)。
	// Path is the absolute path of the credential file. It is not a secret and must be exposed:
	// commands need it to write $(cat <path>).
	Path      string `json:"path"`
	UpdatedAt int64  `json:"updated_at"`
}

// Store 是一个凭证目录。
// Store is one directory of credentials.
type Store struct {
	dir string
}

// NewStore 打开（必要时创建）凭证目录。
//
// 已存在的目录也会被 Chmod 一次：MkdirAll 对已存在的目录不改权限，而一个 0755 的
// 凭证目录等于本机其他用户都能列出你配了哪些凭证。
//
// NewStore opens the credential directory, creating it when absent.
//
// An existing directory is chmod'd as well: MkdirAll leaves the permissions of an existing
// directory alone, and a 0755 credential directory lets every other local user enumerate which
// credentials you have configured.
func NewStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析凭证目录失败 / resolve credential directory %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("创建凭证目录失败 / create credential directory %q: %w", abs, err)
	}
	if err := os.Chmod(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("设置凭证目录权限失败 / chmod credential directory %q: %w", abs, err)
	}
	return &Store{dir: abs}, nil
}

// Dir 返回凭证目录的绝对路径。
// Dir returns the absolute path of the credential directory.
func (s *Store) Dir() string { return s.dir }

// List 返回全部已配置的凭证名，按字典序。
// List returns every configured credential name in lexical order.
func (s *Store) List() ([]Info, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		// 目录被人手工删掉是完全正常的状态，等同于"一条都没配"。
		// The directory having been removed by hand is a perfectly normal state, equivalent to
		// "nothing configured".
		return []Info{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取凭证目录失败 / read credential directory: %w", err)
	}

	out := make([]Info, 0, len(entries))
	for _, e := range entries {
		// 不合名字规则的一律跳过：写入时留下的临时文件、编辑器的备份文件、
		// 手工丢进来的杂物，都在这里被滤掉，不会被当成一条凭证报给模型。
		//
		// Anything not matching the name rule is skipped: temp files from writes, editor
		// backups and hand-dropped clutter are filtered here rather than reported to the model
		// as a credential.
		if e.IsDir() || !nameRE.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{
			Name:      e.Name(),
			Path:      filepath.Join(s.dir, e.Name()),
			UpdatedAt: info.ModTime().UnixMilli(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Path 返回某条凭证的文件路径，凭证不存在时返回 ErrNotFound。
// Path returns the file path of one credential, or ErrNotFound when it does not exist.
func (s *Store) Path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	p := filepath.Join(s.dir, name)
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	return p, nil
}

// Set 写入或覆盖一条凭证。
//
// 值会先 TrimSpace 再写，且不补结尾换行。这不是洁癖：从输入框粘贴极易带上一个
// 换行，而 `curl -H "Authorization: Bearer $(cat f)"` 里 $() 会吃掉结尾换行、
// `cmd < f` 却不会，于是同一个文件在两种读法下一个能用一个报 400，排查起来毫无头绪。
//
// Set writes or replaces one credential.
//
// The value is trimmed before writing and no trailing newline is added. This is not fastidious:
// pasting from an input box readily carries a newline along, and while $() strips trailing
// newlines in `curl -H "Authorization: Bearer $(cat f)"`, `cmd < f` does not — so the same file
// works under one reading and returns 400 under the other, with nothing to go on.
func (s *Store) Set(name, value string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	v := strings.TrimSpace(value)
	if v == "" {
		return fmt.Errorf("凭证 %s 的值不能为空 / the value of credential %s must not be empty", name, name)
	}
	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return fmt.Errorf("创建凭证目录失败 / create credential directory: %w", err)
	}

	// 先写临时文件再 rename，而不是直接截断重写：写到一半被打断时，原先那份仍然
	// 完整可用。直接重写会留下半截的凭证，而半截的凭证和错的凭证表现完全一样——401。
	//
	// Write to a temporary file and rename rather than truncating in place: if the write is
	// interrupted halfway, the previous value is still intact and usable. Truncating leaves a
	// half-written credential, which is indistinguishable from a wrong one — both are 401.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败 / create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// rename 成功后这次 Remove 会失败，无妨；失败路径上它才是真正起作用的那次。
	// After a successful rename this Remove fails harmlessly; it earns its keep on the error paths.
	defer func() { _ = os.Remove(tmpName) }()

	// CreateTemp 建出来本就是 0600，这里仍然显式 Chmod：权限是这个文件存在的全部
	// 理由，不该依赖另一个函数当前的默认值。
	// CreateTemp already yields 0600, yet the mode is set explicitly: permissions are the whole
	// point of this file and should not rest on another function's present-day default.
	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置临时文件权限失败 / chmod temp file: %w", err)
	}
	if _, err := tmp.WriteString(v); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入临时文件失败 / write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败 / close temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
		return fmt.Errorf("保存凭证失败 / install credential file: %w", err)
	}
	return nil
}

// Delete 删除一条凭证。
// Delete removes one credential.
func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("删除凭证失败 / remove credential: %w", err)
	}
	return nil
}

// ValidateName 校验凭证名。错误信息要说清楚规则，页面才能直接显示给用户。
// ValidateName checks a credential name. The message must spell out the rule so the UI can
// show it to the user verbatim.
func ValidateName(name string) error {
	if nameRE.MatchString(name) {
		return nil
	}
	return fmt.Errorf(
		"凭证名 %q 不合法：只允许大写字母、数字和下划线，必须以字母开头，最长 64 个字符（例如 TAVILY_API_KEY） / "+
			"invalid credential name %q: only uppercase letters, digits and underscores are allowed, "+
			"it must start with a letter and be at most 64 characters (e.g. TAVILY_API_KEY)",
		name, name)
}
