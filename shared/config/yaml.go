package config

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var _ IConfig = (*YamlConfig)(nil)

// YamlConfig implements all configuration interfaces with YAML file-based storage
type YamlConfig struct {
	mu         sync.RWMutex
	configPath string
	logger     *zap.Logger

	// Parsed configuration
	serverAddress        string
	serverName           string
	serverVersion        string
	logLevel             string
	infoHandlerValue     string
	frontendAddressValue string
	authorizationType    AuthorizationType
	userAuthKeys         map[string]string            // authKey -> userID
	userParams           map[string]map[string]string // userID -> paramName -> paramValue
	userSubscribes       map[string][]string          // userID -> serverIDs
	backends             map[string]*Backend          // serverID -> Server

	// File watcher for hot reloading
	watcher      *fsnotify.Watcher
	watcherDone  chan struct{}
	lastModified time.Time
	watcherMu    sync.Mutex
	isWatching   bool
}

// YAML configuration structure matching the required format
type yamlConfig struct {
	Server struct {
		Address         string `yaml:"address"`
		Name            string `yaml:"name"`
		Version         string `yaml:"version"`
		LogLevel        string `yaml:"log_level"`
		InfoHandler     string `yaml:"info_handler"`
		FrontendAddress string `yaml:"frontend_address"`
		Authorization   string `yaml:"authorization"` // Can be "users_only", "marked_methods", or "none"
	} `yaml:"server"`

	Users map[string]struct {
		Keys       []string `yaml:"keys"`
		Subscribes []string `yaml:"subscribes"`
	} `yaml:"users"`

	Backends map[string]struct {
		URL    string `yaml:"url"`
		Bearer string `yaml:"bearer"`
	} `yaml:"backends"`
}

// YamlConfigOptions contains options for creating a new YamlConfig
type YamlConfigOptions struct {
	// If true, the configuration will watch for file changes and reload automatically
	EnableHotReload bool
	// Minimum interval between reloads to prevent excessive reloads (default: 1 second)
	HotReloadMinInterval time.Duration
}

// DefaultYamlConfigOptions returns default options for YamlConfig
func DefaultYamlConfigOptions() YamlConfigOptions {
	return YamlConfigOptions{
		EnableHotReload:      false,
		HotReloadMinInterval: 1 * time.Second,
	}
}

// NewYamlConfig creates a new YAML-based configuration
func NewYamlConfig(configPath string, logger *zap.Logger) (*YamlConfig, error) {
	return NewYamlConfigWithOptions(configPath, logger, DefaultYamlConfigOptions())
}

// NewYamlConfigWithOptions creates a new YAML-based configuration with specified options
func NewYamlConfigWithOptions(configPath string, logger *zap.Logger, options YamlConfigOptions) (*YamlConfig, error) {
	if logger == nil {
		// Create a default logger if none provided
		var err error
		logger, err = zap.NewProduction()
		if err != nil {
			return nil, err
		}
	}

	config := &YamlConfig{
		configPath:        configPath,
		logger:            logger,
		userAuthKeys:      make(map[string]string),
		userParams:        make(map[string]map[string]string),
		userSubscribes:    make(map[string][]string),
		backends:          make(map[string]*Backend),
		authorizationType: AuthorizedUsersOnly, // Default to requiring authorization
	}

	// Load initial configuration
	if err := config.Update(); err != nil {
		return nil, err
	}

	// Set up file watcher for hot reloading if enabled
	if options.EnableHotReload {
		if err := config.StartWatcher(options.HotReloadMinInterval); err != nil {
			logger.Error("Failed to start file watcher", zap.Error(err))
			return config, err
		}
	}

	return config, nil
}

// StartWatcher begins watching the configuration file for changes
func (c *YamlConfig) StartWatcher(minInterval time.Duration) error {
	c.watcherMu.Lock()
	defer c.watcherMu.Unlock()

	if c.isWatching {
		return nil // Already watching
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	c.watcher = watcher
	c.watcherDone = make(chan struct{})
	c.isWatching = true

	// Get initial file info for modification time
	fileInfo, err := os.Stat(c.configPath)
	if err == nil {
		c.lastModified = fileInfo.ModTime()
	}

	// Start the watching goroutine
	go func() {
		defer watcher.Close()

		// Add the config file to the watcher
		if err := watcher.Add(c.configPath); err != nil {
			c.logger.Error("Failed to add file to watcher",
				zap.String("path", c.configPath),
				zap.Error(err))
			return
		}
		c.logger.Info("Started watching configuration file for changes",
			zap.String("path", c.configPath))

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// Check if this is a modification event
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					// Check if enough time has passed since the last reload
					currentTime := time.Now()
					c.watcherMu.Lock()
					timeSinceLastModification := currentTime.Sub(c.lastModified)
					shouldReload := timeSinceLastModification > minInterval
					if shouldReload {
						c.lastModified = currentTime
					}
					c.watcherMu.Unlock()

					if shouldReload {
						c.logger.Info("Configuration file modified, reloading",
							zap.String("path", c.configPath))
						if err := c.Update(); err != nil {
							c.logger.Error("Failed to reload configuration",
								zap.String("path", c.configPath),
								zap.Error(err))
						}
					}
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				c.logger.Error("File watcher error", zap.Error(err))

			case <-c.watcherDone:
				c.logger.Info("Stopping file watcher")
				return
			}
		}
	}()

	return nil
}

