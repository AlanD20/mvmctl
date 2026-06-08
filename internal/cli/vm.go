package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/infra/model"
	infraptr "mvmctl/internal/infra/ptr"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// vmColumns defines the local listing columns for VMs.
var vmColumns = []common.ListingColumn{
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.VM).ID) }},
	{Header: "Name", Extract: func(v any) string { return v.(*model.VM).Name }},
	{Header: "Status", Extract: func(v any) string { return string(v.(*model.VM).Status) }},
	{Header: "Exit", Extract: func(v any) string {
		ec := v.(*model.VM).ExitCode
		if ec != nil {
			return fmt.Sprintf("%d", *ec)
		}
		return "-"
	}},
	{Header: "IPv4", Extract: func(v any) string {
		ip := v.(*model.VM).IPv4
		if ip == "" {
			return "-"
		}
		return ip
	}},
	{Header: "Resources", Extract: func(v any) string {
		vm := v.(*model.VM)
		return fmt.Sprintf("%d vCPU / %d MiB / %d MiB", vm.VCPUCount, vm.MemSizeMiB, vm.DiskSizeMiB)
	}, LongOnly: true},
	{
		Header:   "Image",
		Extract:  func(v any) string { return common.Cli.FormatID(v.(*model.VM).ImageID) },
		LongOnly: true,
	},
	{
		Header:   "Kernel",
		Extract:  func(v any) string { return common.Cli.FormatID(v.(*model.VM).KernelID) },
		LongOnly: true,
	},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.VM).CreatedAt, "relative") },
	},
}

func NewVMCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "VM lifecycle management",
	}

	cmd.AddCommand(newVMListCmd(op))
	cmd.AddCommand(newVMpsCmd(op))
	cmd.AddCommand(newVMCreateCmd(op))
	cmd.AddCommand(newVMRemoveCmd(op))
	cmd.AddCommand(newVMStartCmd(op))
	cmd.AddCommand(newVMStopCmd(op))
	cmd.AddCommand(newVMRebootCmd(op))
	cmd.AddCommand(newVMPauseCmd(op))
	cmd.AddCommand(newVMResumeCmd(op))
	cmd.AddCommand(newVMSnapshotCmd(op))
	cmd.AddCommand(newVMLoadCmd(op))
	cmd.AddCommand(newVMInspectCmd(op))
	cmd.AddCommand(newVMExportCmd(op))
	cmd.AddCommand(newVMImportCmd(op))
	cmd.AddCommand(newVMAttachVolumeCmd(op))
	cmd.AddCommand(newVMDetachVolumeCmd(op))
	return cmd
}

// ─── ls (list all VMs) ────────────────────────────────────────────────────────

func newVMListCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List all VMs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVMList(op, cmd, jsonOutput, longOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	return cmd
}

