package validators

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// --- Helpers ---

func assertCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	var de *errs.DomainError
	if errors.As(err, &de) {
		assert.Equal(t, code, de.Code)
	} else if err != nil {
		t.Errorf("expected *errs.DomainError, got %T", err)
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if de, ok := err.(*errs.DomainError); ok {
		t.Fatalf("unexpected DomainError: %s (code=%s)", de.Message, de.Code)
	}
	assert.NoError(t, err)
}

// --- EntityName ---
// Rationale: Must validate length, reserved names, dangerous chars, prefix, and regex.

func TestEntityName(t *testing.T) {
	t.Run("valid_name", func(t *testing.T) {
		assertNoError(t, EntityName("my-vm", "VM", 63))
	})

	t.Run("empty_errors", func(t *testing.T) {
		err := EntityName("", "VM", 63)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be empty")
	})

	t.Run("too_long_errors", func(t *testing.T) {
		err := EntityName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "VM", 63)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum length")
	})

	t.Run("starts_with_hyphen_errors", func(t *testing.T) {
		err := EntityName("-bad", "VM", 63)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot start with a hyphen")
	})

	t.Run("reserved_name_errors", func(t *testing.T) {
		err := EntityName("all", "VM", 63)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved name")
	})

	t.Run("ip_address_errors_with_clear_message", func(t *testing.T) {
		// IsIPAddress runs before ContainsDangerousChars, so dotted IPs
		// get a clear "cannot be an IP address" error.
		err := EntityName("10.0.0.1", "VM", 63)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be an IP address")
	})

	t.Run("invalid_chars_errors", func(t *testing.T) {
		err := EntityName("bad name!", "VM", 63)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must match")
	})

	t.Run("uppercase_errors", func(t *testing.T) {
		err := EntityName("UpperCase", "VM", 63)
		require.Error(t, err)
	})
}

// --- KeyName / VolumeName / VMName ---
// Rationale: Thin wrappers around EntityName with different defaults.

func TestKeyName(t *testing.T) {
	assertNoError(t, KeyName("my-key"))
	assert.Error(t, KeyName(""))
}

func TestVolumeName(t *testing.T) {
	assertNoError(t, VolumeName("my-volume"))
	assert.Error(t, VolumeName(""))
}

func TestVMName(t *testing.T) {
	assertNoError(t, VMName("my-vm"))
	assert.Error(t, VMName(""))
}

// --- NetworkName ---
// Rationale: EntityName + no dots + no reserved interfaces + no CLI_NAME- prefix.

func TestNetworkName(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		assertNoError(t, NetworkName("my-network"))
	})

	t.Run("dots_caught_by_entity_name_dangerous_chars", func(t *testing.T) {
		// Dots are in DangerousChars, so EntityName fails before NetworkName's dot check.
		err := NetworkName("my.network")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forbidden characters")
	})

	t.Run("reserved_interface_errors", func(t *testing.T) {
		err := NetworkName("eth0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved interface name")
	})

	t.Run("cliname_prefix_errors", func(t *testing.T) {
		err := NetworkName("mvm-test")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot start with 'mvm-'")
	})
}

// --- IsIPAddress ---
func TestIsIPAddress(t *testing.T) {
	assert.True(t, IsIPAddress("10.0.0.1"))
	assert.True(t, IsIPAddress("192.168.1.1"))
	assert.True(t, IsIPAddress("::1"))
	assert.False(t, IsIPAddress("not-an-ip"))
	assert.False(t, IsIPAddress(""))
}

// --- IPv4Address ---
// Rationale: Validates format, private range, subnet containment, network addr
// and gateway exclusion.