// StopWatcher stops watching the configuration file
func (c *YamlConfig) StopWatcher() {
	c.watcherMu.Lock()
	defer c.watcherMu.Unlock()

	if !c.isWatching {
		return
	}

	close(c.watcherDone)
	c.isWatching = false
}

// IsWatching returns true if the configuration file is being watched
func (c *YamlConfig) IsWatching() bool {
	c.watcherMu.Lock()
	defer c.watcherMu.Unlock()
	return c.isWatching
}

// Update reloads configuration from the YAML file
func (c *YamlConfig) Update() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info("Updating configuration from YAML file", zap.String("path", c.configPath))

	// Read and parse the configuration file
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		c.logger.Error("Failed to read configuration file", zap.String("path", c.configPath), zap.Error(err))
		return err
	}

	var yamlCfg yamlConfig
	if err := yaml.Unmarshal(data, &yamlCfg); err != nil {
		c.logger.Error("Failed to parse YAML configuration", zap.Error(err))
		return err
	}

	// Process server configuration
	c.serverAddress = yamlCfg.Server.Address
	c.serverName = yamlCfg.Server.Name
	c.serverVersion = yamlCfg.Server.Version
	c.logLevel = yamlCfg.Server.LogLevel
	c.infoHandlerValue = yamlCfg.Server.InfoHandler
	c.frontendAddressValue = yamlCfg.Server.FrontendAddress

	// Process authorization type
	switch yamlCfg.Server.Authorization {
	case "users_only":
		c.authorizationType = AuthorizedUsersOnly
	case "marked_methods":
		c.authorizationType = NotAuthorizedToMarkedMethods
	case "none":
		c.authorizationType = NotAuthorizedEverywhere
	default:
		// Default to requiring authorization for all users if not specified
		c.authorizationType = AuthorizedUsersOnly
	}

	// Process users and their auth keys
	oldUserAuthKeys := c.userAuthKeys
	c.userAuthKeys = make(map[string]string)
	c.userSubscribes = make(map[string][]string)

	// Collect all users for which we need to call the callbacks
	affectedUsers := make(map[string]bool)

	for userID, user := range yamlCfg.Users {
		// Process auth keys
		for _, authKey := range user.Keys {
			c.userAuthKeys[authKey] = userID
			if oldUserID, exists := oldUserAuthKeys[authKey]; !exists || oldUserID != userID {
				affectedUsers[userID] = true
			}
		}

		// Process subscribes
		if len(user.Subscribes) > 0 {
			c.userSubscribes[userID] = make([]string, len(user.Subscribes))
			copy(c.userSubscribes[userID], user.Subscribes)
		}
	}

	// Check for removed auth keys
	for authKey, userID := range oldUserAuthKeys {
		if _, exists := c.userAuthKeys[authKey]; !exists {
			affectedUsers[userID] = true
		}
	}

	// Process servers
	c.backends = make(map[string]*Backend)
	for backendID, backend := range yamlCfg.Backends {
		c.backends[backendID] = &Backend{URL: backend.URL, Bearer: backend.Bearer}
	}

	return nil
}

// Close stops the file watcher and cleans up resources
func (c *YamlConfig) Close() error {
	c.StopWatcher()
	return nil
}

// ListenAddr returns the server address
func (c *YamlConfig) ListenAddr() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverAddress, nil
}

func (c *YamlConfig) SetListenAddr(add string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.serverAddress = add
}

// GetUserIDByKeyHash returns the user ID associated with the given key hash
func (c *YamlConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Load the file again to ensure we have the latest data
	var config yamlConfig
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Iterate through users to find the matching key hash
	for userID, user := range config.Users {
		for _, hash := range user.Keys {
			if hash == keyHash {
				return userID, nil
			}
		}
	}

	return "", nil // Return empty string if hash not found
}

// GetUserParams returns the parameters for the given user ID
func (c *YamlConfig) GetUserParams(userID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	params, exists := c.userParams[userID]
	if !exists {
		return make(map[string]string), nil
	}

	// Return a copy to prevent concurrent map access
	result := make(map[string]string, len(params))
	for k, v := range params {
		result[k] = v
	}

	return result, nil
}

// GetUserSubscribes returns the server IDs that the user is subscribed to
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

// GetServer returns the URL for the given server ID
func (c *YamlConfig) GetBackend(backendID string) (*Backend, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	backend, exists := c.backends[backendID]
	if !exists {
		return nil, ErrNotFound
	}

	return backend, nil
}

// Authorization returns the configured authorization type
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

// LogLevel returns the configured log level for Zap
func (c *YamlConfig) LogLevel() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.logLevel, nil
}

// InfoHandler returns the configured info handler path
func (c *YamlConfig) InfoHandler() (string, error) {
	// For YAML config, we don't have this setting
	// Return a default value or empty string
	return c.infoHandlerValue, nil
}

// FrontendAddressForProxy returns the frontend address for proxy
func (c *YamlConfig) FrontendAddressForProxy() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.frontendAddressValue, nil
}

func (c *YamlConfig) Status(ctx context.Context) error {
	return nil
}