func runVMList(op *api.Operation, cmd *cobra.Command, jsonOutput, longOutput bool) error {
	vms := op.VMList(cmd.Context())

	if jsonOutput {
		if vms == nil {
			vms = []*model.VM{}
		}
		b, _ := json.MarshalIndent(vms, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	style := common.Cli.ResolveListingStyle(cmd.Context(), op, longOutput)
	common.RenderListing(vms, vmColumns, style)
	return nil
}

// ─── ps (list running VMs) ────────────────────────────────────────────────────

func newVMpsCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List running VMs (active processes).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVMps(op, cmd, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runVMps(op *api.Operation, cmd *cobra.Command, jsonOutput bool) error {
	// Server-side filtering matching Python's list_all(status=[...])
	vms := op.VMList(cmd.Context(), string(model.VMStatusStarting), string(model.VMStatusRunning))

	if jsonOutput {
		if vms == nil {
			vms = []*model.VM{}
		}
		b, _ := json.MarshalIndent(vms, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	if len(vms) == 0 {
		common.Cli.Success("No active VMs")
		return nil
	}

	style := common.Cli.ResolveListingStyle(cmd.Context(), op, false)
	common.RenderListing(vms, vmColumns, style)
	return nil
}

// ─── create ───────────────────────────────────────────────────────────────────

func newVMCreateCmd(op *api.Operation) *cobra.Command {
	var (
		image           string
		kernel          string
		vcpus           int
		mem             string
		diskSize        string
		ip              string
		networkName     string
		mac             string
		sshKey          string
		userData        string
		cloudInitMode   string
		nocloudNetPort  int
		user            string
		noPCI           bool
		nestedVirt      bool
		noNestedVirt    bool
		cpuTemplate     string
		noConsole       bool
		bootArgs        string
		lsmFlags        string
		enableLogging   bool
		noEnableLogging bool
		enableMetrics   bool
		noEnableMetrics bool
		count           int
		atomic          bool
		skipCleanup     bool
		skipDeblob      bool
		force           bool
		volume          []string
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create and start a new Firecracker VM.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if skipCleanup && !force {
				cleanupConfirmed, pErr := common.Cli.PromptConfirm(
					cmd.Context(),
					"--skip-cleanup is set: if creation fails, resources will be left behind and must be cleaned manually. Continue?",
					true,
				)
				if pErr != nil {
					return pErr
				}
				if !cleanupConfirmed {
					common.Cli.Info("Aborted")
					return nil
				}
			}

			var sshKeyList []string
			if sshKey != "" {
				sshKeyList = strings.Split(sshKey, ",")
			}

			effectiveCount := max(count, 1)
			if effectiveCount > 1 && len(volume) > 0 {
				return fmt.Errorf("--count and --volume are mutually exclusive")
			}

			prog := common.NewProgress()
			prog.Start("Creating VM...")
			defer prog.Stop()

			if cpuTemplate != "" {
				fi, err := os.Stat(cpuTemplate)
				if err != nil {
					if os.IsNotExist(err) {
						return fmt.Errorf("invalid value for '--cpu-template': path '%s' does not exist", cpuTemplate)
					}
					return fmt.Errorf("invalid value for '--cpu-template': %w", err)
				}
				if fi.IsDir() {
					return fmt.Errorf("invalid value for '--cpu-template': path '%s' is a directory", cpuTemplate)
				}
			}

			input := inputs.VMCreateInput{
				Name:        name,
				SSHKeys:     sshKeyList,
				NoConsole:   noConsole,
				BootArgs:    bootArgs,
				LSMFlags:    lsmFlags,
				CPUTemplate: cpuTemplate,
				MemSizeMib:  mem,
				DiskSize:    diskSize,
				SkipCleanup: skipCleanup,
				SkipDeblob:  skipDeblob,
				Atomic:      atomic,
				Volumes:     volume,
			}

			if cmd.Flags().Changed("image") {
				input.Image = infraptr.Ptr(image)
			}
			if cmd.Flags().Changed("kernel") {
				input.KernelID = infraptr.Ptr(kernel)
			}
			if cmd.Flags().Changed("vcpus") {
				input.VCPUCount = infraptr.Ptr(vcpus)
			}
			if cmd.Flags().Changed("ip") {
				input.RequestedGuestIP = infraptr.Ptr(ip)
			}
			if cmd.Flags().Changed("network") {
				input.NetworkName = infraptr.Ptr(networkName)
			}
			if cmd.Flags().Changed("mac") {
				input.RequestedGuestMAC = infraptr.Ptr(mac)
			}
			if cmd.Flags().Changed("user") {
				input.User = infraptr.Ptr(user)
			}
			if cmd.Flags().Changed("cloud-init-mode") {
				input.CloudInitMode = infraptr.Ptr(cloudInitMode)
			}
			if cmd.Flags().Changed("cloudinit-config") {
				input.CustomCloudInitConfig = infraptr.Ptr(userData)
			}
			if cmd.Flags().Changed("nocloud-net-port") {
				input.NocloudNetPort = infraptr.Ptr(nocloudNetPort)
			}
			if cmd.Flags().Changed("count") {
				input.Count = infraptr.Ptr(effectiveCount)
			}
			if cmd.Flags().Changed("nested-virt") {
				input.NestedVirt = infraptr.Ptr(nestedVirt)
			} else if cmd.Flags().Changed("no-nested-virt") {
				input.NestedVirt = infraptr.Ptr(false)
			}
			if cmd.Flags().Changed("enable-logging") {
				input.EnableLogging = infraptr.Ptr(enableLogging)
			} else if cmd.Flags().Changed("no-enable-logging") {
				input.EnableLogging = infraptr.Ptr(false)
			}
			if cmd.Flags().Changed("enable-metrics") {
				input.EnableMetrics = infraptr.Ptr(enableMetrics)
			} else if cmd.Flags().Changed("no-enable-metrics") {
				input.EnableMetrics = infraptr.Ptr(false)
			}
			if cmd.Flags().Changed("no-pci") {
				input.PCIEnabled = infraptr.Ptr(false)
			}

			vms, err := op.VMCreate(cmd.Context(), input, func(e event.Progress) {
				if e.Message != "" {
					prog.UpdateText(e.Message)
				}
			})
			if err != nil {
				return fmt.Errorf("create VMs: %w", err)
			}

			if len(vms) == 0 {
				return fmt.Errorf("no VMs returned")
			}

			names := make([]string, len(vms))
			for i, v := range vms {
				names[i] = v.Name
			}
			common.Cli.Success(fmt.Sprintf("Created: %s", strings.Join(names, ", ")))
			if input.NestedVirt != nil && *input.NestedVirt {
				common.Cli.Info("Nested virtualization: enabled")
			}
			return nil
		},
	}

	cmd.Flags().
		StringVar(&image, "image", "", "Image name, type:version (e.g. ubuntu:24.04), short ID, or path to .ext4 file")
	cmd.Flags().StringVar(&kernel, "kernel", "", "Kernel short ID or path to vmlinux file")
	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "Number of vCPUs (default: from user config)")
	cmd.Flags().StringVar(&mem, "mem", "", "Memory in MiB or GiB (e.g. 512M, 1G, 4096). Default: from user config")
	cmd.Flags().
		StringVarP(&diskSize, "disk-size", "s", "", "Rootfs disk size in MiB/GiB (e.g., 512M=512MiB, 1G=1GiB). Default from config.")
	cmd.Flags().StringVar(&ip, "ip", "", "Guest IP (auto-assigned if omitted)")
	cmd.Flags().StringVar(&networkName, "network", "", "Named network to use")
	cmd.Flags().StringVar(&mac, "mac", "", "Custom MAC address (auto-generated if omitted)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key name (from key cache) or file path")
	cmd.Flags().StringVar(&userData, "cloudinit-config", "", "Path to custom cloud-init configuration file")
	cmd.Flags().
		StringVar(&cloudInitMode, "cloud-init-mode", "", "Cloud-init mode: 'inject' (direct injection), 'iso' (ISO mode), 'net' (HTTP), 'off' (default, no cloud-init)")
	cmd.Flags().
		IntVar(&nocloudNetPort, "nocloud-net-port", 0, "Port for nocloud-net HTTP server (0 for auto-assign, default: auto-assign)")
	cmd.Flags().StringVar(&user, "user", "", "Default SSH user for cloud-init (default: from user config)")
	cmd.Flags().
		BoolVar(&noPCI, "no-pci", false, "Disable PCI transport (default: enabled). Required for hotplug support.")
	cmd.Flags().
		BoolVar(&nestedVirt, "nested-virt", false, "Enable nested virtualization (requires PCI, adds kvm-intel/amd.nested=1 boot arg)")
	cmd.Flags().BoolVar(&noNestedVirt, "no-nested-virt", false, "Disable nested virtualization")
	cmd.Flags().MarkHidden("no-nested-virt")
	cmd.MarkFlagsMutuallyExclusive("nested-virt", "no-nested-virt")
	cmd.Flags().
		StringVar(&cpuTemplate, "cpu-template", "", "Path to CPU template JSON file (merged with nested-virt config if both set)")
	cmd.Flags().BoolVar(&noConsole, "no-console", false, "Disable serial console")
	cmd.Flags().StringVar(&bootArgs, "boot-args", "", "Kernel boot arguments (default: from constants.py)")
	cmd.Flags().
		StringVar(&lsmFlags, "lsm-flags", "", "Linux Security Module flags for kernel cmdline (default: from user config)")
	cmd.Flags().
		BoolVar(&enableLogging, "enable-logging", false, "Enable Firecracker logging (default: from user config)")
	cmd.Flags().BoolVar(&noEnableLogging, "no-enable-logging", false, "Disable Firecracker logging")
	cmd.Flags().MarkHidden("no-enable-logging")
	cmd.MarkFlagsMutuallyExclusive("enable-logging", "no-enable-logging")
	cmd.Flags().
		BoolVar(&enableMetrics, "enable-metrics", false, "Enable Firecracker metrics (default: from user config)")
	cmd.Flags().BoolVar(&noEnableMetrics, "no-enable-metrics", false, "Disable Firecracker metrics")
	cmd.Flags().MarkHidden("no-enable-metrics")
	cmd.MarkFlagsMutuallyExclusive("enable-metrics", "no-enable-metrics")
	cmd.Flags().IntVarP(&count, "count", "c", 1, "Number of VMs to create (default: 1)")
	cmd.Flags().
		BoolVar(&atomic, "atomic", false, "If any VM fails, remove all successfully-created VMs (all-or-nothing)")
	cmd.Flags().
		BoolVar(&skipCleanup, "skip-cleanup", false, "Skip cleanup if VM creation fails; keeps cloud-init ISO and partial resources (for debugging)")
	cmd.Flags().
		BoolVar(&skipDeblob, "skip-deblob", false, "Skip debloat operations on rootfs (removes OS caches, cleans package manager caches)")
	cmd.Flags().StringArrayVarP(&volume, "volume", "v", nil, "Attach volume(s) to the VM (can specify multiple times)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompts")
	cmd.Flags().SetNormalizeFunc(func(f *pflag.FlagSet, name string) pflag.NormalizedName {
		switch name {
		case "cpus":
			name = "vcpus"
		case "memory":
			name = "mem"
		case "net":
			name = "network"
		}
		return pflag.NormalizedName(name)
	})

	return cmd
}

// ─── rm (remove) ─────────────────────────────────────────────────────────────

func newVMRemoveCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [identifiers...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more VMs.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVMRemove(op, cmd, args, force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force removal")
	return cmd
}

func runVMRemove(op *api.Operation, cmd *cobra.Command, identifiers []string, force bool) error {
	// Use batch API — pass all identifiers at once
	removeResult := op.VMRemove(cmd.Context(), inputs.VMInput{Identifiers: identifiers, Force: force})
	if removeResult.HasErrors() {
		for _, r := range removeResult.Items {
			if r.IsOK() {
				vm, ok := r.Item.(*model.VM)
				if ok && vm != nil {
					common.Cli.Success(fmt.Sprintf("Removed: %s", vm.Name))
				}
			} else {
				itemName := "unknown"
				if vm, ok := r.Item.(*model.VM); ok && vm != nil {
					itemName = vm.Name
				}
				msg := r.Message
				if msg == "" {
					msg = fmt.Sprintf("Remove failed: %s", itemName)
				}
				common.Cli.Error(msg)
			}
		}
		return fmt.Errorf("one or more removals failed")
	}
	names := make([]string, 0, len(removeResult.Items))
	for _, r := range removeResult.Items {
		if vm, ok := r.Item.(*model.VM); ok && vm != nil {
			names = append(names, vm.Name)
		}
	}
	common.Cli.Success(fmt.Sprintf("Removed: %s", strings.Join(names, ", ")))
	return nil
}

// ─── start ────────────────────────────────────────────────────────────────────

func newVMStartCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "start [identifiers...]",
		Short:             "Start one or more stopped VMs.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			startResult := op.VMStart(cmd.Context(), inputs.VMInput{Identifiers: args})
			if startResult.HasErrors() {
				for _, r := range startResult.Items {
					if !r.IsOK() {
						msg := r.Message
						if msg == "" {
							msg = "Start failed"
						}
						common.Cli.Error(msg)
					}
				}
				return fmt.Errorf("one or more starts failed")
			}
			common.Cli.Success(fmt.Sprintf("Started: %s", strings.Join(args, ", ")))
			return nil
		},
	}
}

// ─── stop ─────────────────────────────────────────────────────────────────────

func newVMStopCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "stop [identifiers...]",
		Short:             "Stop one or more running VMs.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			stopResult := op.VMStop(cmd.Context(), inputs.VMInput{Identifiers: args, Force: force})
			if stopResult.HasErrors() {
				for _, r := range stopResult.Items {
					if !r.IsOK() {
						msg := r.Message
						if msg == "" {
							msg = "Stop failed"
						}
						common.Cli.Error(msg)
					}
				}
				return fmt.Errorf("one or more stops failed")
			}
			common.Cli.Success(fmt.Sprintf("Stopped: %s", strings.Join(args, ", ")))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force stop")
	return cmd
}

