package firewall

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	testNetfilterSubsystemType = unix.NFNL_SUBSYS_NFTABLES << 8
	testNFTAListElem           = uint16(unix.NFTA_LIST_ELEM)
	testNFTATableHandle        = uint16(4)
	testNFTAChainFlags         = uint16(10)
	testNFTChainBase           = uint32(1)
	testNFTBaseSequence        = uint16(0x1234)
	testKernelGenerationID     = "55003fdd06928dc2be2e8af48d62bd2f179f0f5add30987dc9b45c261a54d65c"
)

// Rationale: A hand-built dump fixture is independent of google/nftables marshaling and locks the Linux reply shape
// that the strict decoder must accept before its canonical observation is compared with compiler output.
func TestDecodeKernelObservationAcceptsExactCompilerMessages(t *testing.T) {
	raw, want := capturedKernelObservationFixture(t)

	observed, err := decodeKernelObservation(raw)
	require.NoError(t, err)
	assertKernelObservationEqual(t, want, observed)

	compiled, err := compileGeneration(kernelFixtureIntent())
	require.NoError(t, err)
	require.Equal(t, testKernelGenerationID, compiled.id)
	assert.NoError(t, compareKernelObservation(observed, compiled))
}

// Rationale: Linux dumps all rules for one declared chain before advancing to the next. With two masqueraded networks,
// compiler insertion interleaves input and postrouting rules, so comparison must canonicalize compiler order by chain.
func TestDecodeKernelObservationComparesTwoNetworkChainOrderedDump(t *testing.T) {
	intent := compatibilityIntent()
	compiled, err := compileGeneration(generationIntent{networks: slices.Clone(intent.networks)})
	require.NoError(t, err)
	want := kernelDumpObservationFromCompiled(compiled)
	raw := kernelMessagesFromObservation(t, want)

	observed, err := decodeKernelObservation(raw)
	require.NoError(t, err)
	assertKernelObservationEqual(t, want, observed)
	assert.NoError(t, compareKernelObservation(observed, compiled))
}

func TestDecodeKernelObservationAcceptsExactEmptyMessages(t *testing.T) {
	compiled, err := compileGeneration(generationIntent{})
	require.NoError(t, err)

	observed, err := decodeKernelObservation(kernelObservationMessages{})
	require.NoError(t, err)

	assert.NoError(t, compareKernelObservation(observed, compiled))
}