func TestIPv4Address(t *testing.T) {
	t.Run("valid_private", func(t *testing.T) {
		assertNoError(t, IPv4Address("10.0.0.5", "IP", false, "", ""))
	})

	t.Run("valid_public_no_require", func(t *testing.T) {
		assertNoError(t, IPv4Address("8.8.8.8", "IP", false, "", ""))
	})

	t.Run("empty_errors", func(t *testing.T) {
		err := IPv4Address("", "IP", false, "", "")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("spaces_errors", func(t *testing.T) {
		err := IPv4Address("10.0.0. 1", "IP", false, "", "")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("not_ipv4_errors", func(t *testing.T) {
		err := IPv4Address("::1", "IP", false, "", "")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("public_required_private_errors", func(t *testing.T) {
		err := IPv4Address("8.8.8.8", "IP", true, "", "")
		assertCode(t, err, errs.CodeValidationFailed)
		assert.Contains(t, err.Error(), "must be a private")
	})

	t.Run("within_subnet", func(t *testing.T) {
		assertNoError(t, IPv4Address("10.0.0.5", "IP", false, "10.0.0.0/24", ""))
	})

	t.Run("outside_subnet_errors", func(t *testing.T) {
		err := IPv4Address("10.0.1.5", "IP", false, "10.0.0.0/24", "")
		assertCode(t, err, errs.CodeValidationFailed)
		assert.Contains(t, err.Error(), "not within subnet")
	})

	t.Run("network_address_errors", func(t *testing.T) {
		err := IPv4Address("10.0.0.0", "IP", false, "10.0.0.0/24", "")
		assertCode(t, err, errs.CodeValidationFailed)
		assert.Contains(t, err.Error(), "network address")
	})

	t.Run("gateway_errors", func(t *testing.T) {
		err := IPv4Address("10.0.0.1", "IP", false, "10.0.0.0/24", "10.0.0.1")
		assertCode(t, err, errs.CodeValidationFailed)
		assert.Contains(t, err.Error(), "gateway address")
	})

	t.Run("non_gateway_ok", func(t *testing.T) {
		assertNoError(t, IPv4Address("10.0.0.5", "IP", false, "10.0.0.0/24", "10.0.0.1"))
	})
}

// --- IPv4Gateway ---
// Rationale: Validates gateway is private, within subnet, not network address.

func TestIPv4Gateway(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		got, err := IPv4Gateway("10.0.0.1", "10.0.0.0/24")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.1", got)
	})

	t.Run("empty_errors", func(t *testing.T) {
		_, err := IPv4Gateway("", "10.0.0.0/24")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("spaces_errors", func(t *testing.T) {
		_, err := IPv4Gateway("10.0.0. 1", "10.0.0.0/24")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("public_errors", func(t *testing.T) {
		_, err := IPv4Gateway("8.8.8.8", "10.0.0.0/24")
		assertCode(t, err, errs.CodeValidationFailed)
		assert.Contains(t, err.Error(), "private")
	})

	t.Run("outside_subnet_errors", func(t *testing.T) {
		_, err := IPv4Gateway("10.0.1.1", "10.0.0.0/24")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("network_addr_errors", func(t *testing.T) {
		_, err := IPv4Gateway("10.0.0.0", "10.0.0.0/24")
		assertCode(t, err, errs.CodeValidationFailed)
		assert.Contains(t, err.Error(), "network address")
	})

	t.Run("returns_normalized", func(t *testing.T) {
		got, err := IPv4Gateway("10.0.0.1", "10.0.0.0/24")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.1", got)
	})
}

// --- MAC ---
func TestMAC(t *testing.T) {
	assertNoError(t, MAC("02:FC:00:00:00:01"))
	assertNoError(t, MAC("aa:bb:cc:dd:ee:ff"))
	assertNoError(t, MAC("AA:BB:CC:DD:EE:FF"))

	err := MAC("not-a-mac")
	assertCode(t, err, errs.CodeValidationFailed)

	err = MAC("02:FC:00:00:00") // too short
	assertCode(t, err, errs.CodeValidationFailed)

	err = MAC("")
	assertCode(t, err, errs.CodeValidationFailed)
}

// --- Subnet ---
func TestSubnet(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		got, err := Subnet("10.0.0.0/24")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.0/24", got)
	})

	t.Run("strips_host_bits", func(t *testing.T) {
		got, err := Subnet("10.0.0.5/24")
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.0/24", got)
	})

	t.Run("empty_errors", func(t *testing.T) {
		_, err := Subnet("")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("spaces_errors", func(t *testing.T) {
		_, err := Subnet("10.0.0.0/24 ")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("invalid_cidr_errors", func(t *testing.T) {
		_, err := Subnet("not-a-cidr")
		assertCode(t, err, errs.CodeValidationFailed)
	})

	t.Run("ipv6_errors", func(t *testing.T) {
		_, err := Subnet("fe80::/64")
		assertCode(t, err, errs.CodeValidationFailed)
	})
}

// --- SubnetNoOverlap ---
// Rationale: Must detect CIDR overlaps and reject host bits in strict mode.

func TestSubnetNoOverlap(t *testing.T) {
	t.Run("no_overlap", func(t *testing.T) {
		assertNoError(t, SubnetNoOverlap("10.0.1.0/24", []string{"10.0.0.0/24"}))
	})

	t.Run("overlap_detected", func(t *testing.T) {
		err := SubnetNoOverlap("10.0.0.0/16", []string{"10.0.1.0/24"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overlaps")
	})

	t.Run("exact_same", func(t *testing.T) {
		err := SubnetNoOverlap("10.0.0.0/24", []string{"10.0.0.0/24"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overlaps")
	})

	t.Run("empty_existing", func(t *testing.T) {
		assertNoError(t, SubnetNoOverlap("10.0.0.0/24", []string{}))
	})

	t.Run("host_bits_set", func(t *testing.T) {
		err := SubnetNoOverlap("10.0.0.5/24", []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "host bits set")
	})

	t.Run("invalid_cidr_errors", func(t *testing.T) {
		err := SubnetNoOverlap("bad", []string{})
		assertCode(t, err, errs.CodeValidationFailed)
	})
}

// --- BootArgComponent ---
func TestBootArgComponent(t *testing.T) {
	assertNoError(t, BootArgComponent("hello", "arg"))
	assertNoError(t, BootArgComponent("", "arg")) // empty is ok

	err := BootArgComponent("hello world", "arg")
	assertCode(t, err, errs.CodeValidationFailed)

	err = BootArgComponent("hello;world", "arg")
	assertCode(t, err, errs.CodeValidationFailed)

	err = BootArgComponent("hello|world", "arg")
	assertCode(t, err, errs.CodeValidationFailed)
}

// --- SSHUsername ---
func TestSSHUsername(t *testing.T) {
	assertNoError(t, SSHUsername("user"))
	assertNoError(t, SSHUsername("root"))
	assertNoError(t, SSHUsername("my_user"))
	assertNoError(t, SSHUsername("_underscore"))

	err := SSHUsername("User") // uppercase
	assertCode(t, err, errs.CodeValidationFailed)

	err = SSHUsername("user name") // space
	assertCode(t, err, errs.CodeValidationFailed)

	err = SSHUsername("") // empty
	assertCode(t, err, errs.CodeValidationFailed)
}

// --- BootArgs ---
func TestBootArgs(t *testing.T) {
	t.Run("missing_root_uuid_and_guest_ip", func(t *testing.T) {
		errs := BootArgs("", "", "")
		assert.Len(t, errs, 2) // root UUID required + guest IP required
	})

	t.Run("valid_boot_args_with_bad_value", func(t *testing.T) {
		errs := BootArgs("console=ttyS0 reboot=k", "valid-uuid-1234", "10.0.0.2")
		assert.Empty(t, errs)
	})

	t.Run("bad_boot_arg_value", func(t *testing.T) {
		errs := BootArgs("console=ttyS0 bad=val;ue", "valid-uuid", "10.0.0.2")
		assert.Len(t, errs, 1)
		assert.Contains(t, errs[0], "must not contain spaces or shell metacharacters")
	})

	t.Run("bad_root_uuid_format", func(t *testing.T) {
		errs := BootArgs("root_uuid=abc", "not-a-uuid", "10.0.0.2")
		assert.NotEmpty(t, errs)
	})
}

// --- ParsePortRange ---
func TestParsePortRange(t *testing.T) {
	got := ParsePortRange("32768,60999")
	assert.Equal(t, [2]int{32768, 60999}, got)

	got = ParsePortRange("invalid")
	assert.Equal(t, infra.DefaultIPLocalPortRange, got)

	got = ParsePortRange("")
	assert.Equal(t, infra.DefaultIPLocalPortRange, got)

	got = ParsePortRange("32768") // only one number
	assert.Equal(t, infra.DefaultIPLocalPortRange, got)
}

// --- IsDigits ---
func TestIsDigits(t *testing.T) {
	assert.True(t, IsDigits("12345"))
	assert.True(t, IsDigits("0"))
	assert.False(t, IsDigits(""))
	assert.False(t, IsDigits("12a34"))
	assert.False(t, IsDigits("12 34"))
}

// --- networkRange / ipCmp / cidrsOverlap ---
// Rationale: Internal helpers for subnet overlap detection. Must correctly
// compute range boundaries and compare CIDR pairs.

func TestNetworkRange(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("10.0.0.0/24")
	first, last := networkRange(ipnet)
	require.NotNil(t, first)
	require.NotNil(t, last)
	assert.Equal(t, "10.0.0.0", first.String())
	assert.Equal(t, "10.0.0.255", last.String())
}

func TestIPCmp(t *testing.T) {
	a := net.ParseIP("10.0.0.1").To4()
	b := net.ParseIP("10.0.0.2").To4()
	c := net.ParseIP("10.0.0.1").To4()
	assert.Equal(t, -1, ipCmp(a, b))
	assert.Equal(t, 1, ipCmp(b, a))
	assert.Equal(t, 0, ipCmp(a, c))
}

func TestCidrsOverlap(t *testing.T) {
	_, a, _ := net.ParseCIDR("10.0.0.0/24")
	_, b, _ := net.ParseCIDR("10.0.0.0/16")
	_, c, _ := net.ParseCIDR("10.1.0.0/24")

	assert.True(t, cidrsOverlap(a, b), "a within b overlaps")
	assert.True(t, cidrsOverlap(b, a), "b containing a overlaps")
	assert.False(t, cidrsOverlap(a, c), "non-overlapping subnets")
}