// ─── reboot ───────────────────────────────────────────────────────────────────

func newVMRebootCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "reboot [identifiers...]",
		Short:             "Reboot one or more VMs.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			rebootResult := op.VMReboot(cmd.Context(), inputs.VMInput{Identifiers: args, Force: force})
			if rebootResult.HasErrors() {
				for _, r := range rebootResult.Items {
					if !r.IsOK() {
						msg := r.Message
						if msg == "" {
							msg = "Reboot failed"
						}
						common.Cli.Error(msg)
					}
				}
				return fmt.Errorf("one or more reboots failed")
			}
			common.Cli.Success(fmt.Sprintf("Rebooted: %s", strings.Join(args, ", ")))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force reboot")
	return cmd
}

// ─── pause ────────────────────────────────────────────────────────────────────

func newVMPauseCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "pause [identifiers...]",
		Short:             "Pause one or more running VMs.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			pauseResult := op.VMPause(cmd.Context(), inputs.VMInput{Identifiers: args})
			if pauseResult.HasErrors() {
				for _, r := range pauseResult.Items {
					if !r.IsOK() {
						msg := r.Message
						if msg == "" {
							msg = "Pause failed"
						}
						common.Cli.Error(msg)
					}
				}
				return fmt.Errorf("one or more pauses failed")
			}
			common.Cli.Success(fmt.Sprintf("Paused: %s", strings.Join(args, ", ")))
			return nil
		},
	}
}

