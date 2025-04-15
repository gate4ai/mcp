package config

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema" // Import A2A schema
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var _ IConfig = (*YamlConfig)(nil)

// YamlConfig implements all configuration interfaces with YAML file-based storage
type YamlConfig struct {
	mu                          sync.RWMutex
	configPath                  string
	logger                      *zap.Logger
	serverAddress               string
	serverName                  string
	serverVersion               string
	logLevel                    string
	DiscoveringHandlerPathValue string
	frontendAddressValue        string
	authorizationType           AuthorizationType
	userKeyHashes               map[string]string            // keyHash -> userID (generated on load)
	userParams                  map[string]map[string]string // userID -> paramName -> paramValue (from yaml)
	userSubscribes              map[string][]string          // userID -> serverSlugs (from yaml)
	backends                    map[string]*Backend          // serverSlug -> Server (from yaml)

	// SSL Fields
	sslEnabled      bool
	sslMode         string
	sslCertFile     string
	sslKeyFile      string
	sslAcmeDomains  []string
	sslAcmeEmail    string
	sslAcmeCacheDir string

	// A2A Fields (read from config)
	a2aAgentNameValue          string
	a2aAgentDescriptionValue   *string
	a2aProviderOrgValue        *string
	a2aProviderURLValue        *string
	a2aAgentVersionValue       string
	a2aDocumentationURLValue   *string
	a2aDefaultInputModesValue  []string
	a2aDefaultOutputModesValue []string
	a2aAuthenticationValue     *a2aSchema.AgentAuthentication // Raw YAML value
}

// YAML configuration structure matching the required format
type yamlConfig struct {
	Server struct {
		Address                string `yaml:"address"`
		Name                   string `yaml:"name"`
		Version                string `yaml:"version"`
		LogLevel               string `yaml:"log_level"`
		DiscoveringHandlerPath string `yaml:"info_handler"`
		FrontendAddress        string `yaml:"frontend_address"`
		Authorization          string `yaml:"authorization"` // "users_only", "marked_methods", or "none"
		SSL                    struct {
			Enabled      bool     `yaml:"enabled"`
			Mode         string   `yaml:"mode"`
			CertFile     string   `yaml:"cert_file"`
			KeyFile      string   `yaml:"key_file"`
			AcmeDomains  []string `yaml:"acme_domains"`
			AcmeEmail    string   `yaml:"acme_email"`
			AcmeCacheDir string   `yaml:"acme_cache_dir"`
		} `yaml:"ssl"`
		A2A *struct { // Optional A2A section
			Name               string                         `yaml:"agent_name"`
			Description        *string                        `yaml:"agent_description"`
			Version            string                         `yaml:"agent_version"`
			DocumentationURL   *string                        `yaml:"agent_documentation_url"`
			ProviderOrg        *string                        `yaml:"agent_provider_organization"`
			ProviderURL        *string                        `yaml:"agent_provider_url"`
			DefaultInputModes  []string                       `yaml:"default_input_modes"`
			DefaultOutputModes []string                       `yaml:"default_output_modes"`
			Authentication     *a2aSchema.AgentAuthentication `yaml:"agent_authentication"` // Directly unmarshal A2A auth struct
		} `yaml:"a2a"` // Use pointer to make the section optional
	} `yaml:"server"`

	Users map[string]struct {
		Keys       []string `yaml:"keys"` // Store hashes directly
		Subscribes []string `yaml:"subscribes"`
	} `yaml:"users"`

	Backends map[string]struct {
		URL    string `yaml:"url"`
		Bearer string `yal:"bearer"`
	} `yaml:"backends"`
}

// NewYamlConfig creates a new YAML-based configuration
func NewYamlConfig(configPath string, logger *zap.Logger) (*YamlConfig, error) {
	return NewYamlConfigWithOptions(configPath, logger)
}

// NewYamlConfigWithOptions creates a new YAML-based configuration with specified options
func NewYamlConfigWithOptions(configPath string, logger *zap.Logger) (*YamlConfig, error) {
	if logger == nil {
		logger, _ = zap.NewProduction()
	}

	config := &YamlConfig{
		configPath:        configPath,
		logger:            logger,
		userKeyHashes:     make(map[string]string),
		userParams:        make(map[string]map[string]string), // Params not directly in YAML, kept empty for now
		userSubscribes:    make(map[string][]string),
		backends:          make(map[string]*Backend),
		authorizationType: AuthorizedUsersOnly, // Default
		sslMode:           "manual",
		sslAcmeCacheDir:   "./.autocert-cache",
	}

	if err := config.Update(); err != nil {
		return nil, err
	}
	return config, nil
}