// Rationale: Kernel replies are untrusted observations; every side-object and allocation bound must fail closed before
// any canonical comparison can accidentally admit a partial generation.
func TestDecodeKernelObservationRejectsUnexpectedObjectsAndBounds(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)

	tests := map[string]func(*kernelObservationMessages){
		"set exists": func(raw *kernelObservationMessages) {
			raw.setReplies = []netlink.Message{{}}
		},
		"object exists": func(raw *kernelObservationMessages) {
			raw.objectReplies = []netlink.Message{{}}
		},
		"flowtable exists": func(raw *kernelObservationMessages) {
			raw.flowtableReplies = []netlink.Message{{}}
		},
		"message count exceeds limit": func(raw *kernelObservationMessages) {
			raw.ruleReplies = make([]netlink.Message, maxKernelObservationMessages+1)
		},
		"message bytes exceed limit": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Data = make([]byte, maxKernelObservationMessageBytes+1)
			raw.tableReplies[0].Header.Length = uint32(netlinkHeaderBytes + len(raw.tableReplies[0].Data))
		},
		"rule objects exceed limit": func(raw *kernelObservationMessages) {
			raw.ruleReplies = make([]netlink.Message, maxKernelObservationRules+1)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)

			_, err := decodeKernelObservation(raw)
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: Exact parent lookups and table-scoped dumps are authoritative only when every envelope and object remains
// in the exact kernel order and family expected by the closed managed-table grammar.
func TestDecodeKernelObservationRejectsMessageAndObjectIdentityChanges(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)
	compiled, err := compileGeneration(kernelFixtureIntent())
	require.NoError(t, err)

	tests := map[string]func(*kernelObservationMessages){
		"table response type": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Header.Type++
		},
		"chain response type": func(raw *kernelObservationMessages) {
			raw.chainReplies[0].Header.Type++
		},
		"rule response type": func(raw *kernelObservationMessages) {
			raw.ruleReplies[0].Header.Type++
		},
		"rule response flags": func(raw *kernelObservationMessages) {
			raw.ruleReplies[0].Header.Flags = netlink.Multi
		},
		"table response flags": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Header.Flags = netlink.Multi
		},
		"chain response flags": func(raw *kernelObservationMessages) {
			raw.chainReplies[0].Header.Flags = netlink.Multi
		},
		"header length": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Header.Length++
		},
		"truncated nfgen header": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Data = raw.tableReplies[0].Data[:netfilterHeaderBytes-1]
			raw.tableReplies[0].Header.Length = uint32(netlinkHeaderBytes + len(raw.tableReplies[0].Data))
		},
		"netfilter version": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Data[1] = 1
		},
		"netfilter resource ID": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Data[3] = 1
		},
		"table family": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Data[0] = unix.NFPROTO_IPV4
		},
		"chain family": func(raw *kernelObservationMessages) {
			raw.chainReplies[0].Data[0] = unix.NFPROTO_IPV4
		},
		"rule family": func(raw *kernelObservationMessages) {
			raw.ruleReplies[0].Data[0] = unix.NFPROTO_IPV4
		},
		"chain table": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[0], unix.NFTA_CHAIN_TABLE, []byte("foreign\x00"))
		},
		"rule table": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_TABLE, []byte("foreign\x00"))
		},
		"table order": func(raw *kernelObservationMessages) {
			raw.tableReplies[0], raw.tableReplies[1] = raw.tableReplies[1], raw.tableReplies[0]
		},
		"chain order": func(raw *kernelObservationMessages) {
			raw.chainReplies[0], raw.chainReplies[1] = raw.chainReplies[1], raw.chainReplies[0]
		},
		"cross-table chain regression": func(raw *kernelObservationMessages) {
			raw.chainReplies[1], raw.chainReplies[2] = raw.chainReplies[2], raw.chainReplies[1]
		},
		"rule order": func(raw *kernelObservationMessages) {
			raw.ruleReplies[0], raw.ruleReplies[1] = raw.ruleReplies[1], raw.ruleReplies[0]
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)

			observed, err := decodeKernelObservation(raw)
			if err == nil {
				err = compareKernelObservation(observed, compiled)
			}
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: Attribute duplication or extension must never be silently collapsed by map decoding at any managed
// top-level object boundary.
func TestDecodeKernelObservationRejectsDuplicateAndUnknownTopLevelAttributes(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)

	tests := map[string]struct {
		messages func(*kernelObservationMessages) *[]netlink.Message
		index    int
		attrType uint16
	}{
		"duplicate table name": {
			messages: func(raw *kernelObservationMessages) *[]netlink.Message { return &raw.tableReplies },
			attrType: unix.NFTA_TABLE_NAME,
		},
		"unknown table attribute": {
			messages: func(raw *kernelObservationMessages) *[]netlink.Message { return &raw.tableReplies },
			attrType: 63,
		},
		"duplicate chain name": {
			messages: func(raw *kernelObservationMessages) *[]netlink.Message { return &raw.chainReplies },
			attrType: unix.NFTA_CHAIN_NAME,
		},
		"unknown chain attribute": {
			messages: func(raw *kernelObservationMessages) *[]netlink.Message { return &raw.chainReplies },
			attrType: 63,
		},
		"duplicate rule userdata": {
			messages: func(raw *kernelObservationMessages) *[]netlink.Message { return &raw.ruleReplies },
			attrType: unix.NFTA_RULE_USERDATA,
		},
		"unknown rule attribute": {
			messages: func(raw *kernelObservationMessages) *[]netlink.Message { return &raw.ruleReplies },
			attrType: 63,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			messages := tc.messages(&raw)
			appendMessageAttribute(t, &(*messages)[tc.index], tc.attrType)

			_, err := decodeKernelObservation(raw)
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: Required attributes and their exact widths/flags are part of the dump grammar; testing all three managed
// object levels prevents a permissive decoder from admitting a differently encoded authority object.
func TestDecodeKernelObservationRejectsMissingFlaggedAndWrongWidthAttributes(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)
	tests := map[string]func(*kernelObservationMessages){
		"missing table name": func(raw *kernelObservationMessages) {
			removeMessageAttribute(t, &raw.tableReplies[0], unix.NFTA_TABLE_NAME)
		},
		"flagged table name": func(raw *kernelObservationMessages) {
			setMessageAttributeFlags(t, &raw.tableReplies[0], unix.NFTA_TABLE_NAME, netlink.NetByteOrder)
		},
		"wrong table scalar width": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.tableReplies[0], unix.NFTA_TABLE_FLAGS, make([]byte, 3))
		},
		"missing chain name": func(raw *kernelObservationMessages) {
			removeMessageAttribute(t, &raw.chainReplies[0], unix.NFTA_CHAIN_NAME)
		},
		"flagged chain hook": func(raw *kernelObservationMessages) {
			setMessageAttributeFlags(t, &raw.chainReplies[0], unix.NFTA_CHAIN_HOOK, netlink.Nested)
		},
		"wrong chain scalar width": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[0], unix.NFTA_CHAIN_USE, make([]byte, 3))
		},
		"missing rule userdata": func(raw *kernelObservationMessages) {
			removeMessageAttribute(t, &raw.ruleReplies[0], unix.NFTA_RULE_USERDATA)
		},
		"flagged rule expressions": func(raw *kernelObservationMessages) {
			setMessageAttributeFlags(t, &raw.ruleReplies[0], unix.NFTA_RULE_EXPRESSIONS, netlink.Nested)
		},
		"wrong rule scalar width": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_HANDLE, make([]byte, 7))
		},
		"missing hook priority": func(raw *kernelObservationMessages) {
			mutateNestedAttribute(t, &raw.chainReplies[0], unix.NFTA_CHAIN_HOOK,
				func(attrs []netlink.Attribute) []netlink.Attribute {
					return slices.DeleteFunc(attrs, func(attr netlink.Attribute) bool {
						return testAttributeType(attr) == unix.NFTA_HOOK_PRIORITY
					})
				})
		},
		"flagged hook number": func(raw *kernelObservationMessages) {
			mutateNestedAttribute(t, &raw.chainReplies[0], unix.NFTA_CHAIN_HOOK,
				func(attrs []netlink.Attribute) []netlink.Attribute {
					for index := range attrs {
						if testAttributeType(attrs[index]) == unix.NFTA_HOOK_HOOKNUM {
							attrs[index].Type |= netlink.NetByteOrder
						}
					}
					return attrs
				})
		},
		"wrong hook width": func(raw *kernelObservationMessages) {
			mutateNestedAttribute(t, &raw.chainReplies[0], unix.NFTA_CHAIN_HOOK,
				func(attrs []netlink.Attribute) []netlink.Attribute {
					for index := range attrs {
						if testAttributeType(attrs[index]) == unix.NFTA_HOOK_HOOKNUM {
							attrs[index].Data = make([]byte, 3)
						}
					}
					return attrs
				})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)
			_, err := decodeKernelObservation(raw)
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: Unknown expression grammar would create an unaudited firewall semantic, so all nested ambiguity must be
// rejected rather than delegated to google/nftables' permissive decoder.
func TestDecodeKernelObservationRejectsNestedAndExpressionAmbiguity(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)

	tests := map[string]func(*kernelObservationMessages){
		"duplicate hook number": func(raw *kernelObservationMessages) {
			mutateNestedAttribute(
				t,
				&raw.chainReplies[0],
				unix.NFTA_CHAIN_HOOK,
				func(attrs []netlink.Attribute) []netlink.Attribute {
					return append(attrs, attrs[0])
				},
			)
		},
		"unknown hook attribute": func(raw *kernelObservationMessages) {
			mutateNestedAttribute(
				t,
				&raw.chainReplies[0],
				unix.NFTA_CHAIN_HOOK,
				func(attrs []netlink.Attribute) []netlink.Attribute {
					return append(attrs, netlink.Attribute{Type: 63, Data: []byte{0, 0, 0, 0}})
				},
			)
		},
		"unknown expression name": func(raw *kernelObservationMessages) {
			mutateFirstExpression(t, &raw.ruleReplies[0], func(attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					if testAttributeType(attrs[index]) == unix.NFTA_EXPR_NAME {
						attrs[index].Data = []byte("foreign\x00")
					}
				}
				return attrs
			})
		},
		"duplicate expression name": func(raw *kernelObservationMessages) {
			mutateFirstExpression(t, &raw.ruleReplies[0], func(attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, attrs[0])
			})
		},
		"unknown expression attribute": func(raw *kernelObservationMessages) {
			mutateFirstExpression(t, &raw.ruleReplies[0], func(attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, netlink.Attribute{Type: 63})
			})
		},
		"unknown expression data attribute": func(raw *kernelObservationMessages) {
			mutateFirstExpression(t, &raw.ruleReplies[0], func(attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					if testAttributeType(attrs[index]) != unix.NFTA_EXPR_DATA {
						continue
					}
					dataAttrs, decodeErr := netlink.UnmarshalAttributes(attrs[index].Data)
					require.NoError(t, decodeErr)
					dataAttrs = append(dataAttrs, netlink.Attribute{Type: 63, Data: []byte{0, 0, 0, 0}})
					attrs[index].Data = mustMarshalKernelAttributes(t, dataAttrs)
				}
				return attrs
			})
		},
		"expression count exceeds limit": func(raw *kernelObservationMessages) {
			mutateNestedAttribute(t, &raw.ruleReplies[0], unix.NFTA_RULE_EXPRESSIONS,
				func(attrs []netlink.Attribute) []netlink.Attribute {
					return slices.Repeat(attrs[:1], maxKernelObservationExpressionsPerRule+1)
				})
		},
		"empty expression list": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_EXPRESSIONS, nil)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)

			_, err := decodeKernelObservation(raw)
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: nft_base_seq may legitimately be zero and PAD attributes may be absent; accepting both variants avoids
// turning kernel serialization details into false generation mismatches.
func TestDecodeKernelObservationAcceptsZeroSequenceAndAbsentPadding(t *testing.T) {
	raw, want := capturedKernelObservationFixture(t)
	groups := []struct {
		messages []netlink.Message
		padType  uint16
	}{
		{messages: raw.tableReplies, padType: kernelNFTATablePad},
		{messages: raw.chainReplies, padType: unix.NFTA_CHAIN_PAD},
		{messages: raw.ruleReplies, padType: unix.NFTA_RULE_PAD},
	}
	for _, group := range groups {
		messages := group.messages
		for index := range messages {
			binary.BigEndian.PutUint16(messages[index].Data[2:4], 0)
			mutateMessageAttributes(t, &messages[index], func(attrs []netlink.Attribute) []netlink.Attribute {
				return slices.DeleteFunc(attrs, func(attr netlink.Attribute) bool {
					return testAttributeType(attr) == group.padType
				})
			})
		}
	}

	observed, err := decodeKernelObservation(raw)
	require.NoError(t, err)
	want.nftBaseSequence = 0
	assertKernelObservationEqual(t, want, observed)
}

