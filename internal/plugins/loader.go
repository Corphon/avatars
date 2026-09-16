package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"avatars/internal/skills"
)

const defaultMarketplacePath = ".agents/plugins/marketplace.json"

type Marketplace struct {
	Name    string             `json:"name"`
	Plugins []MarketplaceEntry `json:"plugins"`
}

type MarketplaceEntry struct {
	Name     string            `json:"name"`
	Source   MarketplaceSource `json:"source"`
	Policy   MarketplacePolicy `json:"policy"`
	Category string            `json:"category"`
}

type MarketplaceSource struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

type MarketplacePolicy struct {
	Installation string `json:"installation"`
}

type Manifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Skills      string          `json:"skills"`
	Interface   ManifestUIBlock `json:"interface"`
}

type ManifestUIBlock struct {
	DisplayName      string   `json:"displayName"`
	ShortDescription string   `json:"shortDescription"`
	Category         string   `json:"category"`
	Capabilities     []string `json:"capabilities"`
}

type Plugin struct {
	Name        string
	Version     string
	Description string
	Marketplace string
	Category    string
	RootDir     string
	SkillsDir   string
	Enabled     bool
	Skills      []skills.Definition
}

type Summary struct {
	MarketplaceCount int
	PluginCount      int
	SkillCount       int
	Plugins          []Plugin
	Skills           []skills.Definition
	Warnings         []string
}

type LoadResult struct {
	Plugins  []Plugin
	Warnings []string
}

func (r LoadResult) SkillCount() int {
	count := 0
	for _, plugin := range r.Plugins {
		if plugin.Enabled {
			count += len(plugin.Skills)
		}
	}
	return count
}

func (r LoadResult) SkillDefinitions() []skills.Definition {
	definitions := make([]skills.Definition, 0, r.SkillCount())
	for _, plugin := range r.Plugins {
		if !plugin.Enabled {
			continue
		}
		definitions = append(definitions, plugin.Skills...)
	}
	sort.Slice(definitions, func(i int, j int) bool {
		if definitions[i].Name != definitions[j].Name {
			return definitions[i].Name < definitions[j].Name
		}
		return definitions[i].Path < definitions[j].Path
	})
	return definitions
}

func LoadLocalPlugins() (LoadResult, error) {
	return LoadLocalPluginsFromMarketplace(resolveRuntimePath(defaultMarketplacePath))
}

func LoadSummary() (Summary, error) {
	result, err := LoadLocalPlugins()
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{
		PluginCount: len(result.Plugins),
		SkillCount:  result.SkillCount(),
		Plugins:     result.Plugins,
		Skills:      result.SkillDefinitions(),
		Warnings:    result.Warnings,
	}
	marketplaces := map[string]struct{}{}
	for _, plugin := range result.Plugins {
		if strings.TrimSpace(plugin.Marketplace) == "" {
			continue
		}
		marketplaces[plugin.Marketplace] = struct{}{}
	}
	summary.MarketplaceCount = len(marketplaces)
	return summary, nil
}

func LoadLocalPluginsFromMarketplace(path string) (LoadResult, error) {
	resolvedPath := strings.TrimSpace(path)
	if resolvedPath == "" {
		resolvedPath = defaultMarketplacePath
	}
	content, err := os.ReadFile(filepath.Clean(resolvedPath))
	if errors.Is(err, os.ErrNotExist) {
		return LoadResult{}, nil
	}
	if err != nil {
		return LoadResult{}, fmt.Errorf("read plugin marketplace %s: %w", resolvedPath, err)
	}
	var marketplace Marketplace
	if err := json.Unmarshal(content, &marketplace); err != nil {
		return LoadResult{}, fmt.Errorf("decode plugin marketplace %s: %w", resolvedPath, err)
	}
	baseDir := filepath.Dir(resolvedPath)
	result := LoadResult{}
	for _, entry := range marketplace.Plugins {
		plugin, warnings, err := loadMarketplacePlugin(marketplace.Name, baseDir, entry)
		result.Warnings = append(result.Warnings, warnings...)
		if err != nil {
			result.Warnings = append(result.Warnings, err.Error())
			continue
		}
		result.Plugins = append(result.Plugins, plugin)
	}
	sort.Slice(result.Plugins, func(i int, j int) bool {
		if result.Plugins[i].Marketplace != result.Plugins[j].Marketplace {
			return result.Plugins[i].Marketplace < result.Plugins[j].Marketplace
		}
		return result.Plugins[i].Name < result.Plugins[j].Name
	})
	sort.Strings(result.Warnings)
	return result, nil
}

