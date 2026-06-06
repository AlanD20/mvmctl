package cloudinit

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode/utf8"

	"mvmctl/internal/assets"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/system"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// TemplateData holds the data passed to the cloud-init Go template.
// Field names must match the Go template syntax in cloud_init.template.yaml:
// {{.VMName}}, {{.GuestIP}}/{{.NetworkPrefixLen}}, etc.
type TemplateData struct {
	VMName           string
	User             string
	GuestIP          string
	IPv4Gateway      string
	NetworkPrefixLen int // matches {{.NetworkPrefixLen}} in template
	SSHPubkeys       []string
	PasswordHash     string
}

// cloudInitUser represents a user entry in cloud-init user-data YAML.
// Explicit fields are for keys we manipulate; Extra preserves all other keys
// via yaml:",inline" pass-through.
type cloudInitUser struct {
	Name              string                 `yaml:"name"`
	SSHAuthorizedKeys []string               `yaml:"ssh-authorized-keys,omitempty"`
	Extra             map[string]any `yaml:",inline"`
}

// dangerousCloudInitDirectives lists cloud-init directives that could be security risks.
// Matches Python's _DANGEROUS_CLOUD_INIT_DIRECTIVES (module-level dict).
var dangerousCloudInitDirectives = map[string]string{
	"write_files": "Can write arbitrary files to the system",
	"runcmd":      "Can execute arbitrary commands",
	"bootcmd":     "Can execute commands at boot",
	"snap":        "Can install snap packages",
	"apt":         "Can install packages (use with caution)",
	"yum":         "Can install packages (use with caution)",
	"packages":    "Can install packages (use with caution)",
}

// Manager handles cloud-init configuration file generation and ISO creation.
// Matches Python's CloudInitManager.
type Manager struct {
	config *Config
}

// NewManager creates a new cloud-init Manager with the given provisioning config.
func NewManager(config *Config) *Manager {
	return &Manager{config: config}
}

// Generate writes cloud-init configuration files (meta-data, user-data, network-config)
// to the cloud-init seed directory. Matches Python's write_config_files().
func (m *Manager) Generate(ctx context.Context) error {
	// Custom user data is the entire cloud-init content — write it directly.
	if m.config.CustomCloudInitConfig != nil {
		return m.parseCustomCloudInitConfig()
	}

	// Render template sections
	rendered, err := m.renderCloudInitTemplate()
	if err != nil {
		return err
	}

	// Write meta-data
	metaDataPath := filepath.Join(m.config.CloudInitDir, "meta-data")
	if err := os.WriteFile(metaDataPath, []byte(rendered["meta_data"]), 0644); err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("write meta-data: %w", err))
	}

	// Write network-config if not skipped
	if !m.config.SkipNetworkConfig {
		if ncContent, ok := rendered["network_config"]; ok && ncContent != "" {
			ncPath := filepath.Join(m.config.CloudInitDir, "network-config")
			if err := os.WriteFile(ncPath, []byte(ncContent), 0644); err != nil {
				return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("write network-config: %w", err))
			}
		}
	}

	// Write user-data
	userDataPath := filepath.Join(m.config.CloudInitDir, "user-data")
	if err := os.WriteFile(userDataPath, []byte(rendered["user_data"]), 0644); err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("write user-data: %w", err))
	}

	return nil
}

// CreateSeedISO creates a cloud-init ISO from the seed directory using cloud-localds.
// Matches Python's create_seed_iso() exactly.
func (m *Manager) CreateSeedISO(ctx context.Context, cloudInitDir, outputISO string) error {
	// Validate required files exist (network-config is optional for NO_CLOUD_NET mode)
	// Python: raise CloudInitError(f"Missing required cloud-init file: {filename}")
	requiredFiles := []string{"meta-data", "user-data"}
	for _, filename := range requiredFiles {
		fp := filepath.Join(cloudInitDir, filename)
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			return ErrCloudInitFailed(
				fmt.Sprintf("Missing required cloud-init file: %s", filename),
			)
		}
	}

	// Build command: cloud-localds -v [-N network-config] <output_iso> <user-data> <meta-data>
	networkConfigPath := filepath.Join(cloudInitDir, "network-config")
	hasNetworkConfig := false
	if _, err := os.Stat(networkConfigPath); err == nil {
		hasNetworkConfig = true
	}

	args := []string{"-v"}
	if hasNetworkConfig {
		args = append(args, "-N", networkConfigPath)
	}
	args = append(args, outputISO,
		filepath.Join(cloudInitDir, "user-data"),
		filepath.Join(cloudInitDir, "meta-data"),
	)

	// Python: run_cmd(cmd, check=True)
	//         except ProcessError as e:
	//             raise CloudInitError(f"Failed to create cloud-init ISO: {e}") from e
	// Python's ProcessError message format:
	//   "Command failed (exit N): cloud-localds\n[sanitized_stderr]"
	// where sanitized_stderr is trimmed and limited to 100 chars.
	result := system.RunCmdCompat(
		ctx,
		append([]string{infra.RequiredISOTool}, args...),
		system.RunCmdOpts{Capture: true, Check: false},
	)
	if !result.Success {
		exitCode := result.ExitCode
		stderr := strings.TrimSpace(result.Stderr)
		sanitized := stderr
		if len(sanitized) > 100 {
			sanitized = sanitized[:100] + "..."
		}
		processMsg := fmt.Sprintf("Command failed (exit %d): cloud-localds", exitCode)
		if sanitized != "" {
			processMsg += "\n" + sanitized
		}
		return ErrCloudInitFailed(
			fmt.Sprintf("Failed to create cloud-init ISO: %s", processMsg),
		)
	}

	return nil
}