// Rationale: Dump envelopes can be syntactically valid while representing partial parents, duplicate identities, or
// inconsistent kernel counters; those conditions must be rejected before compiler comparison.
func TestDecodeKernelObservationRejectsObjectIntegrityDrift(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)
	tests := map[string]func(*kernelObservationMessages){
		"partial tables": func(raw *kernelObservationMessages) {
			raw.tableReplies = raw.tableReplies[:1]
		},
		"partial chains": func(raw *kernelObservationMessages) {
			raw.chainReplies = raw.chainReplies[:4]
		},
		"chains without tables": func(raw *kernelObservationMessages) {
			raw.tableReplies = nil
		},
		"rules without parents": func(raw *kernelObservationMessages) {
			raw.tableReplies = nil
			raw.chainReplies = nil
		},
		"zero table handle": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.tableReplies[0], testNFTATableHandle, testBigEndianUint64(0))
		},
		"duplicate table handle": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.tableReplies[1], testNFTATableHandle, testBigEndianUint64(1))
		},
		"zero chain handle": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[0], unix.NFTA_CHAIN_HANDLE, testBigEndianUint64(0))
		},
		"duplicate chain handle": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[1], unix.NFTA_CHAIN_HANDLE, testBigEndianUint64(100))
		},
		"duplicate chain name": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[1], unix.NFTA_CHAIN_NAME,
				[]byte(managedBridgePreroutingChainName+"\x00"))
		},
		"table use mismatch": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.tableReplies[0], unix.NFTA_TABLE_USE, testBigEndianUint32(1))
		},
		"zero rule handle": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_HANDLE, testBigEndianUint64(0))
		},
		"duplicate rule handle": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[1], unix.NFTA_RULE_HANDLE, testBigEndianUint64(1000))
		},
		"unknown rule chain": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_CHAIN, []byte("foreign\x00"))
		},
		"empty userdata": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_USERDATA, nil)
		},
		"oversize userdata": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_USERDATA,
				make([]byte, maxKernelObservationUserdataBytes+1))
		},
		"invalid userdata identity": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_USERDATA, []byte("foreign"))
		},
		"mixed generation identity": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[1], unix.NFTA_RULE_USERDATA,
				testRuleUserdata(strings.Repeat("0", 64), "different.rule"))
		},
		"duplicate rule identity": func(raw *kernelObservationMessages) {
			first := requireMessageAttribute(t, raw.ruleReplies[0], unix.NFTA_RULE_USERDATA)
			setMessageAttributeData(t, &raw.ruleReplies[1], unix.NFTA_RULE_USERDATA, slices.Clone(first.Data))
		},
		"no rules": func(raw *kernelObservationMessages) {
			raw.ruleReplies = nil
			setMessageAttributeData(t, &raw.chainReplies[2], unix.NFTA_CHAIN_USE, testBigEndianUint32(0))
			setMessageAttributeData(t, &raw.chainReplies[3], unix.NFTA_CHAIN_USE, testBigEndianUint32(0))
		},
		"chain use mismatch": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[2], unix.NFTA_CHAIN_USE, testBigEndianUint32(2))
		},
		"invalid chain policy": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[0], unix.NFTA_CHAIN_POLICY, testBigEndianUint32(2))
		},
		"invalid chain flags": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[0], testNFTAChainFlags, testBigEndianUint32(0))
		},
		"cross-table rule order": func(raw *kernelObservationMessages) {
			bridgeRule := cloneKernelMessages(raw.ruleReplies[:1])[0]
			bridgeRule.Data[0] = byte(nftables.TableFamilyBridge)
			mutateMessageAttributes(t, &bridgeRule, func(attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					switch testAttributeType(attrs[index]) {
					case unix.NFTA_RULE_TABLE:
						attrs[index].Data = []byte(managedBridgeTableName + "\x00")
					case unix.NFTA_RULE_CHAIN:
						attrs[index].Data = []byte(managedBridgeForwardChainName + "\x00")
					case unix.NFTA_RULE_HANDLE:
						attrs[index].Data = testBigEndianUint64(2000)
					case unix.NFTA_RULE_USERDATA:
						attrs[index].Data = testRuleUserdata(testKernelGenerationID, "bridge.late")
					}
				}
				return attrs
			})
			raw.ruleReplies = append(raw.ruleReplies, bridgeRule)
			setMessageAttributeData(t, &raw.chainReplies[1], unix.NFTA_CHAIN_USE, testBigEndianUint32(1))
		},
		"base sequence drift": func(raw *kernelObservationMessages) {
			binary.BigEndian.PutUint16(raw.ruleReplies[0].Data[2:4], testNFTBaseSequence+1)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)
			_, err := decodeKernelObservation(raw)
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: NFTA_RULE_POSITION is kernel-handle evidence rather than compiler position state, so each chain must form
// its own exact predecessor sequence without leaking ordering across chains.
func TestDecodeKernelObservationRejectsRulePositionDrift(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)
	tests := map[string]func(*kernelObservationMessages){
		"first rule has position": func(raw *kernelObservationMessages) {
			appendMessageAttribute(t, &raw.ruleReplies[0], unix.NFTA_RULE_POSITION)
		},
		"later rule misses position": func(raw *kernelObservationMessages) {
			removeMessageAttribute(t, &raw.ruleReplies[1], unix.NFTA_RULE_POSITION)
		},
		"later rule has wrong position": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[1], unix.NFTA_RULE_POSITION, testBigEndianUint64(999))
		},
		"later rule has wrong position width": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[1], unix.NFTA_RULE_POSITION, make([]byte, 7))
		},
		"rule returns to earlier chain": func(raw *kernelObservationMessages) {
			raw.ruleReplies[2], raw.ruleReplies[3] = raw.ruleReplies[3], raw.ruleReplies[2]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)
			_, err := decodeKernelObservation(raw)
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: Optional alignment PAD is the only admitted extension point and must remain unique, flagless, and empty.
func TestDecodeKernelObservationRejectsInvalidPaddingAttributes(t *testing.T) {
	valid, _ := capturedKernelObservationFixture(t)
	tests := map[string]func(*kernelObservationMessages){
		"table padding data": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.tableReplies[0], kernelNFTATablePad, []byte{1})
		},
		"chain padding data": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.chainReplies[0], unix.NFTA_CHAIN_PAD, []byte{1})
		},
		"rule padding data": func(raw *kernelObservationMessages) {
			setMessageAttributeData(t, &raw.ruleReplies[0], unix.NFTA_RULE_PAD, []byte{1})
		},
		"padding flag": func(raw *kernelObservationMessages) {
			setMessageAttributeFlags(t, &raw.tableReplies[0], kernelNFTATablePad, netlink.Nested)
		},
		"duplicate padding": func(raw *kernelObservationMessages) {
			appendMessageAttribute(t, &raw.ruleReplies[0], unix.NFTA_RULE_PAD)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)
			_, err := decodeKernelObservation(raw)
			requireKernelObservationRejected(t, err)
		})
	}
}

// Rationale: Bounds are security properties independent of semantic decoding, so exact and one-over cases exercise the
// arithmetic directly without constructing impossible managed generations.
func TestKernelObservationCapsAcceptExactBoundsAndRejectOver(t *testing.T) {
	messageAtLimit := netlink.Message{Data: make([]byte, maxKernelObservationMessageBytes-netlinkHeaderBytes)}
	assert.NoError(
		t,
		validateKernelObservationCaps(kernelObservationMessages{setReplies: []netlink.Message{messageAtLimit}}),
	)
	messageOverLimit := netlink.Message{Data: make([]byte, maxKernelObservationMessageBytes-netlinkHeaderBytes+1)}
	requireKernelObservationRejected(t,
		validateKernelObservationCaps(kernelObservationMessages{setReplies: []netlink.Message{messageOverLimit}}))

	bytesAtLimit := slices.Repeat(
		[]netlink.Message{messageAtLimit},
		maxKernelObservationBytes/maxKernelObservationMessageBytes,
	)
	assert.NoError(t, validateKernelObservationCaps(kernelObservationMessages{setReplies: bytesAtLimit}))
	bytesOverLimit := append(slices.Clone(bytesAtLimit), netlink.Message{})
	requireKernelObservationRejected(t,
		validateKernelObservationCaps(kernelObservationMessages{setReplies: bytesOverLimit}))

	messageCountAtLimit := make([]netlink.Message, maxKernelObservationMessages)
	assert.NoError(t, validateKernelObservationCaps(kernelObservationMessages{setReplies: messageCountAtLimit}))
	requireKernelObservationRejected(t, validateKernelObservationCaps(kernelObservationMessages{
		setReplies: make([]netlink.Message, maxKernelObservationMessages+1),
	}))

	assert.NoError(t, validateKernelObservationCaps(kernelObservationMessages{
		tableReplies: make([]netlink.Message, len(managedTableIdentities)),
		chainReplies: make([]netlink.Message, 5),
		ruleReplies:  make([]netlink.Message, maxKernelObservationRules),
	}))
	requireKernelObservationRejected(t, validateKernelObservationCaps(kernelObservationMessages{
		tableReplies: make([]netlink.Message, len(managedTableIdentities)+1),
	}))
	requireKernelObservationRejected(t, validateKernelObservationCaps(kernelObservationMessages{
		chainReplies: make([]netlink.Message, 6),
	}))
	requireKernelObservationRejected(t, validateKernelObservationCaps(kernelObservationMessages{
		ruleReplies: make([]netlink.Message, maxKernelObservationRules+1),
	}))

	total, err := checkedKernelExpressionTotal(maxKernelObservationExpressions-1, 1)
	require.NoError(t, err)
	assert.Equal(t, maxKernelObservationExpressions, total)
	_, err = checkedKernelExpressionTotal(maxKernelObservationExpressions, 1)
	requireKernelObservationRejected(t, err)

	expression := testKernelExpression(t, &expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1})
	perRuleAtLimit := mustMarshalKernelAttributes(t,
		slices.Repeat([]netlink.Attribute{expression}, maxKernelObservationExpressionsPerRule))
	decoded, err := decodeKernelExpressions(perRuleAtLimit, nftables.TableFamilyINet)
	require.NoError(t, err)
	assert.Len(t, decoded, maxKernelObservationExpressionsPerRule)
	perRuleOverLimit := mustMarshalKernelAttributes(t,
		slices.Repeat([]netlink.Attribute{expression}, maxKernelObservationExpressionsPerRule+1))
	_, err = decodeKernelExpressions(perRuleOverLimit, nftables.TableFamilyINet)
	requireKernelObservationRejected(t, err)
}