// ─── resume ───────────────────────────────────────────────────────────────────

func newVMResumeCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "resume [identifiers...]",
		Short:             "Resume one or more paused VMs.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			resumeResult := op.VMResume(cmd.Context(), inputs.VMInput{Identifiers: args})
			if resumeResult.HasErrors() {
				for _, r := range resumeResult.Items {
					if !r.IsOK() {
						msg := r.Message
						if msg == "" {
							msg = "Resume failed"
						}
						common.Cli.Error(msg)
					}
				}
				return fmt.Errorf("one or more resumes failed")
			}
			common.Cli.Success(fmt.Sprintf("Resumed: %s", strings.Join(args, ", ")))
			return nil
		},
	}
}

// ─── snapshot ─────────────────────────────────────────────────────────────────

func newVMSnapshotCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "snapshot [id] [mem_file] [state_file]",
		Short:             "Snapshot VM memory and disk state.",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			memFile := args[1]
			stateFile := args[2]

			if err := op.VMSnapshot(
				cmd.Context(),
				inputs.VMInput{Identifiers: []string{id}},
				memFile,
				stateFile,
			); err != nil {
				return fmt.Errorf("snapshot failed: %w", err)
			}

			common.Cli.Success(fmt.Sprintf("Snapshot saved: %s", id))
			return nil
		},
	}

	return cmd
}

