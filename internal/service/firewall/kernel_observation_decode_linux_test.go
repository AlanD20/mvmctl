package firewall

import (
	"encoding/binary"
	"slices"
	"testing"

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
)

func TestDecodeKernelObservationAcceptsExactCompilerMessages(t *testing.T) {
	compiled, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	raw := kernelMessagesFromCompiled(t, compiled)

	observed, err := decodeKernelObservation(raw)
	require.NoError(t, err)

	assert.NoError(t, compareKernelObservation(observed, compiled))
}

func TestDecodeKernelObservationAcceptsExactEmptyMessages(t *testing.T) {
	compiled, err := compileGeneration(generationIntent{})
	require.NoError(t, err)

	observed, err := decodeKernelObservation(kernelObservationMessages{})
	require.NoError(t, err)

	assert.NoError(t, compareKernelObservation(observed, compiled))
}

func TestDecodeKernelObservationRejectsUnexpectedObjectsAndBounds(t *testing.T) {
	compiled, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	valid := kernelMessagesFromCompiled(t, compiled)

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
			assert.Error(t, err)
			assert.ErrorContains(t, err, "kernel observation")
		})
	}
}

func TestDecodeKernelObservationRejectsMessageAndObjectIdentityChanges(t *testing.T) {
	compiled, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	valid := kernelMessagesFromCompiled(t, compiled)

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
		"header length": func(raw *kernelObservationMessages) {
			raw.tableReplies[0].Header.Length++
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
		"table order": func(raw *kernelObservationMessages) {
			raw.tableReplies[0], raw.tableReplies[1] = raw.tableReplies[1], raw.tableReplies[0]
		},
		"chain order": func(raw *kernelObservationMessages) {
			raw.chainReplies[0], raw.chainReplies[1] = raw.chainReplies[1], raw.chainReplies[0]
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
			assert.Error(t, err)
			assert.ErrorContains(t, err, "kernel observation")
		})
	}
}

func TestDecodeKernelObservationRejectsDuplicateAndUnknownTopLevelAttributes(t *testing.T) {
	compiled, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	valid := kernelMessagesFromCompiled(t, compiled)

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
			assert.Error(t, err)
			assert.ErrorContains(t, err, "kernel observation")
		})
	}
}

func TestDecodeKernelObservationRejectsNestedAndExpressionAmbiguity(t *testing.T) {
	compiled, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	valid := kernelMessagesFromCompiled(t, compiled)

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
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw := cloneKernelObservationMessages(valid)
			mutate(&raw)

			_, err := decodeKernelObservation(raw)
			assert.Error(t, err)
			assert.ErrorContains(t, err, "kernel observation")
		})
	}
}

func kernelMessagesFromCompiled(t *testing.T, compiled compiledGeneration) kernelObservationMessages {
	t.Helper()
	if compiled.empty {
		return kernelObservationMessages{}
	}

	result := kernelObservationMessages{}
	tableHandle := uint64(1)
	chainHandle := uint64(100)
	ruleHandle := uint64(1000)
	for _, table := range []compiledTable{compiled.bridge, compiled.inet} {
		result.tableReplies = append(result.tableReplies, testKernelMessage(t, unix.NFT_MSG_NEWTABLE,
			table.table.Family, netlink.Multi, []netlink.Attribute{
				{Type: unix.NFTA_TABLE_NAME, Data: []byte(table.table.Name + "\x00")},
				{Type: unix.NFTA_TABLE_FLAGS, Data: testBigEndianUint32(table.table.Flags)},
				{Type: unix.NFTA_TABLE_USE, Data: testBigEndianUint32(uint32(len(table.chains)))},
				{Type: testNFTATableHandle, Data: testBigEndianUint64(tableHandle)},
			}))
		tableHandle++

		for _, chain := range table.chains {
			hookAttrs := []netlink.Attribute{
				{Type: unix.NFTA_HOOK_HOOKNUM, Data: testBigEndianUint32(uint32(*chain.Hooknum))},
				{Type: unix.NFTA_HOOK_PRIORITY, Data: testBigEndianUint32(uint32(*chain.Priority))},
			}
			policy := nftables.ChainPolicyAccept
			if chain.Policy != nil {
				policy = *chain.Policy
			}
			result.chainReplies = append(result.chainReplies, testKernelMessage(t, unix.NFT_MSG_NEWCHAIN,
				table.table.Family, netlink.Multi, []netlink.Attribute{
					{Type: unix.NFTA_CHAIN_TABLE, Data: []byte(table.table.Name + "\x00")},
					{Type: unix.NFTA_CHAIN_HANDLE, Data: testBigEndianUint64(chainHandle)},
					{Type: unix.NFTA_CHAIN_NAME, Data: []byte(chain.Name + "\x00")},
					{Type: netlink.Nested | unix.NFTA_CHAIN_HOOK, Data: mustMarshalKernelAttributes(t, hookAttrs)},
					{Type: unix.NFTA_CHAIN_POLICY, Data: testBigEndianUint32(uint32(policy))},
					{Type: unix.NFTA_CHAIN_USE, Data: testBigEndianUint32(0)},
					{Type: unix.NFTA_CHAIN_TYPE, Data: []byte(chain.Type + "\x00")},
					{Type: testNFTAChainFlags, Data: testBigEndianUint32(testNFTChainBase)},
				}))
			chainHandle++
		}

		for _, compiledRule := range table.rules {
			expressionAttrs := make([]netlink.Attribute, 0, len(compiledRule.rule.Exprs))
			for _, expression := range compiledRule.rule.Exprs {
				rawExpression, marshalErr := expr.Marshal(byte(table.table.Family), expression)
				require.NoError(t, marshalErr)
				expressionAttrs = append(expressionAttrs, netlink.Attribute{
					Type: netlink.Nested | testNFTAListElem,
					Data: rawExpression,
				})
			}
			result.ruleReplies = append(result.ruleReplies, testKernelMessage(t, unix.NFT_MSG_NEWRULE,
				table.table.Family, netlink.Multi|netlink.Append, []netlink.Attribute{
					{Type: unix.NFTA_RULE_TABLE, Data: []byte(table.table.Name + "\x00")},
					{Type: unix.NFTA_RULE_CHAIN, Data: []byte(compiledRule.rule.Chain.Name + "\x00")},
					{Type: unix.NFTA_RULE_HANDLE, Data: testBigEndianUint64(ruleHandle)},
					{Type: netlink.Nested | unix.NFTA_RULE_EXPRESSIONS,
						Data: mustMarshalKernelAttributes(t, expressionAttrs)},
					{Type: unix.NFTA_RULE_USERDATA, Data: slices.Clone(compiledRule.rule.UserData)},
				}))
			ruleHandle++
		}
	}
	return result
}

func testKernelMessage(
	t *testing.T,
	messageType uint16,
	family nftables.TableFamily,
	flags netlink.HeaderFlags,
	attrs []netlink.Attribute,
) netlink.Message {
	t.Helper()
	data := append([]byte{byte(family), 0, 0, 0}, mustMarshalKernelAttributes(t, attrs)...)
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

func mustMarshalKernelAttributes(t *testing.T, attrs []netlink.Attribute) []byte {
	t.Helper()
	encoded, err := netlink.MarshalAttributes(attrs)
	require.NoError(t, err)
	return encoded
}

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