// Rationale: Manual wire validation must reject malformed alignment before mdlayher/netlink can normalize the input,
// including an attribute-count overflow before allocating the decoded attribute slice.
func TestKernelAttributeWireRejectsMalformedLengthsPaddingAndCount(t *testing.T) {
	validAttribute := mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 1}})
	assert.NoError(t, validateKernelAttributeWire(
		bytes.Repeat(validAttribute, maxKernelObservationAttributes), "test"))
	shortLength := testRawKernelAttribute(3, 4)
	longLength := testRawKernelAttribute(8, 4)
	truncatedPadding := testRawKernelAttribute(5, 5)
	nonzeroPadding := testRawKernelAttribute(5, 8)
	nonzeroPadding[5] = 1

	tests := map[string][]byte{
		"truncated header":  {1, 2, 3},
		"short length":      shortLength,
		"long length":       longLength,
		"truncated padding": truncatedPadding,
		"nonzero padding":   nonzeroPadding,
		"attribute count":   bytes.Repeat(validAttribute, maxKernelObservationAttributes+1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			requireKernelObservationRejected(t, validateKernelAttributeWire(raw, "test"))
		})
	}
}

// Rationale: Exact string, scalar, and opaque-data widths prevent truncated identifiers or register values from being
// interpreted as a different canonical firewall object.
func TestKernelAttributePrimitiveBounds(t *testing.T) {
	maxString := append(bytes.Repeat([]byte{'a'}, maxKernelObservationStringBytes-1), 0)
	decoded, err := kernelAttributeString(netlink.Attribute{Data: maxString}, "test")
	require.NoError(t, err)
	assert.Len(t, decoded, maxKernelObservationStringBytes-1)
	assert.Equal(t, uint32(7), mustKernelUint32(t, testBigEndianUint32(7)))
	assert.Equal(t, uint64(9), mustKernelUint64(t, testBigEndianUint64(9)))

	invalidStrings := map[string][]byte{
		"nil":                nil,
		"only terminator":    {0},
		"missing terminator": []byte("missing-nul"),
		"embedded NUL":       {'a', 0, 'b', 0},
		"invalid UTF-8":      {0xff, 0},
		"over limit":         append(bytes.Repeat([]byte{'a'}, maxKernelObservationStringBytes), 0),
	}
	for name, raw := range invalidStrings {
		t.Run("string/"+name, func(t *testing.T) {
			_, stringErr := kernelAttributeString(netlink.Attribute{Data: raw}, "test")
			requireKernelObservationRejected(t, stringErr)
		})
	}
	for name, width := range map[string]int{"short": 3, "long": 5} {
		t.Run("uint32/"+name, func(t *testing.T) {
			_, scalarErr := kernelAttributeUint32(netlink.Attribute{Data: make([]byte, width)}, "test")
			requireKernelObservationRejected(t, scalarErr)
		})
	}
	for name, width := range map[string]int{"short": 7, "long": 9} {
		t.Run("uint64/"+name, func(t *testing.T) {
			_, scalarErr := kernelAttributeUint64(netlink.Attribute{Data: make([]byte, width)}, "test")
			requireKernelObservationRejected(t, scalarErr)
		})
	}
	for _, width := range []int{1, 64} {
		value, dataErr := decodeKernelDataValue(mustMarshalKernelAttributes(t,
			[]netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: make([]byte, width)}}), "test data")
		require.NoError(t, dataErr)
		assert.Len(t, value, width)
	}
	for name, width := range map[string]int{"empty": 0, "over limit": 65} {
		t.Run("data/"+name, func(t *testing.T) {
			_, dataErr := decodeKernelDataValue(mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: make([]byte, width)}}), "test data")
			requireKernelObservationRejected(t, dataErr)
		})
	}
}

// Rationale: Each of the compiler's eight expression types needs an independent positive wire proof and must reject
// missing, duplicate, unknown, flagged, or malformed envelope attributes before type-specific decoding.
func TestDecodeKernelExpressionsAcceptsClosedTypesAndRejectsEnvelopeAmbiguity(t *testing.T) {
	for name, value := range testKernelExpressionValues() {
		t.Run(name+"/accepted", func(t *testing.T) {
			raw := mustMarshalKernelAttributes(t, []netlink.Attribute{testKernelExpression(t, value)})
			decoded, err := decodeKernelExpressions(raw, nftables.TableFamilyINet)
			require.NoError(t, err)
			assertKernelExpressionsEqual(t, []expr.Any{value}, decoded)
		})

		mutations := map[string]func(*netlink.Attribute, []netlink.Attribute) []netlink.Attribute{
			"missing": func(_ *netlink.Attribute, attrs []netlink.Attribute) []netlink.Attribute {
				return slices.DeleteFunc(attrs, func(attr netlink.Attribute) bool {
					return testAttributeType(attr) == unix.NFTA_EXPR_DATA
				})
			},
			"duplicate": func(_ *netlink.Attribute, attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, attrs[0])
			},
			"unknown": func(_ *netlink.Attribute, attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, netlink.Attribute{Type: 63})
			},
			"flag": func(_ *netlink.Attribute, attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					if testAttributeType(attrs[index]) == unix.NFTA_EXPR_DATA {
						attrs[index].Type |= netlink.Nested
					}
				}
				return attrs
			},
			"wrong width": func(_ *netlink.Attribute, attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					if testAttributeType(attrs[index]) == unix.NFTA_EXPR_NAME {
						attrs[index].Data = []byte{0}
					}
				}
				return attrs
			},
			"list flag": func(element *netlink.Attribute, attrs []netlink.Attribute) []netlink.Attribute {
				element.Type |= netlink.Nested
				return attrs
			},
		}
		for mutationName, mutate := range mutations {
			t.Run(name+"/"+mutationName, func(t *testing.T) {
				element := testKernelExpression(t, value)
				attrs, err := netlink.UnmarshalAttributes(element.Data)
				require.NoError(t, err)
				element.Data = mustMarshalKernelAttributes(t, mutate(&element, attrs))
				_, err = decodeKernelExpressions(
					mustMarshalKernelAttributes(t, []netlink.Attribute{element}),
					nftables.TableFamilyINet,
				)
				requireKernelObservationRejected(t, err)
			})
		}
	}
}

// Rationale: The compiler emits masquerade with no type-specific data, so even a known masquerade attribute would
// represent unsupported semantics and must be rejected.
func TestDecodeKernelMasqRejectsAnyAttribute(t *testing.T) {
	raw := mutateTestKernelExpressionData(t, &expr.Masq{}, func(attrs []netlink.Attribute) []netlink.Attribute {
		return append(attrs, netlink.Attribute{Type: unix.NFTA_MASQ_FLAGS, Data: testBigEndianUint32(0)})
	})
	_, err := decodeKernelExpressions(raw, nftables.TableFamilyINet)
	requireKernelObservationRejected(t, err)
}

// Rationale: Type-specific attribute sets are closed independently of the common expression envelope; scalar width
// drift and flags must not be normalized into a compiler-supported expression.
func TestDecodeKernelExpressionsRejectsTypeSpecificAttributeAmbiguity(t *testing.T) {
	tests := map[string]struct {
		value        expr.Any
		requiredAttr uint16
	}{
		"meta": {value: &expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, requiredAttr: unix.NFTA_META_DREG},
		"bitwise": {
			value: &expr.Bitwise{
				SourceRegister: 2,
				DestRegister:   3,
				Len:            4,
				Mask:           []byte{0xff, 0xf0, 0x0f, 0xaa},
				Xor:            []byte{0x11, 0x22, 0x33, 0x44},
			},
			requiredAttr: unix.NFTA_BITWISE_SREG,
		},
		"cmp": {
			value:        &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{1}},
			requiredAttr: unix.NFTA_CMP_SREG,
		},
		"ct": {value: &expr.Ct{Register: 1, Key: expr.CtKeySTATE}, requiredAttr: unix.NFTA_CT_DREG},
		"payload": {
			value: &expr.Payload{
				DestRegister: 4,
				Base:         expr.PayloadBaseTransportHeader,
				Offset:       7,
				Len:          2,
			},
			requiredAttr: unix.NFTA_PAYLOAD_DREG,
		},
		"range": {
			value:        &expr.Range{Op: expr.CmpOpEq, Register: 1, FromData: []byte{1}, ToData: []byte{2}},
			requiredAttr: unix.NFTA_RANGE_SREG,
		},
		"immediate": {value: &expr.Verdict{Kind: expr.VerdictAccept}, requiredAttr: unix.NFTA_IMMEDIATE_DREG},
	}
	for name, tc := range tests {
		mutations := map[string]func([]netlink.Attribute) []netlink.Attribute{
			"missing": func(attrs []netlink.Attribute) []netlink.Attribute {
				return slices.DeleteFunc(attrs, func(attr netlink.Attribute) bool {
					return testAttributeType(attr) == tc.requiredAttr
				})
			},
			"duplicate": func(attrs []netlink.Attribute) []netlink.Attribute {
				for _, attr := range attrs {
					if testAttributeType(attr) == tc.requiredAttr {
						return append(attrs, attr)
					}
				}
				return attrs
			},
			"unknown": func(attrs []netlink.Attribute) []netlink.Attribute {
				return append(attrs, netlink.Attribute{Type: 63})
			},
			"flag": func(attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					if testAttributeType(attrs[index]) == tc.requiredAttr {
						attrs[index].Type |= netlink.NetByteOrder
					}
				}
				return attrs
			},
			"wrong width": func(attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					if testAttributeType(attrs[index]) == tc.requiredAttr {
						attrs[index].Data = make([]byte, 3)
					}
				}
				return attrs
			},
		}
		for mutationName, mutate := range mutations {
			t.Run(name+"/"+mutationName, func(t *testing.T) {
				raw := mutateTestKernelExpressionData(t, tc.value, mutate)
				_, err := decodeKernelExpressions(raw, nftables.TableFamilyINet)
				requireKernelObservationRejected(t, err)
			})
		}
	}
}

