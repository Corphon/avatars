package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/events"
	"avatars/internal/llm"
	"avatars/internal/mcp"
	memstore "avatars/internal/memory"
	"avatars/internal/plugins"
	"avatars/internal/registry"
	"avatars/internal/runtime"
	"avatars/internal/skills"
	"avatars/internal/tasks"
	"avatars/internal/tools"
	"avatars/internal/transcript"
	"avatars/internal/verification"
	"avatars/internal/workflow"
)

type Application struct {
	Engine           *runtime.Engine
	Events           *events.Store
	LLM              llm.Client
	LLMStatus        llm.ProviderStatus
	LLMStatusWarning error
	Skills           *skills.Store
	Tools            *registry.ToolRegistry
}

type BootstrapOptions struct {
	Workspace             *tasks.Workspace
	AllowInvalidLLMConfig bool
	PermissionMode        runtime.PermissionMode
}

func Bootstrap() (*Application, error) {
	return BootstrapWithOptions(BootstrapOptions{})
}

func BootstrapWithOptions(options BootstrapOptions) (*Application, error) {
	toolRegistry := registry.NewToolRegistry()
	if err := toolRegistry.Register(tools.ReadTool{}); err != nil {
		return nil, err
	}
	if err := toolRegistry.Register(tools.ShellTool{}); err != nil {
		return nil, err
	}
	if err := toolRegistry.Register(tools.WriteTool{}); err != nil {
		return nil, err
	}
	if err := toolRegistry.Register(tools.PatchTool{}); err != nil {
		return nil, err
	}
	if err := toolRegistry.Register(tools.PreciseEditTool{}); err != nil {
		return nil, err
	}
	if err := toolRegistry.Register(tools.GitTool{}); err != nil {
		return nil, err
	}

	sessionID := time.Now().UTC().Format("20060102-150405.000000000")
	writerBaseDir := filepath.Join(".avatars", "sessions")
	memoryBaseDir := filepath.Join(".avatars", "memory")
	workspaceTaskID := ""
	workspaceRootDir := ""
	if options.Workspace != nil {
		writerBaseDir = options.Workspace.SessionsDir
		memoryBaseDir = options.Workspace.MemoryDir
		workspaceTaskID = options.Workspace.ID
		workspaceRootDir = options.Workspace.RootDir
	}
	writer, err := transcript.NewJSONLWriter(writerBaseDir, sessionID)
	if err != nil {
		return nil, err
	}
	bootstrapped := false
	defer func() {
		if !bootstrapped {
			_ = writer.Close()
		}
	}()

	store := events.NewStore()
	llmConfig, err := loadLLMConfig(ResolveRuntimePath(defaultAgentConfigPath))
	llmStatusWarning := error(nil)
	if err != nil {
		if !options.AllowInvalidLLMConfig {
			return nil, err
		}
		llmStatusWarning = err
		llmConfig = llm.DefaultConfig()
	}
	llmStatus := llm.ResolveProviderStatus(llmConfig)
	client, err := llm.NewClient(llmConfig)
	if err != nil {
		return nil, err
	}
	mcpClient := mcp.NewClient(mcp.Config{Timeout: 10 * time.Second}, nil)
	skillStore := skills.NewStore(filepath.Join(ResolveRuntimeHome(), "skills"))
	if healCount, healErr := skillStore.SelfHealBuiltinSkills(); healErr != nil {
		return nil, fmt.Errorf("self-heal builtin skills: %w", healErr)
	} else if healCount > 0 {
		healWarning := fmt.Sprintf("self-healed %d builtin skills into empty approved directory", healCount)
		if llmStatusWarning != nil {
			llmStatusWarning = fmt.Errorf("%s; %s", llmStatusWarning.Error(), healWarning)
		} else {
			llmStatusWarning = fmt.Errorf("%s", healWarning)
		}
	}

	// Workflow preload: ensure /docs/avatars_plan.md, /docs/avatars_todo.md,
	// and /docs/process_record.md exist in the project root. Missing files
	// are created from hardcoded English templates embedded in the binary.
	projectRoot, _ := os.Getwd()
	if created, preloadErr := workflow.PreloadDocs(projectRoot); preloadErr != nil {
		// Preload failure is non-fatal: log and continue.
		preloadWarning := fmt.Sprintf("workflow preload: %v", preloadErr)
		if llmStatusWarning != nil {
			llmStatusWarning = fmt.Errorf("%s; %s", llmStatusWarning.Error(), preloadWarning)
		} else {
			llmStatusWarning = fmt.Errorf("%s", preloadWarning)
		}
	} else if created > 0 {
		// Inform the engine so the LLM knows docs were initialized.
		_ = created // Docs created; the LLM will discover them via preload reads.
	}
	runtimeSkillStore := runtime.SkillStore(skillStore)
	if pluginResult, pluginErr := plugins.LoadLocalPlugins(); pluginErr == nil {
		pluginDefinitions := pluginResult.SkillDefinitions()
		if len(pluginDefinitions) > 0 {
			runtimeSkillStore = skills.NewCompositeStore(skillStore, pluginDefinitions)
		}
	}

	// S3.1: features.memory gates SQLite create / engine wiring / workflow injection.
	memoryEnabled := LoadFeaturesMemory(ResolveRuntimePath(defaultAgentConfigPath))
	var memoryStore *memstore.Store
	var projectMemoryStore *memstore.Store
	if memoryEnabled {
		var err error
		memoryStore, err = memstore.NewSQLiteStore(memoryBaseDir)
		if err != nil {
			return nil, err
		}
		projectMemoryStore = memoryStore
		if options.Workspace != nil {
			projectMemoryStore, err = memstore.NewSQLiteStore(filepath.Join(".avatars", "memory"))
			if err != nil {
				_ = memoryStore.Close()
				return nil, err
			}
		}
		// P0-2: SQLite as single source of truth for workflow memory.
		workflow.SetMemoryStore(projectMemoryStore)
	}

	engine := runtime.NewEngine(toolRegistry, writer, store, sessionID, verification.DefaultPolicy(), client, mcpClient, runtimeSkillStore)
	if servers, err := LoadMCPServerConfigs(""); err == nil && len(servers) > 0 {
		specs := make([]mcp.ServerSpec, 0, len(servers))
		for _, server := range servers {
			specs = append(specs, server.Spec())
		}
		engine = engine.WithMCPServers(specs)
	}
	if memoryStore != nil {
		engine = engine.WithMemory(memoryStore).WithProjectMemory(projectMemoryStore)
	}
	engine = engine.WithProjectRoot(projectRoot)
	engine = engine.WithPermissionMode(options.PermissionMode)
	engine = engine.WithLLMStatus(llmStatus, errorString(llmStatusWarning))
	engine = applyEngineOperatorSettings(engine, ResolveRuntimePath(defaultAgentConfigPath))
	// L1: Load intent keywords from YAML as single source of truth for NL classification.
	LoadIntentKeywords()
	if workspaceTaskID != "" {
		engine = engine.WithTaskWorkspace(workspaceTaskID, workspaceRootDir)
	}
	if parsed, err := loadAgentConfigFile(ResolveRuntimePath(defaultAgentConfigPath)); err == nil {
		modelRouting := make(map[string]string, len(parsed.ModelRouting))
		for role, model := range parsed.ModelRouting {
			if strings.TrimSpace(model) != "" {
				modelRouting[role] = strings.TrimSpace(model)
			}
		}
		if len(modelRouting) > 0 {
			engine = engine.WithModelRouting(modelRouting)
		}
		// S2.8: wire workflow.auto_confirm (default false when key absent).
		engine = engine.WithWorkflowAutoConfirm(parsed.Workflow.AutoConfirm)
	}
	application := &Application{Engine: engine, Events: store, LLM: client, LLMStatus: llmStatus, LLMStatusWarning: llmStatusWarning, Skills: skillStore, Tools: toolRegistry}
	bootstrapped = true
	return application, nil
}

// applyEngineOperatorSettings injects personality unconditionally. Auto-approve
// of generated skills stays gated on skills.auto_approve_generated (B1).
func applyEngineOperatorSettings(engine *runtime.Engine, configPath string) *runtime.Engine {
	if engine == nil {
		return nil
	}
	if LoadSkillsAutoApprove(configPath) {
		engine = engine.WithAutoApproveSkills(true)
	}
	engine = engine.WithPersonality(LoadPersonalityConfig())
	return engine
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *Application) Close() error {
	if a == nil || a.Engine == nil {
		return nil
	}
	return a.Engine.Close()
}

// NewLLMClient creates a lightweight LLM client directly from the agent
// config file, without spinning up a full Application (no SQLite, no
// transcript writer, no skill store, no engine). Use this for one-shot
// LLM queries (complexity assessment, intent routing, script generation)
// that don't need the full runtime infrastructure.
//
// The returned client is standalone — there is no cleanup callback
// because no persistent resources are created.
func NewLLMClient() (llm.Client, error) {
	config, err := loadLLMConfig(ResolveRuntimePath(defaultAgentConfigPath))
	if err != nil {
		return nil, err
	}
	return llm.NewClient(config)
}
