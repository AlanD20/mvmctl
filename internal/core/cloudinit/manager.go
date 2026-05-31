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

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
	"mvmctl/internal/assets"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// requiredISOTool is the command used to create cloud-init ISO images.
// Matches Python's constants.REQUIRED_ISO_TOOL (value: "cloud-localds").
const requiredISOTool = "cloud-localds"

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

// validateTemplateData checks all required TemplateData fields are non-empty,
// mimicking Jinja2's StrictUndefined behavior in the Python implementation.
// If any required field is empty, it returns an error with the field name.
func validateTemplateData(data TemplateData) error {
	required := []struct {
		name  string
		value string
	}{
		{"VMName", data.VMName},
		{"User", data.User},
		{"GuestIP", data.GuestIP},
		{"IPv4Gateway", data.IPv4Gateway},
		{"PasswordHash", data.PasswordHash},
	}
	for _, r := range required {
		if r.value == "" {
			return fmt.Errorf("cloud-init template requires non-empty field: .%s", r.name)
		}
	}
	return nil
}

// Manager handles cloud-init configuration file generation and ISO creation.
// Matches Python's CloudInitManager.
type Manager struct {
	config *model.ProvisionConfig
}

// NewManager creates a new cloud-init Manager with the given provisioning config.
func NewManager(config *model.ProvisionConfig) *Manager {
	return &Manager{config: config}
}

// Generate writes cloud-init configuration files (meta-data, user-data, network-config)
// to the cloud-init seed directory. Matches Python's write_config_files().
func (m *Manager) Generate(ctx context.Context) error {
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

	// Write user-data (either custom or rendered)
	if m.config.CustomUserDataPath != nil {
		if err := m.parseCustomUserData(); err != nil {
			return err
		}
	} else {
		userDataPath := filepath.Join(m.config.CloudInitDir, "user-data")
		if err := os.WriteFile(userDataPath, []byte(rendered["user_data"]), 0644); err != nil {
			return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("write user-data: %w", err))
		}
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
		append([]string{requiredISOTool}, args...),
		system.RunCmdOptions{Capture: true, Check: false},
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

// parseCustomUserData processes custom user data provided to the API.
// Matches Python's _parse_custom_user_data().
func (m *Manager) parseCustomUserData() error {
	if m.config.CustomUserDataPath == nil || *m.config.CustomUserDataPath == "" {
		return nil
	}

	content, err := os.ReadFile(*m.config.CustomUserDataPath)
	if err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("read custom user-data: %w", err))
	}

	contentStr := string(content)

	// Detect content type from first line
	if strings.HasPrefix(contentStr, "#!") {
		// Shell script: write as-is
		userDataPath := filepath.Join(m.config.CloudInitDir, "user-data")
		return os.WriteFile(userDataPath, content, 0644)
	}

	if strings.HasPrefix(contentStr, "Content-Type:") {
		// MIME multi-part: write as-is
		userDataPath := filepath.Join(m.config.CloudInitDir, "user-data")
		return os.WriteFile(userDataPath, content, 0644)
	}

	if !strings.HasPrefix(contentStr, "#cloud-config") {
		// Truncate for error message like Python
		preview := contentStr
		if len(preview) > 80 {
			preview = preview[:80]
		}
		return errs.ValidationFailed(
			errs.CodeCloudInitProvisionFailed,
			fmt.Sprintf(
				"Custom user-data must start with '#cloud-config' (YAML), '#!' (shell script), or 'Content-Type:' (MIME multipart). Got: %q",
				preview,
			),
		)
	}

	// YAML cloud-config: parse, validate, and merge SSH keys
	// Python: yaml.safe_load(content); if isinstance(loaded, dict): ...
	//     else: raise CloudInitProvisionError("Cloud-config user-data must parse to a YAML mapping/object")
	var raw interface{}
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return ErrCloudInitProvisionFailed(
			fmt.Sprintf("Invalid YAML in user-data file: %s", err),
		)
	}
	customUserdata, ok := raw.(map[string]interface{})
	if !ok {
		return ErrCloudInitProvisionFailed(
			"Cloud-config user-data must parse to a YAML mapping/object",
		)
	}

	if err := m.validateUserData(customUserdata); err != nil {
		return err
	}

	// Warn about "network" key in custom user-data — cloud-init will process it
	if _, hasNetwork := customUserdata["network"]; hasNetwork {
		slog.Warn(
			"Custom user-data already contains 'network' key; cloud-init network stage will apply it. Ensure this is intentional.",
			"vm_name",
			m.config.VMName,
		)
	}

	// Merge SSH keys into user-data users
	if len(m.config.SSHPubkeys) > 0 {
		usersRaw, hasUsers := customUserdata["users"]
		if !hasUsers {
			customUserdata["users"] = []interface{}{
				map[string]interface{}{
					"name":                m.config.User,
					"ssh-authorized-keys": m.config.SSHPubkeys,
				},
			}
		} else {
			switch users := usersRaw.(type) {
			case []interface{}:
				userFound := false
				for i, u := range users {
					if userMap, ok := u.(map[string]interface{}); ok {
						if name, ok := userMap["name"]; ok && name == m.config.User {
							existingKeysRaw, hasKeys := userMap["ssh-authorized-keys"]
							var existingKeys []string
							if hasKeys {
								switch k := existingKeysRaw.(type) {
								case []interface{}:
									for _, v := range k {
										if s, ok := v.(string); ok {
											existingKeys = append(existingKeys, s)
										}
									}
								case []string:
									existingKeys = k
								}
							}
							// Merge keys not already present
							keySet := make(map[string]bool)
							for _, k := range existingKeys {
								keySet[k] = true
							}
							for _, k := range m.config.SSHPubkeys {
								if !keySet[k] {
									existingKeys = append(existingKeys, k)
									keySet[k] = true
								}
							}
							userMap["ssh-authorized-keys"] = existingKeys
							users[i] = userMap
							userFound = true
							break
						}
					}
				}
				if !userFound {
					users = append(users, map[string]interface{}{
						"name":                m.config.User,
						"ssh-authorized-keys": m.config.SSHPubkeys,
					})
				}
				customUserdata["users"] = users
			}
		}
	}

	// Write the merged user-data
	out, err := yaml.Marshal(customUserdata)
	if err != nil {
		return errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("marshal merged user-data: %w", err))
	}
	userDataPath := filepath.Join(m.config.CloudInitDir, "user-data")
	return os.WriteFile(userDataPath, []byte("#cloud-config\n"+string(out)), 0644)
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