// Rationale: Nftables data/value and immediate/verdict trees are flagless nested payloads; every level must reject
// missing, duplicate, unknown, flagged, oversized, wrong-register, and unsupported-verdict alternatives.
func TestDecodeKernelExpressionsRejectsNestedDataAndVerdictDrift(t *testing.T) {
	dataCases := map[string][]netlink.Attribute{
		"missing": nil,
		"duplicate": {
			{Type: unix.NFTA_DATA_VALUE, Data: []byte{1}},
			{Type: unix.NFTA_DATA_VALUE, Data: []byte{1}},
		},
		"unknown":     {{Type: 63, Data: []byte{1}}},
		"flag":        {{Type: netlink.Nested | unix.NFTA_DATA_VALUE, Data: []byte{1}}},
		"over length": {{Type: unix.NFTA_DATA_VALUE, Data: make([]byte, 65)}},
	}
	for name, attrs := range dataCases {
		t.Run("data/"+name, func(t *testing.T) {
			_, err := decodeKernelDataValue(mustMarshalKernelAttributes(t, attrs), "test data")
			requireKernelObservationRejected(t, err)
		})
	}

	nestedFlagCases := map[string]struct {
		value    expr.Any
		attrType uint16
	}{
		"bitwise mask": {value: &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 1,
			Mask: []byte{1}, Xor: []byte{0}}, attrType: unix.NFTA_BITWISE_MASK},
		"comparison data": {value: &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{1}},
			attrType: unix.NFTA_CMP_DATA},
		"range data": {value: &expr.Range{Op: expr.CmpOpEq, Register: 1, FromData: []byte{1}, ToData: []byte{2}},
			attrType: unix.NFTA_RANGE_FROM_DATA},
		"immediate data": {value: &expr.Verdict{Kind: expr.VerdictAccept}, attrType: unix.NFTA_IMMEDIATE_DATA},
	}
	for name, tc := range nestedFlagCases {
		t.Run(name+" flag", func(t *testing.T) {
			raw := mutateTestKernelExpressionData(t, tc.value, func(attrs []netlink.Attribute) []netlink.Attribute {
				for index := range attrs {
					if testAttributeType(attrs[index]) == tc.attrType {
						attrs[index].Type |= netlink.Nested
					}
				}
				return attrs
			})
			_, err := decodeKernelExpressions(raw, nftables.TableFamilyINet)
			requireKernelObservationRejected(t, err)
		})
	}

	verdictTests := map[string]func([]netlink.Attribute) []netlink.Attribute{
		"wrong register": func(attrs []netlink.Attribute) []netlink.Attribute {
			setTestAttributeData(attrs, unix.NFTA_IMMEDIATE_DREG, testBigEndianUint32(1))
			return attrs
		},
		"missing verdict data": func(attrs []netlink.Attribute) []netlink.Attribute {
			return replaceTestNestedAttributes(t, attrs, unix.NFTA_IMMEDIATE_DATA, nil)
		},
		"duplicate verdict data": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestNestedAttributes(t, attrs, unix.NFTA_IMMEDIATE_DATA,
				func(nested []netlink.Attribute) []netlink.Attribute { return append(nested, nested[0]) })
		},
		"unknown verdict data": func(attrs []netlink.Attribute) []netlink.Attribute {
			return replaceTestNestedAttributes(t, attrs, unix.NFTA_IMMEDIATE_DATA,
				[]netlink.Attribute{{Type: 63}})
		},
		"flagged verdict data": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestNestedAttributes(t, attrs, unix.NFTA_IMMEDIATE_DATA,
				func(nested []netlink.Attribute) []netlink.Attribute {
					nested[0].Type |= netlink.Nested
					return nested
				})
		},
		"missing verdict code": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestVerdictAttributes(t, attrs, func([]netlink.Attribute) []netlink.Attribute { return nil })
		},
		"duplicate verdict code": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestVerdictAttributes(t, attrs,
				func(nested []netlink.Attribute) []netlink.Attribute { return append(nested, nested[0]) })
		},
		"unknown verdict attribute": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestVerdictAttributes(t, attrs,
				func(nested []netlink.Attribute) []netlink.Attribute {
					return append(nested, netlink.Attribute{Type: unix.NFTA_VERDICT_CHAIN, Data: []byte("x\x00")})
				})
		},
		"flagged verdict code": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestVerdictAttributes(t, attrs,
				func(nested []netlink.Attribute) []netlink.Attribute {
					nested[0].Type |= netlink.NetByteOrder
					return nested
				})
		},
		"wrong verdict width": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestVerdictAttributes(t, attrs,
				func(nested []netlink.Attribute) []netlink.Attribute {
					nested[0].Data = make([]byte, 3)
					return nested
				})
		},
		"unsupported verdict kind": func(attrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestVerdictAttributes(t, attrs,
				func(nested []netlink.Attribute) []netlink.Attribute {
					kind := int32(expr.VerdictContinue)
					nested[0].Data = testBigEndianUint32(uint32(kind))
					return nested
				})
		},
	}
	for name, mutate := range verdictTests {
		t.Run("verdict/"+name, func(t *testing.T) {
			raw := mutateTestKernelExpressionData(t, &expr.Verdict{Kind: expr.VerdictAccept}, mutate)
			_, err := decodeKernelExpressions(raw, nftables.TableFamilyINet)
			requireKernelObservationRejected(t, err)
		})
	}
}