// Update reloads configuration from the YAML file
func (c *YamlConfig) Update() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Debug("Updating configuration from YAML file", zap.String("path", c.configPath))

	data, err := os.ReadFile(c.configPath)
	if err != nil {
		c.logger.Error("Failed to read config file", zap.Error(err))
		return err
	}

	var yamlCfg yamlConfig
	if err := yaml.Unmarshal(data, &yamlCfg); err != nil {
		c.logger.Error("Failed to parse YAML", zap.Error(err))
		return err
	}

	// --- Process Server Section ---
	c.serverAddress = yamlCfg.Server.Address
	c.serverName = yamlCfg.Server.Name
	c.serverVersion = yamlCfg.Server.Version
	c.logLevel = yamlCfg.Server.LogLevel
	c.DiscoveringHandlerPathValue = yamlCfg.Server.DiscoveringHandlerPath
	c.frontendAddressValue = yamlCfg.Server.FrontendAddress
	switch strings.ToLower(yamlCfg.Server.Authorization) {
	case "users_only":
		c.authorizationType = AuthorizedUsersOnly
	case "marked_methods":
		c.authorizationType = NotAuthorizedToMarkedMethods
	case "none":
		c.authorizationType = NotAuthorizedEverywhere
	default:
		c.authorizationType = AuthorizedUsersOnly
	}

	// --- Process SSL Section ---
	c.sslEnabled = yamlCfg.Server.SSL.Enabled
	c.sslMode = strings.ToLower(yamlCfg.Server.SSL.Mode)
	if c.sslMode != "acme" {
		c.sslMode = "manual"
	}
	c.sslCertFile = yamlCfg.Server.SSL.CertFile
	c.sslKeyFile = yamlCfg.Server.SSL.KeyFile
	c.sslAcmeDomains = yamlCfg.Server.SSL.AcmeDomains
	c.sslAcmeEmail = yamlCfg.Server.SSL.AcmeEmail
	c.sslAcmeCacheDir = yamlCfg.Server.SSL.AcmeCacheDir
	if c.sslAcmeCacheDir == "" {
		c.sslAcmeCacheDir = "./.autocert-cache"
	}

	// --- Process A2A Section (if present) ---
	if yamlCfg.Server.A2A != nil {
		a2aCfg := yamlCfg.Server.A2A
		c.a2aAgentNameValue = a2aCfg.Name
		c.a2aAgentDescriptionValue = a2aCfg.Description
		c.a2aAgentVersionValue = a2aCfg.Version
		c.a2aDocumentationURLValue = a2aCfg.DocumentationURL
		c.a2aProviderOrgValue = a2aCfg.ProviderOrg
		c.a2aProviderURLValue = a2aCfg.ProviderURL
		c.a2aDefaultInputModesValue = a2aCfg.DefaultInputModes
		c.a2aDefaultOutputModesValue = a2aCfg.DefaultOutputModes
		c.a2aAuthenticationValue = a2aCfg.Authentication
	} else {
		// Reset A2A fields if section is removed from config
		c.a2aAgentNameValue = ""
		c.a2aAgentDescriptionValue = nil
		c.a2aAgentVersionValue = ""
		c.a2aDocumentationURLValue = nil
		c.a2aProviderOrgValue = nil
		c.a2aProviderURLValue = nil
		c.a2aDefaultInputModesValue = nil
		c.a2aDefaultOutputModesValue = nil
		c.a2aAuthenticationValue = nil
	}

	// --- Process Users Section ---
	newUserKeyHashes := make(map[string]string)
	newUserSubscribes := make(map[string][]string)
	for userID, user := range yamlCfg.Users {
		for _, keyHash := range user.Keys { // Assume keys in YAML are already hashes
			newUserKeyHashes[keyHash] = userID
		}
		if len(user.Subscribes) > 0 {
			newUserSubscribes[userID] = append([]string{}, user.Subscribes...) // Copy slice
		}
	}
	c.userKeyHashes = newUserKeyHashes
	c.userSubscribes = newUserSubscribes
	// Note: User Params are not directly managed in YAML in this structure

	// --- Process Backends Section ---
	newBackends := make(map[string]*Backend)
	for backendID, backend := range yamlCfg.Backends {
		newBackends[backendID] = &Backend{URL: backend.URL, Bearer: backend.Bearer}
	}
	c.backends = newBackends

	return nil
}