// validateUserData checks user-data for dangerous cloud-init directives.
// Matches Python's _validate_user_data().
func (m *Manager) validateUserData(userData map[string]interface{}) error {
	var found []string
	for directive := range dangerousCloudInitDirectives {
		if _, ok := userData[directive]; ok {
			found = append(found, directive)
		}
	}

	if len(found) > 0 {
		details := make([]string, 0, len(found))
		for _, d := range found {
			details = append(details, fmt.Sprintf("%s: %s", d, dangerousCloudInitDirectives[d]))
		}
		return ErrCloudInitProvisionFailed(
			fmt.Sprintf(
				"Custom cloud-init user-data contains blocked directive(s): %s. %s",
				strings.Join(found, ", "),
				strings.Join(details, "; "),
			),
		)
	}

	return nil
}

// renderCloudInitTemplate renders the cloud-init template with provided values.
// Matches Python's _render_cloud_init_template().
// Returns a map of section name to rendered content.
func (m *Manager) renderCloudInitTemplate() (map[string]string, error) {
	// Read the embedded template
	templateBytes, err := assets.ReadFile("cloud-init.template.yaml")
	if err != nil {
		return nil, errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("read cloud-init template: %w", err))
	}

	// Parse and render with Go text/template
	tmpl, err := template.New("cloud-init").Parse(string(templateBytes))
	if err != nil {
		return nil, errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("parse cloud-init template: %w", err))
	}

	// Get password from defaults — matches Python's get_default("defaults.vm", "user_password")
	passwordVal, gdErr := infra.GetDefault("defaults.vm", "user_password")
	password := "password"
	if gdErr == nil {
		if pwd, ok := passwordVal.(string); ok && pwd != "" {
			password = pwd
		}
	}

	// Generate password hash — matches Python's generate_password_hash()
	passwordHash, err := generatePasswordHash(password)
	if err != nil {
		return nil, errs.Wrap(errs.CodeCloudInitProvisionFailed,
			fmt.Errorf("generate password hash: %w", err))
	}

	data := TemplateData{
		VMName:           m.config.VMName,
		User:             m.config.User,
		GuestIP:          m.config.GuestIP,
		IPv4Gateway:      m.config.IPv4Gateway,
		NetworkPrefixLen: m.config.NetworkPrefixLen,
		SSHPubkeys:       m.config.SSHPubkeys,
		PasswordHash:     passwordHash,
	}

	// Validate all required fields are non-empty (mimicking Jinja2 StrictUndefined)
	if err := validateTemplateData(data); err != nil {
		return nil, errs.ValidationFailed(
			errs.CodeCloudInitProvisionFailed,
			err.Error(),
		)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, errs.Wrap(errs.CodeCloudInitProvisionFailed, fmt.Errorf("render cloud-init template: %w", err))
	}

	rendered := buf.String()

	// Parse the rendered YAML to extract sections
	// Matches Python's _render_cloud_init_template() exactly.
	sectionHeaders := map[string]bool{
		"user_data":      true,
		"meta_data":      true,
		"network_config": true,
		"nocloud_cfg":    true,
	}

	result := make(map[string]string)
	var currentKey string
	var currentContent []string

	lines := strings.Split(rendered, "\n")
	for _, line := range lines {
		isSectionHeader := false
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			// Match Python: line.endswith(": |") or line.endswith(":|>") or line.endswith(":|-")
			if strings.HasSuffix(line, ": |") || strings.HasSuffix(line, ":|>") || strings.HasSuffix(line, ":|-") {
				// Python: section_name = line.rsplit(":", 1)[0]
				// Go equivalent: find last ":" and take everything before it
				colonIdx := strings.LastIndex(line, ":")
				if colonIdx >= 0 {
					sectionName := line[:colonIdx]
					if sectionHeaders[sectionName] {
						if currentKey != "" {
							result[currentKey] = strings.Join(currentContent, "\n")
						}
						currentKey = sectionName
						currentContent = nil
						isSectionHeader = true
					}
				}
			}
		}
		if !isSectionHeader && currentKey != "" {
			currentContent = append(currentContent, line)
		}
	}
	if currentKey != "" {
		result[currentKey] = strings.Join(currentContent, "\n")
	}

	// Dedent content like Python's textwrap.dedent
	for key, value := range result {
		result[key] = infra.Dedent(value)
	}

	return result, nil
}

// generatePasswordHash generates a Unix password hash for cloud-init.
// Always uses bcrypt — only bcrypt is supported.
func generatePasswordHash(password string) (string, error) {
	return bcryptHash(password)
}

// bcryptHash generates a bcrypt password hash using golang.org/x/crypto/bcrypt.
func bcryptHash(password string) (string, error) {
	// bcrypt.GenerateFromPassword uses cost 10 by default, matching
	// Python's passlib default bcrypt rounds.
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt hash failed: %w", err)
	}
	return string(hashBytes), nil
}