// capturedKernelObservationFixture builds a captured-style Linux dump without compiler or expression marshalers.
// The explicit observation returned beside it is the first success oracle.
func capturedKernelObservationFixture(t *testing.T) (kernelObservationMessages, kernelObservation) {
	t.Helper()

	networkID := strings.Repeat("b", 64)
	stateMask := make([]byte, 4)
	binary.NativeEndian.PutUint32(stateMask, expr.CtStateBitESTABLISHED|expr.CtStateBitRELATED)
	interfaceData := make([]byte, 16)
	copy(interfaceData, "mvmbr0")
	interfaceMatch := func(key expr.MetaKey) []expr.Any {
		return []expr.Any{
			&expr.Meta{Key: key, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: slices.Clone(interfaceData)},
		}
	}
	established := func(key expr.MetaKey) []expr.Any {
		return append(interfaceMatch(key),
			&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
				Mask: slices.Clone(stateMask), Xor: make([]byte, 4)},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)},
			&expr.Verdict{Kind: expr.VerdictAccept},
		)
	}
	denied := func(key expr.MetaKey) []expr.Any {
		return append(interfaceMatch(key), &expr.Verdict{Kind: expr.VerdictDrop})
	}
	inetIdentity := tableIdentity{family: nftables.TableFamilyINet, name: managedINetTableName}
	rule := func(chain string, identity string, expressions []expr.Any) kernelObservedRule {
		return kernelObservedRule{
			table:       inetIdentity,
			chain:       chain,
			userdata:    testRuleUserdata(testKernelGenerationID, identity),
			expressions: expressions,
		}
	}

	want := kernelObservation{
		nftBaseSequence: testNFTBaseSequence,
		generationID:    testKernelGenerationID,
		tables: []kernelObservedTable{
			{
				family: nftables.TableFamilyBridge,
				name:   managedBridgeTableName,
				chains: []kernelObservedChain{
					{
						name: managedBridgePreroutingChainName, hook: *nftables.ChainHookPrerouting,
						hookPresent: true, priority: *nftables.ChainPriorityRaw, priorityPresent: true,
						chainType: nftables.ChainTypeFilter, policy: nftables.ChainPolicyAccept,
						policyPresent: true,
					},
					{
						name: managedBridgeForwardChainName, hook: *nftables.ChainHookForward,
						hookPresent: true, priority: *nftables.ChainPriorityFilter, priorityPresent: true,
						chainType: nftables.ChainTypeFilter, policy: nftables.ChainPolicyAccept,
						policyPresent: true,
					},
				},
			},
			{
				family: nftables.TableFamilyINet,
				name:   managedINetTableName,
				chains: []kernelObservedChain{
					{
						name: managedINetForwardChainName, hook: *nftables.ChainHookForward,
						hookPresent: true, priority: *nftables.ChainPriorityFilter, priorityPresent: true,
						chainType: nftables.ChainTypeFilter, policy: nftables.ChainPolicyAccept,
						policyPresent: true, use: 3,
					},
					{
						name: managedINetInputChainName, hook: *nftables.ChainHookInput,
						hookPresent: true, priority: *nftables.ChainPriorityFilter, priorityPresent: true,
						chainType: nftables.ChainTypeFilter, policy: nftables.ChainPolicyAccept,
						policyPresent: true, use: 2,
					},
					{
						name: managedINetNATChainName, hook: *nftables.ChainHookPostrouting,
						hookPresent: true, priority: *nftables.ChainPriorityNATSource, priorityPresent: true,
						chainType: nftables.ChainTypeNAT, policy: nftables.ChainPolicyAccept,
						policyPresent: true,
					},
				},
				rules: []kernelObservedRule{
					rule(managedINetForwardChainName, "inet.forward.established."+networkID,
						established(expr.MetaKeyOIFNAME)),
					rule(managedINetForwardChainName, "inet.forward.default-deny."+networkID,
						denied(expr.MetaKeyIIFNAME)),
					rule(managedINetForwardChainName, "inet.forward.default-deny-inbound."+networkID,
						denied(expr.MetaKeyOIFNAME)),
					rule(managedINetInputChainName, "inet.input.established."+networkID,
						established(expr.MetaKeyIIFNAME)),
					rule(managedINetInputChainName, "inet.input.default-deny."+networkID,
						denied(expr.MetaKeyIIFNAME)),
				},
			},
		},
	}

	return kernelMessagesFromObservation(t, want), want
}

// kernelDumpObservationFromCompiled independently projects compiler rules into Linux's declared-chain dump order.
func kernelDumpObservationFromCompiled(compiled compiledGeneration) kernelObservation {
	observed := kernelObservationFromCompiled(compiled)
	observed.nftBaseSequence = testNFTBaseSequence
	for tableIndex := range observed.tables {
		table := &observed.tables[tableIndex]
		insertionOrder := slices.Clone(table.rules)
		if len(insertionOrder) == 0 {
			continue
		}
		table.rules = make([]kernelObservedRule, 0, len(insertionOrder))
		for chainIndex := range table.chains {
			for _, rule := range insertionOrder {
				if rule.chain != table.chains[chainIndex].name {
					continue
				}
				table.rules = append(table.rules, rule)
				table.chains[chainIndex].use++
			}
		}
	}
	return observed
}

// kernelMessagesFromObservation serializes only the explicit captured-style test literal into flagless dump nests.
func kernelMessagesFromObservation(t *testing.T, observed kernelObservation) kernelObservationMessages {
	t.Helper()
	result := kernelObservationMessages{}
	tableHandle := uint64(1)
	chainHandle := uint64(100)
	ruleHandle := uint64(1000)
	for tableIndex := range observed.tables {
		table := observed.tables[tableIndex]
		result.tableReplies = append(result.tableReplies, testKernelMessage(t, unix.NFT_MSG_NEWTABLE,
			table.family, 0, []netlink.Attribute{
				{Type: unix.NFTA_TABLE_NAME, Data: []byte(table.name + "\x00")},
				{Type: unix.NFTA_TABLE_FLAGS, Data: testBigEndianUint32(table.flags)},
				{Type: unix.NFTA_TABLE_USE, Data: testBigEndianUint32(uint32(len(table.chains)))},
				{Type: testNFTATableHandle, Data: testBigEndianUint64(tableHandle)},
				{Type: kernelNFTATablePad},
			}))
		tableHandle++
		for chainIndex := range table.chains {
			chain := table.chains[chainIndex]
			hook := mustMarshalKernelAttributes(t, []netlink.Attribute{
				{Type: unix.NFTA_HOOK_HOOKNUM, Data: testBigEndianUint32(uint32(chain.hook))},
				{Type: unix.NFTA_HOOK_PRIORITY, Data: testBigEndianUint32(uint32(chain.priority))},
			})
			result.chainReplies = append(result.chainReplies, testKernelMessage(t, unix.NFT_MSG_NEWCHAIN,
				table.family, 0, []netlink.Attribute{
					{Type: unix.NFTA_CHAIN_TABLE, Data: []byte(table.name + "\x00")},
					{Type: unix.NFTA_CHAIN_HANDLE, Data: testBigEndianUint64(chainHandle)},
					{Type: unix.NFTA_CHAIN_NAME, Data: []byte(chain.name + "\x00")},
					{Type: unix.NFTA_CHAIN_HOOK, Data: hook},
					{Type: unix.NFTA_CHAIN_POLICY, Data: testBigEndianUint32(uint32(chain.policy))},
					{Type: unix.NFTA_CHAIN_USE, Data: testBigEndianUint32(chain.use)},
					{Type: unix.NFTA_CHAIN_TYPE, Data: []byte(chain.chainType + "\x00")},
					{Type: unix.NFTA_CHAIN_PAD},
					{Type: testNFTAChainFlags, Data: testBigEndianUint32(testNFTChainBase)},
				}))
			chainHandle++
		}
		previousHandles := make(map[string]uint64, len(table.chains))
		for ruleIndex := range table.rules {
			rule := table.rules[ruleIndex]
			expressions := make([]netlink.Attribute, 0, len(rule.expressions))
			for _, expression := range rule.expressions {
				expressions = append(expressions, testKernelExpression(t, expression))
			}
			attrs := []netlink.Attribute{
				{Type: unix.NFTA_RULE_TABLE, Data: []byte(table.name + "\x00")},
				{Type: unix.NFTA_RULE_CHAIN, Data: []byte(rule.chain + "\x00")},
				{Type: unix.NFTA_RULE_HANDLE, Data: testBigEndianUint64(ruleHandle)},
				{Type: unix.NFTA_RULE_EXPRESSIONS, Data: mustMarshalKernelAttributes(t, expressions)},
				{Type: unix.NFTA_RULE_USERDATA, Data: slices.Clone(rule.userdata)},
				{Type: unix.NFTA_RULE_PAD},
			}
			if previous := previousHandles[rule.chain]; previous != 0 {
				attrs = slices.Insert(attrs, 3,
					netlink.Attribute{Type: unix.NFTA_RULE_POSITION, Data: testBigEndianUint64(previous)})
			}
			result.ruleReplies = append(result.ruleReplies,
				testKernelMessage(t, unix.NFT_MSG_NEWRULE, table.family, netlink.Multi|netlink.Append, attrs))
			previousHandles[rule.chain] = ruleHandle
			ruleHandle++
		}
	}
	return result
}

// testKernelMessage wraps flagless attributes in one exact nftables response envelope.
func testKernelMessage(
	t *testing.T,
	messageType uint16,
	family nftables.TableFamily,
	flags netlink.HeaderFlags,
	attrs []netlink.Attribute,
) netlink.Message {
	t.Helper()
	data := []byte{byte(family), 0, 0, 0}
	binary.BigEndian.PutUint16(data[2:], testNFTBaseSequence)
	data = append(data, mustMarshalKernelAttributes(t, attrs)...)
	return netlink.Message{
		Header: netlink.Header{
			Length:   uint32(netlinkHeaderBytes + len(data)),
			Type:     netlink.HeaderType(testNetfilterSubsystemType | messageType),
			Flags:    flags,
			Sequence: 1,
			PID:      1,
		},
		Data: data,
	}
}

// kernelFixtureIntent is intentionally small enough for the independent raw oracle to remain reviewable while still
// exercising multi-rule and multi-chain ordering.
func kernelFixtureIntent() generationIntent {
	return generationIntent{networks: []networkIntent{{
		id:         strings.Repeat("b", 64),
		hostBridge: "mvmbr0",
		subnet:     netip.MustParsePrefix("10.0.0.0/24"),
		hostIPv4:   netip.MustParseAddr("10.0.0.1"),
	}}}
}

