package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"

	"github.com/bboxsys/platform/logger"
)

var (
	DefaultAgentConfigFilenameNoExtension                 = "blackbox-agent"
	DefaultAgentConfigFilename                            = fmt.Sprintf("%s.%s", DefaultAgentConfigFilenameNoExtension, "json")
	DefaultConfigFilenameNoExtension                      = "blackbox-server"
	DefaultConfigFilename                                 = fmt.Sprintf("%s.%s", DefaultConfigFilenameNoExtension, "toml")
	DefaultUserConfigDir                                  = ".config/blackbox"
	DefaultTestConfigDir                                  = ".gounittests"
	DefaultServiceConfigDir                               = "/etc/blackbox"
	DefaultServiceDataDir                                 = "/usr/lib/blackbox"
	DefaultServiceBinDir                                  = "/usr/bin"
	ErrHomeDirNoExist                                     = errors.New("home directory does not exist")
	ValidConfigFileExtensions                             = []string{"toml", "yaml", "yml", "json"}
	validConfigFileExtensionsMap          map[string]bool = make(map[string]bool)
	ConfigKinds                                           = []string{"service", "user"}
	DefaultConfigKind                                     = "user"
	defaultLogger                                         = logger.NewLogger("blackbox", "config")
)

type Config struct {
	V        *viper.Viper
	Kind     string
	Filename string
}

func New(kind string) *Config {
	config := new(Config)
	config.V = viper.New()
	if kind == "" {
		kind = DefaultConfigKind
	}
	config.Kind = kind
	return config
}
func (config *Config) DefaultConfigDir() string {
	switch config.Kind {
	case "service":
		return DefaultServiceConfigDir
	case "user":
		return fmt.Sprintf("%s/%s", GetHomedir(), DefaultUserConfigDir)
	case "test":
		return fmt.Sprintf("%s/%s", GetHomedir(), DefaultTestConfigDir)
	default:
		return fmt.Sprintf("%s/%s", GetHomedir(), DefaultUserConfigDir)
	}
}
func (config *Config) DefaultDataDir() string {
	switch config.Kind {
	case "service":
		return DefaultServiceDataDir
	case "user":
		return fmt.Sprintf("%s/%s", GetHomedir(), DefaultUserConfigDir)
	case "test":
		return fmt.Sprintf("%s/%s", GetHomedir(), DefaultTestConfigDir)
	default:
		return fmt.Sprintf("%s/%s", GetHomedir(), DefaultUserConfigDir)
	}
}
func (config *Config) DefaultBinDir() string {
	switch config.Kind {
	case "service":
		return DefaultServiceBinDir
	case "user":
		return DefaultUserConfigDir
	case "test":
		return DefaultTestConfigDir
	default:
		return DefaultUserConfigDir
	}
}
func (config *Config) DefaultConfigFullPath() string {
	switch config.Kind {
	case "service":
		return fmt.Sprintf("%s/%s", DefaultServiceConfigDir, DefaultConfigFilename)
	case "user":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultConfigFilename)
	case "test":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultTestConfigDir, DefaultConfigFilename)
	default:
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultConfigFilename)
	}
}
func (config *Config) DefaultConfigFullPathNoExtension() string {
	switch config.Kind {
	case "service":
		return fmt.Sprintf("%s/%s", DefaultServiceConfigDir, DefaultConfigFilenameNoExtension)
	case "user":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultConfigFilenameNoExtension)
	case "test":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultTestConfigDir, DefaultConfigFilenameNoExtension)
	default:
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultConfigFilenameNoExtension)
	}
}
func (config *Config) DefaultAgentConfigFullPath() string {
	switch config.Kind {
	case "service":
		return fmt.Sprintf("%s/%s", DefaultServiceConfigDir, DefaultAgentConfigFilename)
	case "user":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultAgentConfigFilename)
	case "test":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultTestConfigDir, DefaultAgentConfigFilename)
	default:
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultAgentConfigFilename)
	}
}
func (config *Config) DefaultAgentConfigFullPathNoExtension() string {
	switch config.Kind {
	case "service":
		return fmt.Sprintf("%s/%s", DefaultServiceConfigDir, DefaultConfigFilenameNoExtension)
	case "user":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultAgentConfigFilenameNoExtension)
	case "test":
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultTestConfigDir, DefaultAgentConfigFilenameNoExtension)
	default:
		return fmt.Sprintf("%s/%s/%s", GetHomedir(), DefaultUserConfigDir, DefaultAgentConfigFilenameNoExtension)
	}
}

// priority:
// 1. command line --config
// 2. environment variable $BBOXSYS_CONFIG
// 3. default
func (config *Config) FromFile() error {
	if config.Filename == "" {
		config.Filename = config.DefaultConfigFullPath()
		defaultLogger.InfoE().Str("filename", config.Filename).Msg("using default config")
	}
	// Expand ~ to home directory
	if expanded, err := homedir.Expand(config.Filename); err == nil {
		config.Filename = expanded
	}
	config.V.SetConfigName(strings.TrimSuffix(filepath.Base(config.Filename), filepath.Ext(config.Filename)))
	config.V.AddConfigPath(filepath.Dir(config.Filename))
	err := config.ConfigDirExists()
	if err != nil {
		return fmt.Errorf("configuration directory does not exist and cannot be created: %v", err)
	}
	err = config.V.ReadInConfig()
	if err != nil {
		return err
	}
	return nil
}

func (config *Config) DataDirExists() error {
	rootPath := config.V.GetString("app.root_path")
	if rootPath == "" {
		rootPath = config.DefaultDataDir()
	}
	return DirExists(rootPath)
}

func (config *Config) ConfigDirExists() error {
	return CreateDir(filepath.Dir(config.Filename))
}
func (config *Config) DefaultConfigDirExists() bool {
	if _, err := os.Stat(config.DefaultConfigDir()); os.IsNotExist(err) {
		return false
	}
	return true
}
func (config *Config) DefaultConfigExists() bool {
	if _, err := os.Stat(config.DefaultConfigFullPath()); os.IsNotExist(err) {
		return false
	}
	return true
}
func (config *Config) CreateDefaultConfigDir() error {
	return CreateDir(config.DefaultConfigDir())
}

// CreateDir creates dir with owner-only permissions (0700). The config tree
// can hold secrets (SMTP and OAuth credentials, the default admin password,
// TLS key paths), so it must not be world- or group-readable.
func CreateDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		err := os.Mkdir(dir, 0700)
		if err != nil {
			return err
		}
	}
	return nil
}
func DirExists(dir string) error {
	return CreateDir(dir)
}

// Get the user's home directory. Returns empty string if unable to complete.
// there is a default homedir package - switch to that.
func GetHomedir() string {
	homedir, err := homedir.Dir()
	if err != nil {
		defaultLogger.ErrorE().Err(err).Msg("error getting homedir")
		return ""
	}
	return homedir
}
