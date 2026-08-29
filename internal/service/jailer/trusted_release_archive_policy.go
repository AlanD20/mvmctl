package jailer

const (
	trustedReleaseArchiveMaxDecompressedBytes = uint64(32 * 1024 * 1024)
	trustedReleaseArchiveMaxMemberBytes       = uint64(8 * 1024 * 1024)
	trustedReleaseArchiveMemberCount          = 24
	trustedReleaseArchiveMaxPAXBytes          = 128
)

type trustedReleaseArchiveSelection uint8

const (
	trustedReleaseArchiveNotSelected trustedReleaseArchiveSelection = iota
	trustedReleaseArchiveFirecracker
	trustedReleaseArchiveJailer
)

type trustedReleaseArchiveMemberPolicy struct {
	mode     uint64
	selected trustedReleaseArchiveSelection
}

type trustedReleaseArchivePolicy struct {
	source  trustedReleaseSource
	members map[string]trustedReleaseArchiveMemberPolicy
}

// CRITICAL: Archive layout is release authority, not a compatibility guess. Only versions whose complete x86_64
// member sets were audited for ADR-0016 are admitted. aarch64 source and ELF support do not authorize its unaudited
// archive packaging.
func newTrustedReleaseArchivePolicy(source trustedReleaseSource) (trustedReleaseArchivePolicy, error) {
	if err := validateTrustedReleaseSource(source); err != nil {
		return trustedReleaseArchivePolicy{}, err
	}
	if source.slot.architecture != "x86_64" {
		return trustedReleaseArchivePolicy{}, trustedReleaseStoreUntrusted(
			"trusted release archive architecture has not been audited",
			nil,
		)
	}

	var cpuTemplates []string
	switch source.slot.version {
	case "1.10.1":
		cpuTemplates = []string{"c3", "t2a", "t2", "t2s", "v1n1", "t2cl"}
	case "1.14.2", "1.14.3", "1.14.4", "1.15.0", "1.15.1", "1.16.0", "1.16.1":
		cpuTemplates = []string{"C3", "T2A", "T2", "T2S", "V1N1", "T2CL"}
	default:
		return trustedReleaseArchivePolicy{}, trustedReleaseStoreUntrusted(
			"trusted release archive version has not been audited",
			nil,
		)
	}

	root := source.archiveRoot + "/"
	binarySuffix := "-v" + source.slot.version + "-x86_64"
	members := make(map[string]trustedReleaseArchiveMemberPolicy, trustedReleaseArchiveMemberCount)
	addTrustedReleaseArchiveMember := func(
		name string,
		mode uint64,
		selected trustedReleaseArchiveSelection,
	) {
		members[root+name] = trustedReleaseArchiveMemberPolicy{mode: mode, selected: selected}
	}

	addTrustedReleaseArchiveMember("SHA256SUMS", 0644, trustedReleaseArchiveNotSelected)
	addTrustedReleaseArchiveMember("LICENSE", 0644, trustedReleaseArchiveNotSelected)
	addTrustedReleaseArchiveMember("THIRD-PARTY", 0644, trustedReleaseArchiveNotSelected)
	addTrustedReleaseArchiveMember("NOTICE", 0644, trustedReleaseArchiveNotSelected)
	addTrustedReleaseArchiveMember(
		"firecracker_spec-v"+source.slot.version+".yaml",
		0644,
		trustedReleaseArchiveNotSelected,
	)
	addTrustedReleaseArchiveMember(
		"seccomp-filter-v"+source.slot.version+"-x86_64.json",
		0644,
		trustedReleaseArchiveNotSelected,
	)
	addTrustedReleaseArchiveToolMembers := func(name string, selected trustedReleaseArchiveSelection) {
		addTrustedReleaseArchiveMember(name+binarySuffix, 0755, selected)
		addTrustedReleaseArchiveMember(name+binarySuffix+".debug", 0644, trustedReleaseArchiveNotSelected)
	}
	addTrustedReleaseArchiveToolMembers("firecracker", trustedReleaseArchiveFirecracker)
	addTrustedReleaseArchiveToolMembers("jailer", trustedReleaseArchiveJailer)
	addTrustedReleaseArchiveToolMembers("cpu-template-helper", trustedReleaseArchiveNotSelected)
	addTrustedReleaseArchiveToolMembers("rebase-snap", trustedReleaseArchiveNotSelected)
	addTrustedReleaseArchiveToolMembers("seccompiler-bin", trustedReleaseArchiveNotSelected)
	addTrustedReleaseArchiveToolMembers("snapshot-editor", trustedReleaseArchiveNotSelected)
	for _, template := range cpuTemplates {
		addTrustedReleaseArchiveMember(
			template+"-v"+source.slot.version+".json",
			0644,
			trustedReleaseArchiveNotSelected,
		)
	}

	if len(members) != trustedReleaseArchiveMemberCount {
		return trustedReleaseArchivePolicy{}, trustedReleaseStoreError(
			"trusted release archive policy has an unexpected member count",
			nil,
		)
	}
	return trustedReleaseArchivePolicy{source: source, members: members}, nil
}