// ─── load (from snapshot) ─────────────────────────────────────────────────────

func newVMLoadCmd(op *api.Operation) *cobra.Command {
	var resume bool

	cmd := &cobra.Command{
		Use:   "load [id] [mem_file] [state_file]",
		Short: "Load VM from snapshot.",
		Long: `Load a VM from a snapshot.

Arguments:
  id          VM identifier (name, ID prefix, IP, or MAC)
  mem_file    Path to memory state file
  state_file  Path to VM state file

Flags:
  --resume    Resume VM after loading (default: leave paused)`,
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			memFile := args[1]
			stateFile := args[2]

			if err := op.VMLoad(
				cmd.Context(),
				inputs.VMInput{Identifiers: []string{id}},
				memFile,
				stateFile,
				resume,
			); err != nil {
				return err
			}

			// Match Python exactly: success message with no extra detail, no post-check.
			common.Cli.Success(fmt.Sprintf("Snapshot loaded: %s", id))
			return nil
		},
	}

	cmd.Flags().BoolVar(&resume, "resume", false, "Resume VM after loading")
	return cmd
}

// ─── inspect ──────────────────────────────────────────────────────────────────

func newVMInspectCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [id]",
		Short:             "Show detailed information about a VM.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			info, err := op.VMInspect(cmd.Context(), inputs.VMInput{Identifiers: []string{id}})
			if err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(info, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			vmName := info.VM.Name
			if vmName == "" {
				vmName = id
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(info), fmt.Sprintf("VM: %s", vmName))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

// ─── export ───────────────────────────────────────────────────────────────────

func newVMExportCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:   "export [id] [output]",
		Short: "Export a VM's configuration to a portable JSON file.",
		Long: `Export a VM's configuration to a portable JSON file.

The exported config uses semantic references (type, version, name)
instead of internal IDs, making it portable across machines.`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			outputPath := ""
			if len(args) > 1 {
				outputPath = args[1]
			}

			exportConfig, err := op.VMExport(cmd.Context(), inputs.VMInput{Identifiers: []string{id}})
			if err != nil {
				return fmt.Errorf("export failed: %w", err)
			}

			jsonBytes, _ := json.MarshalIndent(exportConfig, "", "  ")

			if outputPath != "" {
				if err := os.WriteFile(outputPath, jsonBytes, 0644); err != nil {
					return err
				}
				common.Cli.Success(fmt.Sprintf("Exported: %s", outputPath))
			} else {
				fmt.Println(string(jsonBytes))
			}
			return nil
		},
	}
}

