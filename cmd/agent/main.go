// Command agent 启动通用 Agent 的本地服务：HTTP 接口 + 内嵌 Web 控制台。
//
// 它只监听回环地址，且不提供监听 0.0.0.0 的选项。这不是保守，是因为整个服务没有
// 鉴权层：一旦对外可达，任何人都能调用 execute 工具在这台机器上执行命令。
// 需要远程访问时，正确做法是在外面套一层 SSH 隧道或反向代理来承担鉴权。
//
// Command agent runs the general agent's local service: the HTTP API plus the embedded web
// console.
//
// It binds to loopback only and offers no option to listen on 0.0.0.0. That is not
// conservatism but a consequence of the service having no auth layer: once reachable from
// outside, anyone could invoke the execute tool and run commands on this machine. Remote access
// should be arranged by wrapping it in an SSH tunnel or a reverse proxy that handles
// authentication.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"private/agent_basedon_eino/internal/agent/config"
	"private/agent_basedon_eino/internal/agent/httpapi"
	"private/agent_basedon_eino/internal/agent/kernel"
	"private/agent_basedon_eino/internal/agent/memory"
	"private/agent_basedon_eino/internal/agent/rag"
	"private/agent_basedon_eino/internal/agent/schedule"
	"private/agent_basedon_eino/internal/agent/session"
	"private/agent_basedon_eino/internal/agent/skills"
	"private/agent_basedon_eino/internal/agent/store"
	"private/agent_basedon_eino/internal/agent/tools"
	"private/agent_basedon_eino/internal/agent/tools/cli"
	"private/agent_basedon_eino/internal/agent/tools/mcp"
	"private/agent_basedon_eino/web"
)

const (
	defaultAddr     = "127.0.0.1:8090"
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败 / startup failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr      = flag.String("addr", defaultAddr, "监听地址，仅限回环 / listen address, loopback only")
		dbPath    = flag.String("db", "agent.db", "SQLite 数据库路径 / SQLite database path")
		ragDir    = flag.String("rag-dir", "documents", "RAG 文档目录 / RAG document directory")
		skillsDir = flag.String("skills-dir", "skills", "技能目录 / skills directory")
		workDir   = flag.String("work-dir", "workspace", "文件工具工作目录 / working directory for file tools")
		verbose   = flag.Bool("verbose", false, "输出调试日志 / emit debug logs")
	)
	flag.Parse()

	if err := ensureLoopback(*addr); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// 密钥检查放在最前面。缺密钥时立刻失败，而不是让用户在页面上敲完一句话
	// 才收到一个 401——那时他还得猜是网络问题还是配置问题。
	// The key check comes first. Missing it fails immediately, rather than letting the user type
	// a full message in the UI only to receive a 401 and be left guessing whether the problem is
	// the network or the configuration.
	if _, err := config.APIKey(); err != nil {
		return err
	}

	ctx := context.Background()

	db, err := store.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := store.Migrate(ctx, db); err != nil {
		return err
	}

	startup := config.Startup{
		Addr:       *addr,
		DBPath:     db.Path(),
		RAGDir:     mustAbs(*ragDir),
		SkillsDir:  mustAbs(*skillsDir),
		WorkingDir: mustAbs(*workDir),
	}
	cfgMgr, err := config.NewManager(ctx, db, startup)
	if err != nil {
		return err
	}

	workspace, err := cli.NewWorkspace(startup.WorkingDir)
	if err != nil {
		return err
	}
	rt := cfgMgr.Current()
	cliStore := cli.NewStore(db)
	runner := cli.NewRunner(workspace)
	shell := cli.NewShell(runner, cliStore, rt.MaxToolResultBytes)

	mcpMgr := mcp.NewManager(db, logger)
	defer mcpMgr.Close()
	registry := tools.NewRegistry(mcpMgr, cliStore, runner, logger)

	skillStore := skills.NewStore(db, startup.SkillsDir)
	skillBackend := skills.NewBackend(skillStore)

	ragMgr, ragRetriever, err := buildRAG(ctx, db, startup.RAGDir, rt.EmbeddingModel, logger)
	if err != nil {
		return err
	}

	memStore := memory.NewStore(db)

	// 调度器先于 Engine 构造，因为 schedule_task 工具要挂到每一轮对话上；
	// 它的执行器反过来依赖 Engine，所以在 Engine 建好后再补上。
	// The scheduler is built before the Engine because the schedule_task tool must be mounted on
	// every turn; its executor depends on the Engine in turn and is filled in afterwards.
	taskStore := schedule.NewStore(db)
	scheduler := schedule.NewScheduler(taskStore, nil, logger)

	sessions := session.NewStore(db)
	titleModel, err := kernel.ModelFactory{}.NewChatModel(ctx, rt.TitleModelName)
	if err != nil {
		return err
	}
	titles := session.NewTitleGenerator(sessions, titleModel, logger)

	engine, err := kernel.NewEngine(kernel.EngineConfig{
		Sessions:   sessions,
		Config:     cfgMgr,
		CheckPoint: store.NewCheckPointStore(db),
		Files:      workspace,
		Shell:      shell,
		WorkDir:    startup.WorkingDir,
		Augmenters: []kernel.Augmenter{
			registry.Augmenter(tools.Scope{}),
			skillBackend.Augmenter(),
			ragRetriever.Augmenter(),
			memory.NewInjector(memStore).Augmenter(),
			schedule.Augmenter(taskStore, scheduler),
		},
	})
	if err != nil {
		return err
	}

	scheduler.SetExecutor(schedule.NewRunner(engine, sessions, registry))
	if err := scheduler.Start(ctx); err != nil {
		return err
	}
	defer scheduler.Stop()

	srv := httpapi.NewServer(httpapi.Deps{
		Logger:    logger,
		Sessions:  sessions,
		Titles:    titles,
		Engine:    engine,
		Config:    cfgMgr,
		Tools:     registry,
		MCP:       mcpMgr,
		CLI:       cliStore,
		Skills:    skillStore,
		RAG:       ragMgr,
		Memory:    memStore,
		Tasks:     taskStore,
		Scheduler: scheduler,
	})
	if dist, err := web.Dist(); err == nil {
		srv.MountStatic(dist)
	} else {
		logger.Warn("前端资源不可用 / frontend assets unavailable", "err", err)
	}

	return serve(ctx, srv, *addr, logger)
}