// parseCustomCloudInitConfig processes a custom cloud-init config provided to the API.
// Matches Python's _parse_custom_user_data().
func (m *Manager) parseCustomCloudInitConfig() error {
	if m.config.CustomCloudInitConfig == nil || *m.config.CustomCloudInitConfig == "" {
		return nil
	}

	// Resolve and validate path — prevent path traversal
	configPath := filepath.Clean(*m.config.CustomCloudInitConfig)
	if strings.Contains(configPath, "..") {
		return errs.ValidationFailed(
			errs.CodeCloudInitProvisionFailed,
			fmt.Sprintf("cloud-init config path must not contain '..': %s", configPath),
		)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("read custom cloud-init config: %w", err))
	}

	contentStr := string(content)

	// Strip UTF-8 BOM if present (Windows editors may add one)
	contentStr = strings.TrimPrefix(contentStr, "\ufeff")

	// Detect content type from first line
	if strings.HasPrefix(contentStr, "#!") {
		// Shell script: write as-is (including meta-data)
		return m.writeCustomCloudInitFiles(content)
	}

	if strings.HasPrefix(contentStr, "Content-Type:") {
		// MIME multi-part: write as-is (including meta-data)
		return m.writeCustomCloudInitFiles(content)
	}

	if !strings.HasPrefix(contentStr, "#cloud-config") {
		// Truncate for error message at rune boundary (like Python)
		preview := contentStr
		runeCount := utf8.RuneCountInString(preview)
		if runeCount > 80 {
			preview = string([]rune(preview)[:80])
		}
		return errs.ValidationFailed(
			errs.CodeCloudInitProvisionFailed,
			fmt.Sprintf(
				"custom cloud-init config must start with '#cloud-config' (YAML), '#!' (shell script), or 'Content-Type:' (MIME multipart). Got: %q",
				preview,
			),
		)
	}

	// YAML cloud-config: parse, validate, and merge SSH keys
	var raw any
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("invalid YAML in cloud-init config file: %w", err))
	}
	customUserdata, ok := raw.(map[string]any)
	if !ok {
		return errs.ValidationFailed(
			errs.CodeCloudInitProvisionFailed,
			"cloud-init config must parse to a YAML mapping/object",
		)
	}

	if err := validateCloudinitConfig(customUserdata); err != nil {
		return err
	}

	// Warn about "network" key in custom config — cloud-init will process it
	if _, hasNetwork := customUserdata["network"]; hasNetwork {
		slog.Warn(
			"Custom cloud-init config already contains 'network' key; cloud-init network stage will apply it. Ensure this is intentional.",
			"vm_name",
			m.config.VMName,
		)
	}

	// Merge SSH keys into user-data users
	if err := m.mergeSSHKeys(customUserdata); err != nil {
		return err
	}

	// Write meta-data (required by cloud-init for NO_CLOUD datasource)
	if err := m.writeMetaData(); err != nil {
		return err
	}

	// Write the merged cloud-init config
	out, err := yaml.Marshal(customUserdata)
	if err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("marshal merged user-data: %w", err))
	}
	userDataPath := filepath.Join(m.config.CloudInitDir, "user-data")
	return os.WriteFile(userDataPath, []byte("#cloud-config\n"+string(out)), 0644)
}

// writeCustomCloudInitFiles writes meta-data and user-data for shell-script and MIME custom configs.
func (m *Manager) writeCustomCloudInitFiles(userDataContent []byte) error {
	if err := m.writeMetaData(); err != nil {
		return err
	}
	userDataPath := filepath.Join(m.config.CloudInitDir, "user-data")
	return os.WriteFile(userDataPath, userDataContent, 0644)
}

