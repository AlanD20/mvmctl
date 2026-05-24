package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// NewBinaryCmd creates the binary command and its subcommands.
func NewBinaryCmd(binaryAPI *api.BinaryOperation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bin",
		Aliases: []string{"binary"},
		Short:   "Binary management",
	}

	cmd.AddCommand(newBinaryLsCmd(binaryAPI))
	cmd.AddCommand(newBinaryPullCmd(binaryAPI))
	cmd.AddCommand(newBinaryRmCmd(binaryAPI))
	cmd.AddCommand(newBinaryDefaultCmd(binaryAPI))

	// Hidden help subcommand matching Python's Typer "help" command
	helpCmd := &cobra.Command{
		Use:    "help",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Parent().Help()
		},
	}
	cmd.AddCommand(helpCmd)

	return cmd
}

func newBinaryLsCmd(binaryAPI *api.BinaryOperation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool
	var remote bool
	var limit int

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List local (and optionally remote) Firecracker versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			local, err := binaryAPI.ListAll(cmd.Context())
			if err != nil {
				return err
			}

			localVersions := make(map[string]bool)
			for _, b := range local {
				if b.Name == "firecracker" {
					localVersions[b.Version] = true
				}
			}

			if jsonOutput {
				type binJSON struct {
					ID          string  `json:"id"`
					Name        string  `json:"name"`
					Version     string  `json:"version"`
					FullVersion string  `json:"full_version"`
					CIVersion   *string `json:"ci_version"`
					Path        string  `json:"path"`
					IsDefault   bool    `json:"is_default"`
					IsPresent   bool    `json:"is_present"`
					CreatedAt   string  `json:"created_at"`
					UpdatedAt   string  `json:"updated_at"`
				}
				data := make([]binJSON, 0, len(local))
				for _, b := range local {
					data = append(data, binJSON{
						ID:          b.ID,
						Name:        b.Name,
						Version:     b.Version,
						FullVersion: b.FullVersion,
						CIVersion:   b.CIVersion,
						Path:        b.Path,
						IsDefault:   b.IsDefault,
						IsPresent:   b.IsPresent,
						CreatedAt:   b.CreatedAt,
						UpdatedAt:   b.UpdatedAt,
					})
				}
				b, _ := json.MarshalIndent(data, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			if remote {
				fmt.Fprintf(os.Stderr, "Fetching remote versions...\n")
				remoteVersions, err := binaryAPI.ListRemote(cmd.Context(), limit)
				if err != nil {
					return err
				}

				rows := make([][]string, 0, len(remoteVersions))
				for _, ver := range remoteVersions {
					cached := " "
					if localVersions[ver] {
						cached = "✓"
					}
					rows = append(rows, []string{cached, ver})
				}
				cli.Table([]string{"Downloaded", "Version"}, rows)
				return nil
			}

			if longOutput {
				rows := make([][]string, 0, len(local))
				for _, b := range local {
					marker := cli.FormatMarker(b.IsDefault)
					shortID := cli.FormatID(b.ID)
					fullVer := b.FullVersion
					if fullVer == "" {
						fullVer = "-"
					}
					created := cli.FormatTimestamp(b.CreatedAt, "relative")
					rows = append(rows, []string{marker, shortID, b.Name, b.Version, fullVer, created})
				}
				cli.Table([]string{"", "ID", "Name", "Version", "Full Version", "Created"}, rows)
			} else {
				rows := make([][]string, 0, len(local))
				for _, b := range local {
					marker := cli.FormatMarker(b.IsDefault)
					shortID := cli.FormatID(b.ID)
					created := cli.FormatTimestamp(b.CreatedAt, "relative")
					rows = append(rows, []string{marker, shortID, b.Name, b.Version, created})
				}
				cli.Table([]string{"", "ID", "Name", "Version", "Created"}, rows)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	cmd.Flags().BoolVarP(&remote, "remote", "r", false, "Also show remote versions")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max remote versions to show")
	return cmd
}

func newBinaryPullCmd(binaryAPI *api.BinaryOperation) *cobra.Command {
	var version string
	var gitRef string
	var setDefault bool
	var force bool

	cmd := &cobra.Command{
		Use:                "pull [name]",
		Short:              "Download a Firecracker version or build from source",
		Args:               cobra.ExactArgs(1),
		ValidArgsFunction:  completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if strings.ToLower(name) != "firecracker" {
				cli.Error(fmt.Sprintf("Unsupported binary: '%s'. Only 'firecracker' is supported for download or build.", name))
				return fmt.Errorf("unsupported binary")
			}

			if gitRef != "" && version != "" {
				cli.Error("--git-ref and --version are mutually exclusive. Use --git-ref to build from source, or --version to download a release.")
				return fmt.Errorf("mutually exclusive options")
			}

			// Git build path
			if gitRef != "" {
				cli.Info(fmt.Sprintf("Building Firecracker from ref '%s' via Docker-based devtool...", gitRef))
				cli.Info("  Phase 1: Cloning/updating Firecracker source (git)")
				cli.Info("  Phase 2: Building release binary (5-15 min via Docker)")
				cli.Info("  The build output will appear below once it starts:\n")

			gitRefPtr := &gitRef
			dwldOverride := false
			result := binaryAPI.Pull(cmd.Context(), &inputs.BinaryPullInput{
				Version:          "",
				Name:             name,
				GitRef:           gitRefPtr,
				SetDefault:       setDefault,
				DownloadOverride: &dwldOverride,
			})
				if result.Status == "error" {
					cli.Error(result.Message)
					return fmt.Errorf("%s", result.Message)
				}
				binaries, _ := result.Item.([]*model.BinaryItem)

				cli.Info("")
				for _, b := range binaries {
					shortID := cli.FormatID(b.ID)
					cli.Success(fmt.Sprintf("Built: %s %s: %s", b.Name, b.Version, b.Path))
					cli.Info(fmt.Sprintf("  ID: %s", shortID))
				}

				if setDefault && len(binaries) > 0 {
					cli.Success(fmt.Sprintf("Default binary set to: %s", binaries[0].Version))
				}

				return nil
			}

			// Normal download path
			ver := version

			dwldOverride := force
			result := binaryAPI.Pull(cmd.Context(), &inputs.BinaryPullInput{
				Version:          ver,
				Name:             name,
				SetDefault:       setDefault,
				DownloadOverride: &dwldOverride,
			})

			// If binary already exists and --force wasn't set, offer to re-download
			if result.Status == "error" && result.Code == string(errs.CodeBinaryAlreadyExists) && !force {
				cli.Warning(result.Message)
				// Prompt for re-download matching typer.confirm() behavior
				confirmed := false
				prompt := "Re-download? [y/N]: "
			promptLoop:
				for {
					fmt.Fprint(os.Stderr, prompt)
					var response string
					_, err := fmt.Scanln(&response)
					if err != nil {
						response = ""
					}
					response = strings.TrimSpace(response)
					switch strings.ToLower(response) {
					case "y", "yes":
						confirmed = true
						break promptLoop
					case "n", "no":
						break promptLoop
					case "":
						break promptLoop
					default:
						prompt = "Please enter 'yes' or 'no': "
					}
				}
				if confirmed {
					overrideTrue := true
				result = binaryAPI.Pull(cmd.Context(), &inputs.BinaryPullInput{
						Version:          ver,
						Name:             name,
						SetDefault:       setDefault,
						DownloadOverride: &overrideTrue,
					})
				} else {
					cli.Info("Aborted")
					return nil
				}
			}

			if result.Status == "error" {
				cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}

			if result.Status == "skipped" {
				cli.Info(result.Message)
				if binaries, ok := result.Item.([]*model.BinaryItem); ok {
					for _, b := range binaries {
						shortID := cli.FormatID(b.ID)
						cli.Info(fmt.Sprintf("  %s v%s: %s", b.Name, b.Version, shortID))
					}
				}
				return nil
			}

			binaries, _ := result.Item.([]*model.BinaryItem)
			for _, b := range binaries {
				shortID := cli.FormatID(b.ID)
				cli.Success(fmt.Sprintf("Downloaded: %s v%s: %s", b.Name, b.Version, b.Path))
				cli.Info(fmt.Sprintf("  ID: %s", shortID))
			}

			if setDefault && len(binaries) > 0 {
				cli.Success(fmt.Sprintf("Default binary set to: v%s", binaries[0].Version))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Version to download (e.g. 1.15.0, latest)")
	cmd.Flags().StringVar(&gitRef, "git-ref", "", "Git ref (branch/tag/commit) to build from source. Mutually exclusive with --version.")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default after download")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Re-download even if version already exists")
	return cmd
}

func newBinaryRmCmd(binaryAPI *api.BinaryOperation) *cobra.Command {
	var version string
	var force bool

	cmd := &cobra.Command{
		Use:   "rm [identifiers...]",
		Short: "Remove one or more binaries. Use --version to remove by version pair.",
		Args:  cobra.ArbitraryArgs,
		ValidArgsFunction:  completeBinaryVersions,
		DisableSuggestions: true,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if version != "" {
				result := binaryAPI.RemoveByVersion(cmd.Context(), version, force)
				if result.IsError() {
					cli.Error(result.Message)
					return fmt.Errorf("%s", result.Message)
				}
				cli.Success(fmt.Sprintf("Removed: v%s", version))
				return nil
			}

			effectiveIDs := args
			if len(effectiveIDs) == 0 {
				cli.Error("Provide at least one binary ID to remove or use --version")
				return fmt.Errorf("usage error")
			}

		batchResult := binaryAPI.Remove(cmd.Context(), &inputs.BinaryInput{Identifiers: effectiveIDs}, force)
		for _, r := range batchResult.Items {
			if r.Status == "success" {
				msg := r.Message
				if msg == "" {
					msg = "Removed"
				}
				cli.Success(msg)
			} else {
				msg := r.Message
				if msg == "" {
					msg = "Remove failed"
				}
				cli.Error(msg)
			}
		}
			if batchResult.HasErrors() {
				return fmt.Errorf("one or more removals failed")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Remove both firecracker and jailer for this version")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Remove even if referenced by VMs")
	return cmd
}

func newBinaryDefaultCmd(binaryAPI *api.BinaryOperation) *cobra.Command {
	return &cobra.Command{
		Use:                "default [identifier]",
		Short:              "Set a binary as the active default",
		Args:               cobra.ExactArgs(1),
		ValidArgsFunction:  completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			result := binaryAPI.SetDefault(cmd.Context(), &inputs.BinaryInput{Identifiers: []string{identifier}})
			if result.IsError() {
				cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}

			msg := result.Message
			if msg == "" {
				msg = fmt.Sprintf("Default binary set to: %s", identifier)
			}
			cli.Success(msg)
			return nil
		},
	}
}