// testKernelExpression hand-encodes the flagless nested form emitted by Linux nftables dump replies.
func testKernelExpression(t *testing.T, expression expr.Any) netlink.Attribute {
	t.Helper()
	name := ""
	data := []netlink.Attribute{}
	switch value := expression.(type) {
	case *expr.Meta:
		name = "meta"
		data = []netlink.Attribute{
			{Type: unix.NFTA_META_DREG, Data: testBigEndianUint32(value.Register)},
			{Type: unix.NFTA_META_KEY, Data: testBigEndianUint32(uint32(value.Key))},
		}
	case *expr.Masq:
		name = "masq"
	case *expr.Bitwise:
		name = "bitwise"
		data = []netlink.Attribute{
			{Type: unix.NFTA_BITWISE_SREG, Data: testBigEndianUint32(value.SourceRegister)},
			{Type: unix.NFTA_BITWISE_DREG, Data: testBigEndianUint32(value.DestRegister)},
			{Type: unix.NFTA_BITWISE_LEN, Data: testBigEndianUint32(value.Len)},
			{Type: unix.NFTA_BITWISE_MASK, Data: mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: slices.Clone(value.Mask)}})},
			{Type: unix.NFTA_BITWISE_XOR, Data: mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: slices.Clone(value.Xor)}})},
		}
	case *expr.Cmp:
		name = "cmp"
		data = []netlink.Attribute{
			{Type: unix.NFTA_CMP_SREG, Data: testBigEndianUint32(value.Register)},
			{Type: unix.NFTA_CMP_OP, Data: testBigEndianUint32(uint32(value.Op))},
			{Type: unix.NFTA_CMP_DATA, Data: mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: slices.Clone(value.Data)}})},
		}
	case *expr.Ct:
		name = "ct"
		data = []netlink.Attribute{
			{Type: unix.NFTA_CT_DREG, Data: testBigEndianUint32(value.Register)},
			{Type: unix.NFTA_CT_KEY, Data: testBigEndianUint32(uint32(value.Key))},
		}
	case *expr.Payload:
		name = "payload"
		data = []netlink.Attribute{
			{Type: unix.NFTA_PAYLOAD_DREG, Data: testBigEndianUint32(value.DestRegister)},
			{Type: unix.NFTA_PAYLOAD_BASE, Data: testBigEndianUint32(uint32(value.Base))},
			{Type: unix.NFTA_PAYLOAD_OFFSET, Data: testBigEndianUint32(value.Offset)},
			{Type: unix.NFTA_PAYLOAD_LEN, Data: testBigEndianUint32(value.Len)},
		}
	case *expr.Range:
		name = "range"
		data = []netlink.Attribute{
			{Type: unix.NFTA_RANGE_SREG, Data: testBigEndianUint32(value.Register)},
			{Type: unix.NFTA_RANGE_OP, Data: testBigEndianUint32(uint32(value.Op))},
			{Type: unix.NFTA_RANGE_FROM_DATA, Data: mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: slices.Clone(value.FromData)}})},
			{Type: unix.NFTA_RANGE_TO_DATA, Data: mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: unix.NFTA_DATA_VALUE, Data: slices.Clone(value.ToData)}})},
		}
	case *expr.Verdict:
		name = "immediate"
		verdict := mustMarshalKernelAttributes(t, []netlink.Attribute{
			{Type: unix.NFTA_VERDICT_CODE, Data: testBigEndianUint32(uint32(value.Kind))},
		})
		data = []netlink.Attribute{
			{Type: unix.NFTA_IMMEDIATE_DREG, Data: testBigEndianUint32(unix.NFT_REG_VERDICT)},
			{Type: unix.NFTA_IMMEDIATE_DATA, Data: mustMarshalKernelAttributes(t,
				[]netlink.Attribute{{Type: unix.NFTA_DATA_VERDICT, Data: verdict}})},
		}
	default:
		t.Fatalf("unsupported test expression %T", expression)
	}
	return netlink.Attribute{Type: testNFTAListElem, Data: mustMarshalKernelAttributes(t, []netlink.Attribute{
		{Type: unix.NFTA_EXPR_NAME, Data: []byte(name + "\x00")},
		{Type: unix.NFTA_EXPR_DATA, Data: mustMarshalKernelAttributes(t, data)},
	})}
}