// --- IConfig Implementation (Rest of methods) ---

func (c *YamlConfig) Close() error { return nil }
func (c *YamlConfig) ListenAddr() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverAddress, nil
}
func (c *YamlConfig) AuthorizationType() (AuthorizationType, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authorizationType, nil
}
func (c *YamlConfig) ServerName() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverName, nil
}
func (c *YamlConfig) ServerVersion() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverVersion, nil
}
func (c *YamlConfig) LogLevel() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.logLevel, nil
}
func (c *YamlConfig) DiscoveringHandlerPath() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.DiscoveringHandlerPathValue, nil
}
func (c *YamlConfig) FrontendAddressForProxy() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.frontendAddressValue, nil
}

func (c *YamlConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if keyHash == "" {
		return "", nil
	}
	userID, exists := c.userKeyHashes[keyHash]
	if !exists {
		return "", ErrNotFound
	} // Use ErrNotFound
	return userID, nil
}

func (c *YamlConfig) GetUserParams(userID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	params, exists := c.userParams[userID]
	if !exists {
		return make(map[string]string), nil
	}
	paramsCopy := make(map[string]string, len(params))
	copyMap(params, paramsCopy)
	return paramsCopy, nil
}

func (c *YamlConfig) GetUserSubscribes(userID string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	servers, exists := c.userSubscribes[userID]
	if !exists {
		return []string{}, nil
	}
	serversCopy := make([]string, len(servers))
	copy(serversCopy, servers)
	return serversCopy, nil
}

func (c *YamlConfig) GetBackendBySlug(backendID string) (*Backend, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	backend, exists := c.backends[backendID]
	if !exists {
		return nil, ErrNotFound
	}
	backendCopy := *backend // Return a copy
	return &backendCopy, nil
}

func (c *YamlConfig) Status(ctx context.Context) error {
	// Check if config file exists and is readable
	if _, err := os.Stat(c.configPath); err != nil {
		c.logger.Error("YAML config file status check failed", zap.String("path", c.configPath), zap.Error(err))
		return fmt.Errorf("config file error: %w", err)
	}
	return nil // Basic check passed
}

// --- SSL Methods ---
func (c *YamlConfig) SSLEnabled() (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslEnabled, nil
}
func (c *YamlConfig) SSLMode() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslMode, nil
}
func (c *YamlConfig) SSLCertFile() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslCertFile, nil
}
func (c *YamlConfig) SSLKeyFile() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslKeyFile, nil
}
func (c *YamlConfig) SSLAcmeDomains() ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	domainsCopy := make([]string, len(c.sslAcmeDomains))
	copy(domainsCopy, c.sslAcmeDomains)
	return domainsCopy, nil
}
func (c *YamlConfig) SSLAcmeEmail() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslAcmeEmail, nil
}
func (c *YamlConfig) SSLAcmeCacheDir() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslAcmeCacheDir, nil
}

// --- A2A Method ---
func (c *YamlConfig) GetA2ACardBaseInfo(agentURL string) (A2ACardBaseInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	info := A2ACardBaseInfo{
		Name:               c.a2aAgentNameValue,
		Description:        c.a2aAgentDescriptionValue, // Already pointer
		AgentURL:           agentURL,
		Version:            c.a2aAgentVersionValue,
		DocumentationURL:   c.a2aDocumentationURLValue, // Already pointer
		DefaultInputModes:  make([]string, len(c.a2aDefaultInputModesValue)),
		DefaultOutputModes: make([]string, len(c.a2aDefaultOutputModesValue)),
		Authentication:     c.a2aAuthenticationValue, // Already pointer
	}
	copy(info.DefaultInputModes, c.a2aDefaultInputModesValue)
	copy(info.DefaultOutputModes, c.a2aDefaultOutputModesValue)

	if c.a2aProviderOrgValue != nil || c.a2aProviderURLValue != nil {
		info.Provider = &a2aSchema.AgentProvider{
			Organization: derefString(c.a2aProviderOrgValue),
			URL:          c.a2aProviderURLValue, // Already pointer
		}
	}

	// Set defaults if values were empty/missing in config
	if info.Name == "" {
		info.Name = "Gate4AI A2A Agent"
	}
	if info.Version == "" {
		info.Version = "1.0.0"
	}
	if len(info.DefaultInputModes) == 0 {
		info.DefaultInputModes = []string{"text"}
	}
	if len(info.DefaultOutputModes) == 0 {
		info.DefaultOutputModes = []string{"text"}
	}

	return info, nil
}