func loadMarketplacePlugin(marketplaceName string, marketplaceDir string, entry MarketplaceEntry) (Plugin, []string, error) {
	if !strings.EqualFold(strings.TrimSpace(entry.Source.Source), "local") {
		return Plugin{}, nil, fmt.Errorf("plugin %s has unsupported source %q", entry.Name, entry.Source.Source)
	}
	rootDir := resolvePluginPath(marketplaceDir, entry.Source.Path)
	manifestPath := filepath.Join(rootDir, ".codex-plugin", "plugin.json")
	content, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		return Plugin{}, nil, fmt.Errorf("read plugin manifest %s: %w", manifestPath, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Plugin{}, nil, fmt.Errorf("decode plugin manifest %s: %w", manifestPath, err)
	}
	name := firstNonEmpty(manifest.Name, entry.Name, filepath.Base(rootDir))
	skillsDir := strings.TrimSpace(manifest.Skills)
	if skillsDir == "" {
		skillsDir = "skills"
	}
	if !filepath.IsAbs(skillsDir) {
		skillsDir = filepath.Join(rootDir, skillsDir)
	}
	definitions, warnings := loadPluginSkills(name, skillsDir)
	return Plugin{
		Name:        name,
		Version:     strings.TrimSpace(manifest.Version),
		Description: firstNonEmpty(manifest.Description, manifest.Interface.ShortDescription),
		Marketplace: strings.TrimSpace(marketplaceName),
		Category:    firstNonEmpty(entry.Category, manifest.Interface.Category),
		RootDir:     rootDir,
		SkillsDir:   filepath.Clean(skillsDir),
		Enabled:     !strings.EqualFold(strings.TrimSpace(entry.Policy.Installation), "DISABLED"),
		Skills:      definitions,
	}, warnings, nil
}

func resolvePluginPath(baseDir string, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return filepath.Clean(baseDir)
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	candidates := []string{
		filepath.Join(baseDir, trimmed),
		resolveRuntimePath(trimmed),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return filepath.Clean(candidate)
		}
	}
	return filepath.Clean(candidates[0])
}

func resolveRuntimePath(relativePath string) string {
	rel := filepath.Clean(strings.TrimSpace(relativePath))
	if rel == "" || rel == "." {
		return resolveRuntimeHome()
	}
	for _, root := range runtimeHomeCandidates() {
		candidate := filepath.Join(root, rel)
		if pathExists(candidate) {
			return candidate
		}
	}
	return rel
}

func resolveRuntimeHome() string {
	for _, root := range runtimeHomeCandidates() {
		if root != "" {
			return root
		}
	}
	return "."
}

func runtimeHomeCandidates() []string {
	seen := map[string]struct{}{}
	candidates := []string{}
	add := func(path string) {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "" {
			return
		}
		key := strings.ToLower(cleaned)
		if _, ok := seen[key]; ok {
			return
		}
		if dirExists(cleaned) {
			seen[key] = struct{}{}
			candidates = append(candidates, cleaned)
		}
	}
	if env := strings.TrimSpace(os.Getenv("AVATARS_HOME")); env != "" {
		add(env)
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, "avatars"))
		add(cwd)
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		add(exeDir)
		add(filepath.Dir(exeDir))
		add(filepath.Dir(filepath.Dir(exeDir)))
	}
	return candidates
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func loadPluginSkills(pluginName string, root string) ([]skills.Definition, []string) {
	warnings := []string{}
	definitions := []skills.Definition{}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return definitions, warnings
	}
	if err != nil {
		return definitions, []string{fmt.Sprintf("read plugin skills dir %s: %v", root, err)}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		definition, err := skills.ReadExternalDefinition(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		definition.Name = pluginSkillName(pluginName, definition.Name)
		definition.Path = filepath.Clean(path)
		definition.LifecycleState = "plugin"
		definition.SourceRunID = ""
		definition.SourceTaskID = ""
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i int, j int) bool {
		if definitions[i].Name != definitions[j].Name {
			return definitions[i].Name < definitions[j].Name
		}
		return definitions[i].Path < definitions[j].Path
	})
	sort.Strings(warnings)
	return definitions, warnings
}