// writeMetaData writes the required cloud-init meta-data file for NO_CLOUD datasource.
func (m *Manager) writeMetaData() error {
	metaDataPath := filepath.Join(m.config.CloudInitDir, "meta-data")
	content := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", m.config.VMID, m.config.VMName)
	return os.WriteFile(metaDataPath, []byte(content), 0644)
}

// mergeSSHKeys merges configured SSH public keys into the custom cloud-init user-data.
// If no users entry exists, creates one with the configured user and keys.
// If the user already exists, only appends keys not already present.
// Uses typed structs for safe manipulation while preserving all other user fields.
func (m *Manager) mergeSSHKeys(customUserdata map[string]any) error {
	if len(m.config.SSHPubkeys) == 0 {
		return nil
	}

	usersRaw, hasUsers := customUserdata["users"]
	if !hasUsers {
		customUserdata["users"] = []any{
			map[string]any{
				"name":                m.config.User,
				"ssh-authorized-keys": m.config.SSHPubkeys,
			},
		}
		return nil
	}

	// Marshal users to YAML and unmarshal into typed structs for safe manipulation
	usersYAML, err := yaml.Marshal(usersRaw)
	if err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("marshal users for merge: %w", err))
	}

	var users []cloudInitUser
	if err := yaml.Unmarshal(usersYAML, &users); err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("unmarshal users for merge: %w", err))
	}

	// Find and update the target user
	found := false
	for i, u := range users {
		if u.Name == m.config.User {
			// Build set of existing keys
			keySet := make(map[string]struct{}, len(u.SSHAuthorizedKeys))
			for _, k := range u.SSHAuthorizedKeys {
				keySet[k] = struct{}{}
			}
			// Append keys not already present
			for _, k := range m.config.SSHPubkeys {
				if _, exists := keySet[k]; !exists {
					u.SSHAuthorizedKeys = append(u.SSHAuthorizedKeys, k)
					keySet[k] = struct{}{}
				}
			}
			users[i] = u
			found = true
			break
		}
	}

	if !found {
		users = append(users, cloudInitUser{
			Name:              m.config.User,
			SSHAuthorizedKeys: m.config.SSHPubkeys,
			Extra:             make(map[string]any),
		})
	}

	// Convert back to generic types for storage in the custom data map
	mergedYAML, err := yaml.Marshal(users)
	if err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("marshal merged users: %w", err))
	}

	var merged []any
	if err := yaml.Unmarshal(mergedYAML, &merged); err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("unmarshal merged users: %w", err))
	}

	customUserdata["users"] = merged
	return nil
}

// renderCloudInitTemplate renders the cloud-init template with provided values.
// Uses Go text/template named templates for each section (user_data, meta_data, etc.)
// via {{define "section_name"}}...{{end}} blocks in the template file.
func (m *Manager) renderCloudInitTemplate() (map[string]string, error) {
	templateBytes, err := assets.ReadFile("cloud-init.template.yaml")
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeCloudInitProvisionFailed,
			Op:      "cloudinit",
			Message: "read cloud-init template",
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}

	tmpl, err := template.New("cloud-init").Parse(string(templateBytes))
	if err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeCloudInitProvisionFailed,
			Op:      "cloudinit",
			Message: "parse cloud-init template",
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}

	// Generate password hash from resolved UserPassword
	// bcrypt cost 10 matches Python's passlib default.
	hashBytes, hashErr := bcrypt.GenerateFromPassword([]byte(m.config.UserPassword), bcrypt.DefaultCost)
	if hashErr != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodeCloudInitProvisionFailed,
			Op:      "cloudinit",
			Message: "generate password hash",
			Err:     hashErr,
			Class:   errs.ClassInternal,
		}
	}
	passwordHash := string(hashBytes)

	data := TemplateData{
		VMName:           m.config.VMName,
		User:             m.config.User,
		GuestIP:          m.config.GuestIP,
		IPv4Gateway:      m.config.IPv4Gateway,
		NetworkPrefixLen: m.config.NetworkPrefixLen,
		SSHPubkeys:       m.config.SSHPubkeys,
		PasswordHash:     passwordHash,
	}

	// Validate all required fields are non-empty
	if err := validateTemplateData(data); err != nil {
		return nil, errs.ValidationFailed(
			errs.CodeCloudInitProvisionFailed,
			err.Error(),
		)
	}

	// Render each named section independently — no YAML parsing hack needed.
	sectionNames := []string{"user_data", "meta_data", "network_config", "nocloud_cfg"}
	result := make(map[string]string, len(sectionNames))
	for _, name := range sectionNames {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeCloudInitProvisionFailed,
				Op:      "cloudinit",
				Message: fmt.Sprintf("render section %q", name),
				Err:     err,
				Class:   errs.ClassInternal,
			}
		}
		result[name] = buf.String()
	}

	return result, nil
}