// testKernelExpressionValues enumerates the compiler's complete admitted expression type set.
func testKernelExpressionValues() map[string]expr.Any {
	return map[string]expr.Any{
		"meta": &expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		"masq": &expr.Masq{},
		"bitwise": &expr.Bitwise{SourceRegister: 2, DestRegister: 3, Len: 4,
			Mask: []byte{0xff, 0xf0, 0x0f, 0xaa}, Xor: []byte{0x11, 0x22, 0x33, 0x44}},
		"cmp":     &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{1}},
		"ct":      &expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		"payload": &expr.Payload{DestRegister: 4, Base: expr.PayloadBaseTransportHeader, Offset: 7, Len: 2},
		"range":   &expr.Range{Op: expr.CmpOpEq, Register: 1, FromData: []byte{1}, ToData: []byte{2}},
		"verdict": &expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// mutateTestKernelExpressionData rewrites the type-specific data of one hand-built expression list element.
func mutateTestKernelExpressionData(
	t *testing.T,
	value expr.Any,
	mutate func([]netlink.Attribute) []netlink.Attribute,
) []byte {
	t.Helper()
	element := testKernelExpression(t, value)
	expressionAttrs, err := netlink.UnmarshalAttributes(element.Data)
	require.NoError(t, err)
	for index := range expressionAttrs {
		if testAttributeType(expressionAttrs[index]) != unix.NFTA_EXPR_DATA {
			continue
		}
		dataAttrs, dataErr := netlink.UnmarshalAttributes(expressionAttrs[index].Data)
		require.NoError(t, dataErr)
		expressionAttrs[index].Data = mustMarshalKernelAttributes(t, mutate(dataAttrs))
		element.Data = mustMarshalKernelAttributes(t, expressionAttrs)
		return mustMarshalKernelAttributes(t, []netlink.Attribute{element})
	}
	t.Fatal("expression data attribute not found")
	return nil
}

// setTestAttributeData replaces one known fixture attribute payload.
func setTestAttributeData(attrs []netlink.Attribute, attrType uint16, data []byte) {
	for index := range attrs {
		if testAttributeType(attrs[index]) == attrType {
			attrs[index].Data = slices.Clone(data)
			return
		}
	}
}

// replaceTestNestedAttributes installs an explicit nested payload while retaining the parent's original flags.
func replaceTestNestedAttributes(
	t *testing.T,
	attrs []netlink.Attribute,
	attrType uint16,
	nested []netlink.Attribute,
) []netlink.Attribute {
	t.Helper()
	for index := range attrs {
		if testAttributeType(attrs[index]) == attrType {
			attrs[index].Data = mustMarshalKernelAttributes(t, nested)
			return attrs
		}
	}
	t.Fatalf("nested attribute %d not found", attrType)
	return nil
}

// mutateTestNestedAttributes decodes and rewrites one nested test attribute.
func mutateTestNestedAttributes(
	t *testing.T,
	attrs []netlink.Attribute,
	attrType uint16,
	mutate func([]netlink.Attribute) []netlink.Attribute,
) []netlink.Attribute {
	t.Helper()
	for index := range attrs {
		if testAttributeType(attrs[index]) != attrType {
			continue
		}
		nested, err := netlink.UnmarshalAttributes(attrs[index].Data)
		require.NoError(t, err)
		attrs[index].Data = mustMarshalKernelAttributes(t, mutate(nested))
		return attrs
	}
	t.Fatalf("nested attribute %d not found", attrType)
	return nil
}

// mutateTestVerdictAttributes reaches the innermost verdict tree without relying on production decoding.
func mutateTestVerdictAttributes(
	t *testing.T,
	attrs []netlink.Attribute,
	mutate func([]netlink.Attribute) []netlink.Attribute,
) []netlink.Attribute {
	t.Helper()
	return mutateTestNestedAttributes(t, attrs, unix.NFTA_IMMEDIATE_DATA,
		func(dataAttrs []netlink.Attribute) []netlink.Attribute {
			return mutateTestNestedAttributes(t, dataAttrs, unix.NFTA_DATA_VERDICT, mutate)
		})
}

// testRuleUserdata builds the stable userdata grammar independently of encodeRuleIdentity.
func testRuleUserdata(generationID string, ruleID string) []byte {
	result := append([]byte("MVMNFT01"), byte(len(generationID)), byte(len(ruleID)))
	result = append(result, generationID...)
	return append(result, ruleID...)
}

// appendMessageAttribute adds a duplicate or unknown top-level attribute while retaining a valid message envelope.
func appendMessageAttribute(t *testing.T, message *netlink.Message, attrType uint16) {
	t.Helper()
	attrs, err := netlink.UnmarshalAttributes(message.Data[netfilterHeaderBytes:])
	require.NoError(t, err)
	data := []byte{0, 0, 0, 0}
	for _, attr := range attrs {
		if testAttributeType(attr) == attrType {
			data = slices.Clone(attr.Data)
			break
		}
	}
	attrs = append(attrs, netlink.Attribute{Type: attrType, Data: data})
	message.Data = append(slices.Clone(message.Data[:netfilterHeaderBytes]), mustMarshalKernelAttributes(t, attrs)...)
	message.Header.Length = uint32(netlinkHeaderBytes + len(message.Data))
}

// mutateMessageAttributes rewrites one message's top-level attributes while preserving its nfgenmsg header.
func mutateMessageAttributes(
	t *testing.T,
	message *netlink.Message,
	mutate func([]netlink.Attribute) []netlink.Attribute,
) {
	t.Helper()
	attrs, err := netlink.UnmarshalAttributes(message.Data[netfilterHeaderBytes:])
	require.NoError(t, err)
	message.Data = append(slices.Clone(message.Data[:netfilterHeaderBytes]),
		mustMarshalKernelAttributes(t, mutate(attrs))...)
	message.Header.Length = uint32(netlinkHeaderBytes + len(message.Data))
}

// requireMessageAttribute returns a copy of one exact test attribute or fails fixture construction.
func requireMessageAttribute(t *testing.T, message netlink.Message, attrType uint16) netlink.Attribute {
	t.Helper()
	attrs, err := netlink.UnmarshalAttributes(message.Data[netfilterHeaderBytes:])
	require.NoError(t, err)
	for _, attr := range attrs {
		if testAttributeType(attr) == attrType {
			return attr
		}
	}
	t.Fatalf("attribute %d not found", attrType)
	return netlink.Attribute{}
}

// setMessageAttributeData replaces one top-level payload and recomputes both attribute and message lengths.
func setMessageAttributeData(t *testing.T, message *netlink.Message, attrType uint16, data []byte) {
	t.Helper()
	mutateMessageAttributes(t, message, func(attrs []netlink.Attribute) []netlink.Attribute {
		for index := range attrs {
			if testAttributeType(attrs[index]) == attrType {
				attrs[index].Data = slices.Clone(data)
				return attrs
			}
		}
		t.Fatalf("attribute %d not found", attrType)
		return nil
	})
}

// setMessageAttributeFlags changes only the flag bits of one top-level attribute.
func setMessageAttributeFlags(
	t *testing.T,
	message *netlink.Message,
	attrType uint16,
	flags uint16,
) {
	t.Helper()
	mutateMessageAttributes(t, message, func(attrs []netlink.Attribute) []netlink.Attribute {
		for index := range attrs {
			if testAttributeType(attrs[index]) == attrType {
				attrs[index].Type = attrType | flags
				return attrs
			}
		}
		t.Fatalf("attribute %d not found", attrType)
		return nil
	})
}

// removeMessageAttribute removes exactly one fixture attribute and recomputes the envelope length.
func removeMessageAttribute(t *testing.T, message *netlink.Message, attrType uint16) {
	t.Helper()
	mutateMessageAttributes(t, message, func(attrs []netlink.Attribute) []netlink.Attribute {
		before := len(attrs)
		attrs = slices.DeleteFunc(attrs, func(attr netlink.Attribute) bool {
			return testAttributeType(attr) == attrType
		})
		require.Len(t, attrs, before-1)
		return attrs
	})
}

// mutateNestedAttribute rewrites one top-level nested payload without adding kernel-style nested flags.
func mutateNestedAttribute(
	t *testing.T,
	message *netlink.Message,
	attrType uint16,
	mutate func([]netlink.Attribute) []netlink.Attribute,
) {
	t.Helper()
	attrs, err := netlink.UnmarshalAttributes(message.Data[netfilterHeaderBytes:])
	require.NoError(t, err)
	for index := range attrs {
		if testAttributeType(attrs[index]) != attrType {
			continue
		}
		nested, nestedErr := netlink.UnmarshalAttributes(attrs[index].Data)
		require.NoError(t, nestedErr)
		attrs[index].Data = mustMarshalKernelAttributes(t, mutate(nested))
		message.Data = append(
			slices.Clone(message.Data[:netfilterHeaderBytes]),
			mustMarshalKernelAttributes(t, attrs)...)
		message.Header.Length = uint32(netlinkHeaderBytes + len(message.Data))
		return
	}
	t.Fatalf("attribute %d not found", attrType)
}

// mutateFirstExpression rewrites the first expression envelope inside one rule reply.
func mutateFirstExpression(
	t *testing.T,
	message *netlink.Message,
	mutate func([]netlink.Attribute) []netlink.Attribute,
) {
	t.Helper()
	mutateNestedAttribute(t, message, unix.NFTA_RULE_EXPRESSIONS, func(list []netlink.Attribute) []netlink.Attribute {
		require.NotEmpty(t, list)
		expressionAttrs, err := netlink.UnmarshalAttributes(list[0].Data)
		require.NoError(t, err)
		list[0].Data = mustMarshalKernelAttributes(t, mutate(expressionAttrs))
		return list
	})
}

// mustMarshalKernelAttributes clears decoded lengths so the marshaler derives exact sizes after test mutations.
func mustMarshalKernelAttributes(t *testing.T, attrs []netlink.Attribute) []byte {
	t.Helper()
	for index := range attrs {
		attrs[index].Length = 0
	}
	encoded, err := netlink.MarshalAttributes(attrs)
	require.NoError(t, err)
	return encoded
}

// cloneKernelObservationMessages deep-copies message data for isolated mutation subtests.
func cloneKernelObservationMessages(raw kernelObservationMessages) kernelObservationMessages {
	return kernelObservationMessages{
		tableReplies:     cloneKernelMessages(raw.tableReplies),
		chainReplies:     cloneKernelMessages(raw.chainReplies),
		ruleReplies:      cloneKernelMessages(raw.ruleReplies),
		setReplies:       cloneKernelMessages(raw.setReplies),
		objectReplies:    cloneKernelMessages(raw.objectReplies),
		flowtableReplies: cloneKernelMessages(raw.flowtableReplies),
	}
}

// cloneKernelMessages deep-copies the raw byte payload of every netlink message.
func cloneKernelMessages(messages []netlink.Message) []netlink.Message {
	result := slices.Clone(messages)
	for index := range result {
		result[index].Data = slices.Clone(result[index].Data)
	}
	return result
}

func testAttributeType(attr netlink.Attribute) uint16 {
	return attr.Type & ^uint16(netlink.Nested|netlink.NetByteOrder)
}

func testBigEndianUint32(value uint32) []byte {
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, value)
	return result
}

func testBigEndianUint64(value uint64) []byte {
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, value)
	return result
}

// testRawKernelAttribute creates a native-endian attribute header with a caller-controlled truncated wire size.
func testRawKernelAttribute(length uint16, wireBytes int) []byte {
	result := make([]byte, wireBytes)
	binary.NativeEndian.PutUint16(result[:2], length)
	binary.NativeEndian.PutUint16(result[2:4], 1)
	return result
}

func mustKernelUint32(t *testing.T, raw []byte) uint32 {
	t.Helper()
	value, err := kernelAttributeUint32(netlink.Attribute{Data: raw}, "test")
	require.NoError(t, err)
	return value
}

func mustKernelUint64(t *testing.T, raw []byte) uint64 {
	t.Helper()
	value, err := kernelAttributeUint64(netlink.Attribute{Data: raw}, "test")
	require.NoError(t, err)
	return value
}

// assertKernelObservationEqual reports a readable structural diff for the full private observation graph.
func assertKernelObservationEqual(t *testing.T, want kernelObservation, got kernelObservation) {
	t.Helper()
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(
		kernelObservation{},
		kernelObservedTable{},
		kernelObservedChain{},
		kernelObservedRule{},
		tableIdentity{},
	)); diff != "" {
		t.Errorf("kernel observation mismatch (-want +got):\n%s", diff)
	}
}

// assertKernelExpressionsEqual compares decoded fields directly rather than relying on the production comparator.
func assertKernelExpressionsEqual(t *testing.T, want []expr.Any, got []expr.Any) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("kernel expressions mismatch (-want +got):\n%s", diff)
	}
}