func pluginSkillName(pluginName string, skillName string) string {
	pluginName = strings.TrimSpace(pluginName)
	skillName = strings.TrimSpace(skillName)
	if pluginName == "" {
		return skillName
	}
	if skillName == "" {
		return pluginName
	}
	if strings.Contains(skillName, ":") {
		return skillName
	}
	return pluginName + ":" + skillName
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// Install adds a local plugin directory to the marketplace configuration.
func Install(pluginPath string) error {
	if pluginPath == "" {
		return fmt.Errorf("plugin path is required")
	}
	// Detect remote (git) vs local paths.
	if strings.HasPrefix(pluginPath, "http://") || strings.HasPrefix(pluginPath, "https://") || strings.HasPrefix(pluginPath, "git@") {
		return installRemote(pluginPath)
	}
	absPath, err := filepath.Abs(pluginPath)
	if err != nil {
		return fmt.Errorf("resolve plugin path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("plugin path does not exist or is not a directory: %s", absPath)
	}
	return registerPlugin("local", absPath, filepath.Base(absPath))
}

func installRemote(gitURL string) error {
	pluginsDir := resolveRuntimePath(".avatars/plugins/remote")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return fmt.Errorf("create remote plugins directory: %w", err)
	}
	// Derive name from URL: last path segment minus .git
	name := filepath.Base(strings.TrimSuffix(gitURL, "/"))
	name = strings.TrimSuffix(name, ".git")
	if name == "" || name == "." {
		return fmt.Errorf("cannot derive plugin name from URL: %s", gitURL)
	}
	targetPath := filepath.Join(pluginsDir, name)
	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("plugin %q already exists at %s; remove it first", name, targetPath)
	}
	cmd := exec.Command("git", "clone", gitURL, targetPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w", gitURL, err)
	}
	return registerPlugin("git", targetPath, name)
}

func registerPlugin(sourceType string, absPath string, name string) error {
	marketplacePath := resolveRuntimePath(defaultMarketplacePath)
	if err := os.MkdirAll(filepath.Dir(marketplacePath), 0o755); err != nil {
		return fmt.Errorf("create marketplace directory: %w", err)
	}
	mp := loadMarketplaceFile(marketplacePath)
	for _, entry := range mp.Plugins {
		if strings.EqualFold(entry.Source.Path, absPath) {
			return fmt.Errorf("plugin at %s is already registered", absPath)
		}
	}
	mp.Plugins = append(mp.Plugins, MarketplaceEntry{
		Name:   name,
		Source: MarketplaceSource{Source: sourceType, Path: absPath},
		Policy: MarketplacePolicy{Installation: "AVAILABLE"},
	})
	return saveMarketplaceFile(marketplacePath, mp)
}

// Remove disables a plugin by name in the marketplace configuration.
func Remove(name string) error {
	if name == "" {
		return fmt.Errorf("plugin name is required")
	}
	marketplacePath := resolveRuntimePath(defaultMarketplacePath)
	mp := loadMarketplaceFile(marketplacePath)
	found := false
	for i, entry := range mp.Plugins {
		if strings.EqualFold(entry.Name, name) {
			mp.Plugins[i].Policy.Installation = "DISABLED"
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("plugin %q not found in marketplace", name)
	}
	return saveMarketplaceFile(marketplacePath, mp)
}

func loadMarketplaceFile(path string) Marketplace {
	data, err := os.ReadFile(path)
	if err != nil {
		return Marketplace{}
	}
	var mp Marketplace
	_ = json.Unmarshal(data, &mp)
	return mp
}

func saveMarketplaceFile(path string, mp Marketplace) error {
	data, err := json.MarshalIndent(mp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal marketplace: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