// serve 启动 HTTP 服务并在收到信号时优雅退出。
// serve starts the HTTP server and shuts down gracefully on a signal.
func serve(ctx context.Context, handler http.Handler, addr string, logger *slog.Logger) error {
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// 不设 WriteTimeout：对话接口是 SSE 长连接，一个几分钟的写超时会把
		// 正在生成的回复从中间掐断。读超时照设。
		// No WriteTimeout: the chat endpoint is a long-lived SSE connection, and a write timeout
		// of a few minutes would cut a reply off mid-generation. The read timeout stays.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("服务已启动 / server started", "addr", "http://"+addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-sigCh:
		logger.Info("正在关闭 / shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// ensureLoopback 拒绝非回环监听地址。
// ensureLoopback rejects non-loopback listen addresses.
func ensureLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("监听地址格式错误 / malformed listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf(
			"监听地址 %q 不是回环地址。本服务没有鉴权层，对外暴露等于把本机命令执行权交给任何人；"+
				"需要远程访问请用 SSH 隧道 / listen address %q is not a loopback address. This service has no "+
				"auth layer, so exposing it hands local command execution to anyone; use an SSH tunnel for "+
				"remote access", addr, addr)
	}
	return nil
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// buildRAG 组装文档加载、切分、嵌入、索引与检索这一整条链路。
// buildRAG wires the document loading, splitting, embedding, indexing and retrieval chain.
func buildRAG(
	ctx context.Context, db *store.DB, dir, embedModel string, logger *slog.Logger,
) (*rag.Manager, *rag.Retriever, error) {
	loader, err := rag.NewLoader(ctx)
	if err != nil {
		return nil, nil, err
	}
	splitter, err := rag.NewSplitter(ctx)
	if err != nil {
		return nil, nil, err
	}
	embedder, err := rag.NewEmbedder(ctx, embedModel)
	if err != nil {
		return nil, nil, err
	}
	indexer := rag.NewIndexer(db, loader, splitter, embedder)
	return rag.NewManager(indexer, dir, logger), rag.NewRetriever(db, embedder), nil
}