// ─── import ───────────────────────────────────────────────────────────────────

func newVMImportCmd(op *api.Operation) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "import [config_path]",
		Short: "Create a VM from a portable config file.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var nameOverride *string
			if cmd.Flags().Changed("name") && name != "" {
				nameOverride = infraptr.Ptr(name)
			}
			if err := op.VMImport(
				cmd.Context(),
				inputs.VMImportInput{ConfigPath: args[0], NameOverride: nameOverride},
				nil,
			); err != nil {
				if errs.IsNeedsInteraction(err) {
					return fmt.Errorf("import requires privileges")
				}
				return fmt.Errorf("import failed: %w", err)
			}
			common.Cli.Success(fmt.Sprintf("VM imported from %s", args[0]))
			return nil
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Override VM name from config")
	return cmd
}

// ─── attach-volume ────────────────────────────────────────────────────────────

func newVMAttachVolumeCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:   "attach-volume [id] [volume_name]",
		Short: "Attach a volume to a running VM.",
		Long: `Attach a volume to a running VM via Firecracker drive hotplug.

Arguments:
  id           VM identifier (name, ID prefix, IP, or MAC)
  volume_name  Name or ID of the volume to attach`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			volumeName := args[1]

			if err := op.VMAttachVolume(
				cmd.Context(),
				inputs.VMInput{Identifiers: []string{id}},
				volumeName,
			); err != nil {
				return fmt.Errorf("attach volume %q: %w", volumeName, err)
			}

			common.Cli.Success(fmt.Sprintf("Volume '%s' attached", volumeName))
			return nil
		},
	}
}

// ─── detach-volume ────────────────────────────────────────────────────────────

func newVMDetachVolumeCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:   "detach-volume [id] [volume_name]",
		Short: "Detach a volume from a running VM.",
		Long: `Detach a volume from a running VM.

Arguments:
  id           VM identifier (name, ID prefix, IP, or MAC)
  volume_name  Name or ID of the volume to detach`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			volumeName := args[1]

			if err := op.VMDetachVolume(
				cmd.Context(),
				inputs.VMInput{Identifiers: []string{id}},
				volumeName,
			); err != nil {
				return fmt.Errorf("detach volume %q: %w", volumeName, err)
			}

			common.Cli.Success(fmt.Sprintf("Volume '%s' detached", volumeName))
			return nil
		},
	}
}
