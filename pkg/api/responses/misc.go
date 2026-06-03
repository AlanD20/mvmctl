package responses

// CacheInitResult is the typed result of CacheInitAll.
type CacheInitResult struct {
	CacheDir         string   `json:"cache_dir"`
	Directories      []string `json:"directories"`
	GuestfsAppliance string   `json:"guestfs_appliance"`
	GuestfsKernel    string   `json:"guestfs_kernel"`
}

// ConsoleStateResult is the typed result of ConsoleGetState.
type ConsoleStateResult struct {
	Running    bool   `json:"running"`
	PID        *int   `json:"pid"`
	SocketPath string `json:"socket_path"`
}

// CPCopyResult is the typed result of CPCopy.
type CPCopyResult struct {
	Bytes   int64  `json:"bytes"`
	Message string `json:"message"`
}
