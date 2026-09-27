package firewall

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf8"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	netlinkHeaderBytes   = 16
	netfilterHeaderBytes = 4

	maxKernelObservationBytes              = 32 << 20
	maxKernelObservationMessageBytes       = 64 << 10
	maxKernelObservationMessages           = 20_000
	maxKernelObservationRules              = 16_384
	maxKernelObservationExpressions        = 262_144
	maxKernelObservationExpressionsPerRule = 32
	maxKernelObservationAttributes         = 64
	maxKernelObservationStringBytes        = 255
	maxKernelObservationUserdataBytes      = 512

	kernelNetfilterSubsystemType = unix.NFNL_SUBSYS_NFTABLES << 8
	kernelNFTATableHandle        = uint16(4)
	kernelNFTATablePad           = uint16(5)
	kernelNFTAChainFlags         = uint16(10)
	// Linux UAPI defines NFTA_BITWISE_OP, but x/sys v0.45.0 does not expose it.
	kernelNFTABitwiseOp = uint16(6)
	kernelNFTChainBase  = uint32(1)
)

type kernelObservationMessages struct {
	tableReplies     []netlink.Message
	chainReplies     []netlink.Message
	ruleReplies      []netlink.Message
	setReplies       []netlink.Message
	objectReplies    []netlink.Message
	flowtableReplies []netlink.Message
}

type kernelAttributeRule struct {
	flags      uint16
	required   bool
	zeroLength bool
}

type decodedKernelTable struct {
	use uint32
}

type kernelBaseSequence struct {
	set   bool
	value uint16
}

type decodedKernelRuleChain struct {
	lastHandle uint64
	ruleCount  uint32
}

// CRITICAL: This parser exists because google/nftables v0.3.0 silently skips expression names it does not know.
// It accepts only the raw grammar emitted by the closed compiler and never opens a socket or mutates kernel state.
func decodeKernelObservation(raw kernelObservationMessages) (kernelObservation, error) {
	if err := validateKernelObservationCaps(raw); err != nil {
		return kernelObservation{}, err
	}
	if len(raw.setReplies) != 0 || len(raw.objectReplies) != 0 || len(raw.flowtableReplies) != 0 {
		return kernelObservation{}, invalidKernelObservation("managed tables contain a set, object, or flowtable")
	}

	sequence := kernelBaseSequence{}
	observed, tables, err := decodeKernelTables(raw.tableReplies, &sequence)
	if err != nil {
		return kernelObservation{}, err
	}
	if err := decodeKernelChains(raw.chainReplies, tables, &observed, &sequence); err != nil {
		return kernelObservation{}, err
	}
	if err := decodeKernelRules(raw.ruleReplies, &observed, &sequence); err != nil {
		return kernelObservation{}, err
	}
	observed.nftBaseSequence = sequence.value
	return observed, nil
}

func validateKernelObservationCaps(raw kernelObservationMessages) error {
	groups := [...][]netlink.Message{
		raw.tableReplies,
		raw.chainReplies,
		raw.ruleReplies,
		raw.setReplies,
		raw.objectReplies,
		raw.flowtableReplies,
	}
	totalMessages := 0
	totalBytes := 0
	for _, messages := range groups {
		if len(messages) > maxKernelObservationMessages-totalMessages {
			return invalidKernelObservation("message count exceeds limit")
		}
		totalMessages += len(messages)
		for _, message := range messages {
			messageBytes := netlinkHeaderBytes + len(message.Data)
			if messageBytes > maxKernelObservationMessageBytes {
				return invalidKernelObservation("message size exceeds limit")
			}
			if messageBytes > maxKernelObservationBytes-totalBytes {
				return invalidKernelObservation("total message bytes exceed limit")
			}
			totalBytes += messageBytes
		}
	}
	if len(raw.tableReplies) > len(managedTableIdentities) {
		return invalidKernelObservation("table object count exceeds limit")
	}
	if len(raw.chainReplies) > 5 {
		return invalidKernelObservation("chain object count exceeds limit")
	}
	if len(raw.ruleReplies) > maxKernelObservationRules {
		return invalidKernelObservation("rule object count exceeds limit")
	}
	return nil
}

func decodeKernelTables(
	messages []netlink.Message,
	sequence *kernelBaseSequence,
) (kernelObservation, []decodedKernelTable, error) {
	if len(messages) == 0 {
		return kernelObservation{empty: true}, nil, nil
	}
	if len(messages) != len(managedTableIdentities) {
		return kernelObservation{}, nil, invalidKernelObservation("managed table observation is partial")
	}

	observed := kernelObservation{tables: make([]kernelObservedTable, 0, len(messages))}
	decoded := make([]decodedKernelTable, 0, len(messages))
	handles := make(map[uint64]struct{}, len(messages))
	for index, message := range messages {
		expected := managedTableIdentities[index]
		family, baseSequence, attrs, err := decodeKernelMessage(
			message,
			unix.NFT_MSG_NEWTABLE,
			0,
			"table",
			map[uint16]kernelAttributeRule{
				unix.NFTA_TABLE_NAME:  {required: true},
				unix.NFTA_TABLE_FLAGS: {required: true},
				unix.NFTA_TABLE_USE:   {required: true},
				kernelNFTATableHandle: {required: true},
				kernelNFTATablePad:    {zeroLength: true},
			},
		)
		if err != nil {
			return kernelObservation{}, nil, err
		}
		if err := sequence.observe(baseSequence); err != nil {
			return kernelObservation{}, nil, err
		}
		name, err := kernelAttributeString(attrs[unix.NFTA_TABLE_NAME], "table name")
		if err != nil {
			return kernelObservation{}, nil, err
		}
		flags, err := kernelAttributeUint32(attrs[unix.NFTA_TABLE_FLAGS], "table flags")
		if err != nil {
			return kernelObservation{}, nil, err
		}
		use, err := kernelAttributeUint32(attrs[unix.NFTA_TABLE_USE], "table use")
		if err != nil {
			return kernelObservation{}, nil, err
		}
		handle, err := kernelAttributeUint64(attrs[kernelNFTATableHandle], "table handle")
		if err != nil {
			return kernelObservation{}, nil, err
		}
		if family != expected.family || name != expected.name || flags != 0 || handle == 0 {
			return kernelObservation{}, nil, invalidKernelObservation("table family, name, flags, or handle is invalid")
		}
		if _, exists := handles[handle]; exists {
			return kernelObservation{}, nil, invalidKernelObservation("table handle is duplicated")
		}
		handles[handle] = struct{}{}
		observed.tables = append(observed.tables, kernelObservedTable{family: family, name: name, flags: flags})
		decoded = append(decoded, decodedKernelTable{use: use})
	}
	return observed, decoded, nil
}

func decodeKernelChains(
	messages []netlink.Message,
	tables []decodedKernelTable,
	observed *kernelObservation,
	sequence *kernelBaseSequence,
) error {
	if len(tables) == 0 {
		if len(messages) != 0 {
			return invalidKernelObservation("chains exist without managed tables")
		}
		return nil
	}
	if len(messages) != 5 {
		return invalidKernelObservation("managed chain observation is partial")
	}

	handles := make([]map[uint64]struct{}, len(observed.tables))
	names := make([]map[string]struct{}, len(observed.tables))
	for index := range observed.tables {
		handles[index] = make(map[uint64]struct{})
		names[index] = make(map[string]struct{})
	}
	lastTableIndex := 0
	for _, message := range messages {
		family, baseSequence, attrs, err := decodeKernelMessage(
			message,
			unix.NFT_MSG_NEWCHAIN,
			0,
			"chain",
			map[uint16]kernelAttributeRule{
				unix.NFTA_CHAIN_TABLE:  {required: true},
				unix.NFTA_CHAIN_HANDLE: {required: true},
				unix.NFTA_CHAIN_NAME:   {required: true},
				unix.NFTA_CHAIN_HOOK:   {required: true},
				unix.NFTA_CHAIN_POLICY: {required: true},
				unix.NFTA_CHAIN_USE:    {required: true},
				unix.NFTA_CHAIN_TYPE:   {required: true},
				kernelNFTAChainFlags:   {required: true},
				unix.NFTA_CHAIN_PAD:    {zeroLength: true},
			},
		)
		if err != nil {
			return err
		}
		if err := sequence.observe(baseSequence); err != nil {
			return err
		}
		tableName, err := kernelAttributeString(attrs[unix.NFTA_CHAIN_TABLE], "chain table")
		if err != nil {
			return err
		}
		tableIndex, exists := kernelObservedTableIndex(observed.tables, family, tableName)
		if !exists || tableIndex < lastTableIndex {
			return invalidKernelObservation("chain family, table, or order is invalid")
		}
		lastTableIndex = tableIndex

		handle, err := kernelAttributeUint64(attrs[unix.NFTA_CHAIN_HANDLE], "chain handle")
		if err != nil {
			return err
		}
		name, err := kernelAttributeString(attrs[unix.NFTA_CHAIN_NAME], "chain name")
		if err != nil {
			return err
		}
		if handle == 0 {
			return invalidKernelObservation("chain handle is zero")
		}
		if _, exists := handles[tableIndex][handle]; exists {
			return invalidKernelObservation("chain handle is duplicated")
		}
		if _, exists := names[tableIndex][name]; exists {
			return invalidKernelObservation("chain name is duplicated")
		}
		handles[tableIndex][handle] = struct{}{}
		names[tableIndex][name] = struct{}{}

		hook, priority, err := decodeKernelHook(attrs[unix.NFTA_CHAIN_HOOK].Data)
		if err != nil {
			return err
		}
		policyValue, err := kernelAttributeUint32(attrs[unix.NFTA_CHAIN_POLICY], "chain policy")
		if err != nil {
			return err
		}
		if policyValue != uint32(nftables.ChainPolicyAccept) && policyValue != uint32(nftables.ChainPolicyDrop) {
			return invalidKernelObservation("chain policy is invalid")
		}
		use, err := kernelAttributeUint32(attrs[unix.NFTA_CHAIN_USE], "chain use")
		if err != nil {
			return err
		}
		chainType, err := kernelAttributeString(attrs[unix.NFTA_CHAIN_TYPE], "chain type")
		if err != nil {
			return err
		}
		chainFlags, err := kernelAttributeUint32(attrs[kernelNFTAChainFlags], "chain flags")
		if err != nil {
			return err
		}
		if chainFlags != kernelNFTChainBase {
			return invalidKernelObservation("chain flags are invalid")
		}

		observed.tables[tableIndex].chains = append(observed.tables[tableIndex].chains, kernelObservedChain{
			name:            name,
			hook:            nftables.ChainHook(hook),
			hookPresent:     true,
			priority:        nftables.ChainPriority(int32(priority)),
			priorityPresent: true,
			chainType:       nftables.ChainType(chainType),
			policy:          nftables.ChainPolicy(policyValue),
			policyPresent:   true,
			use:             use,
		})
	}
	for index := range observed.tables {
		if uint32(len(observed.tables[index].chains)) != tables[index].use {
			return invalidKernelObservation("table use does not match observed chain count")
		}
	}
	return nil
}

func decodeKernelRules(
	messages []netlink.Message,
	observed *kernelObservation,
	sequence *kernelBaseSequence,
) error {
	if observed.empty {
		if len(messages) != 0 {
			return invalidKernelObservation("rules exist without managed tables")
		}
		return nil
	}

	handles := make([]map[uint64]struct{}, len(observed.tables))
	chains := make([]map[string]*decodedKernelRuleChain, len(observed.tables))
	ruleIDs := make(map[string]struct{}, len(messages))
	for index := range handles {
		handles[index] = make(map[uint64]struct{})
		chains[index] = make(map[string]*decodedKernelRuleChain, len(observed.tables[index].chains))
		for chainIndex := range observed.tables[index].chains {
			chains[index][observed.tables[index].chains[chainIndex].name] = &decodedKernelRuleChain{}
		}
	}
	lastTableIndex := 0
	lastChainIndexes := make([]int, len(observed.tables))
	totalExpressions := 0
	for _, message := range messages {
		family, baseSequence, attrs, err := decodeKernelMessage(
			message,
			unix.NFT_MSG_NEWRULE,
			netlink.Multi|netlink.Append,
			"rule",
			map[uint16]kernelAttributeRule{
				unix.NFTA_RULE_TABLE:       {required: true},
				unix.NFTA_RULE_CHAIN:       {required: true},
				unix.NFTA_RULE_HANDLE:      {required: true},
				unix.NFTA_RULE_EXPRESSIONS: {required: true},
				unix.NFTA_RULE_POSITION:    {},
				unix.NFTA_RULE_USERDATA:    {required: true},
				unix.NFTA_RULE_PAD:         {zeroLength: true},
			},
		)
		if err != nil {
			return err
		}
		if err := sequence.observe(baseSequence); err != nil {
			return err
		}
		tableName, err := kernelAttributeString(attrs[unix.NFTA_RULE_TABLE], "rule table")
		if err != nil {
			return err
		}
		tableIndex, exists := kernelObservedTableIndex(observed.tables, family, tableName)
		if !exists || tableIndex < lastTableIndex {
			return invalidKernelObservation("rule family, table, or order is invalid")
		}
		lastTableIndex = tableIndex
		chainName, err := kernelAttributeString(attrs[unix.NFTA_RULE_CHAIN], "rule chain")
		if err != nil {
			return err
		}
		chainIndex, exists := kernelObservedChainIndex(observed.tables[tableIndex].chains, chainName)
		if !exists {
			return invalidKernelObservation("rule references an unknown chain")
		}
		if chainIndex < lastChainIndexes[tableIndex] {
			return invalidKernelObservation("rule chain order is invalid")
		}
		lastChainIndexes[tableIndex] = chainIndex
		handle, err := kernelAttributeUint64(attrs[unix.NFTA_RULE_HANDLE], "rule handle")
		if err != nil {
			return err
		}
		if handle == 0 {
			return invalidKernelObservation("rule handle is zero")
		}
		if _, exists := handles[tableIndex][handle]; exists {
			return invalidKernelObservation("rule handle is duplicated")
		}
		chainState := chains[tableIndex][chainName]
		position, positionPresent := attrs[unix.NFTA_RULE_POSITION]
		if chainState.ruleCount == 0 && positionPresent {
			return invalidKernelObservation("first rule in chain contains a predecessor position")
		}
		if chainState.ruleCount != 0 {
			if !positionPresent {
				return invalidKernelObservation("non-first rule in chain is missing its predecessor position")
			}
			previousHandle, positionErr := kernelAttributeUint64(position, "rule predecessor position")
			if positionErr != nil {
				return positionErr
			}
			if previousHandle != chainState.lastHandle {
				return invalidKernelObservation("rule predecessor position is invalid")
			}
		}
		handles[tableIndex][handle] = struct{}{}
		chainState.lastHandle = handle
		chainState.ruleCount++

		userdata := attrs[unix.NFTA_RULE_USERDATA].Data
		if len(userdata) == 0 || len(userdata) > maxKernelObservationUserdataBytes {
			return invalidKernelObservation("rule userdata length is invalid")
		}
		identity, err := decodeRuleIdentity(userdata)
		if err != nil {
			return invalidKernelObservation("rule userdata is invalid")
		}
		if observed.generationID == "" {
			observed.generationID = identity.generation
		} else if observed.generationID != identity.generation {
			return invalidKernelObservation("rules contain different generation identities")
		}
		if _, exists := ruleIDs[identity.rule]; exists {
			return invalidKernelObservation("rule identity is duplicated")
		}
		ruleIDs[identity.rule] = struct{}{}

		expressions, err := decodeKernelExpressions(attrs[unix.NFTA_RULE_EXPRESSIONS].Data, family)
		if err != nil {
			return err
		}
		totalExpressions, err = checkedKernelExpressionTotal(totalExpressions, len(expressions))
		if err != nil {
			return err
		}
		observed.tables[tableIndex].rules = append(observed.tables[tableIndex].rules, kernelObservedRule{
			table:       tableIdentity{family: family, name: tableName},
			chain:       chainName,
			userdata:    bytes.Clone(userdata),
			expressions: expressions,
		})
	}
	if observed.generationID == "" {
		return invalidKernelObservation("nonempty managed tables contain no generation-bound rules")
	}
	for tableIndex := range observed.tables {
		for chainIndex := range observed.tables[tableIndex].chains {
			chain := observed.tables[tableIndex].chains[chainIndex]
			if chains[tableIndex][chain.name].ruleCount != chain.use {
				return invalidKernelObservation("chain use does not match observed rule count")
			}
		}
	}
	return nil
}

func checkedKernelExpressionTotal(total int, additional int) (int, error) {
	if additional > maxKernelObservationExpressions-total {
		return 0, invalidKernelObservation("expression object count exceeds limit")
	}
	return total + additional, nil
}

func decodeKernelMessage(
	message netlink.Message,
	responseType uint16,
	responseFlags netlink.HeaderFlags,
	objectName string,
	rules map[uint16]kernelAttributeRule,
) (nftables.TableFamily, uint16, map[uint16]netlink.Attribute, error) {
	if message.Header.Length != uint32(netlinkHeaderBytes+len(message.Data)) {
		return 0, 0, nil, invalidKernelObservation(objectName + " response length is invalid")
	}
	wantType := netlink.HeaderType(kernelNetfilterSubsystemType | responseType)
	if message.Header.Type != wantType || message.Header.Flags != responseFlags {
		return 0, 0, nil, invalidKernelObservation(objectName + " response type or flags are invalid")
	}
	if len(message.Data) < netfilterHeaderBytes || message.Data[1] != 0 {
		return 0, 0, nil, invalidKernelObservation(objectName + " netfilter header is invalid")
	}
	attrs, err := decodeUniqueKernelAttributes(message.Data[netfilterHeaderBytes:], objectName, rules)
	if err != nil {
		return 0, 0, nil, err
	}
	return nftables.TableFamily(message.Data[0]), binary.BigEndian.Uint16(message.Data[2:4]), attrs, nil
}

func (sequence *kernelBaseSequence) observe(value uint16) error {
	if !sequence.set {
		sequence.set = true
		sequence.value = value
		return nil
	}
	if sequence.value != value {
		return invalidKernelObservation("netfilter base sequence is inconsistent")
	}
	return nil
}

func decodeKernelHook(raw []byte) (uint32, uint32, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "chain hook", map[uint16]kernelAttributeRule{
		unix.NFTA_HOOK_HOOKNUM:  {required: true},
		unix.NFTA_HOOK_PRIORITY: {required: true},
	})
	if err != nil {
		return 0, 0, err
	}
	hook, err := kernelAttributeUint32(attrs[unix.NFTA_HOOK_HOOKNUM], "chain hook number")
	if err != nil {
		return 0, 0, err
	}
	priority, err := kernelAttributeUint32(attrs[unix.NFTA_HOOK_PRIORITY], "chain hook priority")
	if err != nil {
		return 0, 0, err
	}
	return hook, priority, nil
}

func decodeKernelExpressions(raw []byte, family nftables.TableFamily) ([]expr.Any, error) {
	attrs, err := decodeKernelAttributeList(raw, "expression list")
	if err != nil {
		return nil, err
	}
	if len(attrs) == 0 || len(attrs) > maxKernelObservationExpressionsPerRule {
		return nil, invalidKernelObservation("expression count per rule is invalid")
	}
	result := make([]expr.Any, 0, len(attrs))
	for _, attr := range attrs {
		if kernelAttributeType(attr) != unix.NFTA_LIST_ELEM || kernelAttributeFlags(attr) != 0 {
			return nil, invalidKernelObservation("expression list contains an unknown attribute")
		}
		expressionAttrs, err := decodeUniqueKernelAttributes(attr.Data, "expression", map[uint16]kernelAttributeRule{
			unix.NFTA_EXPR_NAME: {required: true},
			unix.NFTA_EXPR_DATA: {required: true},
		})
		if err != nil {
			return nil, err
		}
		name, err := kernelAttributeString(expressionAttrs[unix.NFTA_EXPR_NAME], "expression name")
		if err != nil {
			return nil, err
		}
		decoded, err := decodeKernelExpression(name, expressionAttrs[unix.NFTA_EXPR_DATA].Data, family)
		if err != nil {
			return nil, err
		}
		result = append(result, decoded)
	}
	return result, nil
}

func decodeKernelExpression(name string, raw []byte, family nftables.TableFamily) (expr.Any, error) {
	switch name {
	case "meta":
		return decodeKernelMeta(raw)
	case "masq":
		return decodeKernelMasq(raw)
	case "bitwise":
		return decodeKernelBitwise(raw)
	case "cmp":
		return decodeKernelComparison(raw)
	case "ct":
		return decodeKernelConntrack(raw)
	case "payload":
		return decodeKernelPayload(raw)
	case "range":
		return decodeKernelRange(raw)
	case "immediate":
		return decodeKernelVerdict(raw)
	default:
		return nil, invalidKernelObservation(fmt.Sprintf("unknown expression %q for family %d", name, family))
	}
}

func decodeKernelMeta(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "meta expression", map[uint16]kernelAttributeRule{
		unix.NFTA_META_DREG: {required: true},
		unix.NFTA_META_KEY:  {required: true},
	})
	if err != nil {
		return nil, err
	}
	register, err := kernelAttributeUint32(attrs[unix.NFTA_META_DREG], "meta destination register")
	if err != nil {
		return nil, err
	}
	key, err := kernelAttributeUint32(attrs[unix.NFTA_META_KEY], "meta key")
	if err != nil {
		return nil, err
	}
	return &expr.Meta{Key: expr.MetaKey(key), Register: register}, nil
}

func decodeKernelMasq(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "masquerade expression", map[uint16]kernelAttributeRule{})
	if err != nil {
		return nil, err
	}
	if len(attrs) != 0 {
		return nil, invalidKernelObservation("masquerade expression contains attributes")
	}
	return &expr.Masq{}, nil
}

func decodeKernelBitwise(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "bitwise expression", map[uint16]kernelAttributeRule{
		unix.NFTA_BITWISE_SREG: {required: true},
		unix.NFTA_BITWISE_DREG: {required: true},
		unix.NFTA_BITWISE_LEN:  {required: true},
		unix.NFTA_BITWISE_MASK: {required: true},
		unix.NFTA_BITWISE_XOR:  {required: true},
		kernelNFTABitwiseOp:    {},
	})
	if err != nil {
		return nil, err
	}
	if attribute, present := attrs[kernelNFTABitwiseOp]; present {
		operation, err := kernelAttributeUint32(attribute, "bitwise operation")
		if err != nil {
			return nil, err
		}
		if operation != unix.NFT_BITWISE_BOOL {
			return nil, invalidKernelObservation("bitwise operation is not boolean mask/xor")
		}
	}
	source, err := kernelAttributeUint32(attrs[unix.NFTA_BITWISE_SREG], "bitwise source register")
	if err != nil {
		return nil, err
	}
	destination, err := kernelAttributeUint32(attrs[unix.NFTA_BITWISE_DREG], "bitwise destination register")
	if err != nil {
		return nil, err
	}
	length, err := kernelAttributeUint32(attrs[unix.NFTA_BITWISE_LEN], "bitwise length")
	if err != nil {
		return nil, err
	}
	mask, err := decodeKernelDataValue(attrs[unix.NFTA_BITWISE_MASK].Data, "bitwise mask")
	if err != nil {
		return nil, err
	}
	xor, err := decodeKernelDataValue(attrs[unix.NFTA_BITWISE_XOR].Data, "bitwise xor")
	if err != nil {
		return nil, err
	}
	return &expr.Bitwise{SourceRegister: source, DestRegister: destination, Len: length, Mask: mask, Xor: xor}, nil
}

func decodeKernelComparison(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "comparison expression", map[uint16]kernelAttributeRule{
		unix.NFTA_CMP_SREG: {required: true},
		unix.NFTA_CMP_OP:   {required: true},
		unix.NFTA_CMP_DATA: {required: true},
	})
	if err != nil {
		return nil, err
	}
	register, err := kernelAttributeUint32(attrs[unix.NFTA_CMP_SREG], "comparison source register")
	if err != nil {
		return nil, err
	}
	operation, err := kernelAttributeUint32(attrs[unix.NFTA_CMP_OP], "comparison operation")
	if err != nil {
		return nil, err
	}
	data, err := decodeKernelDataValue(attrs[unix.NFTA_CMP_DATA].Data, "comparison data")
	if err != nil {
		return nil, err
	}
	return &expr.Cmp{Op: expr.CmpOp(operation), Register: register, Data: data}, nil
}

func decodeKernelConntrack(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "conntrack expression", map[uint16]kernelAttributeRule{
		unix.NFTA_CT_DREG: {required: true},
		unix.NFTA_CT_KEY:  {required: true},
	})
	if err != nil {
		return nil, err
	}
	register, err := kernelAttributeUint32(attrs[unix.NFTA_CT_DREG], "conntrack destination register")
	if err != nil {
		return nil, err
	}
	key, err := kernelAttributeUint32(attrs[unix.NFTA_CT_KEY], "conntrack key")
	if err != nil {
		return nil, err
	}
	return &expr.Ct{Register: register, Key: expr.CtKey(key)}, nil
}

func decodeKernelPayload(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "payload expression", map[uint16]kernelAttributeRule{
		unix.NFTA_PAYLOAD_DREG:   {required: true},
		unix.NFTA_PAYLOAD_BASE:   {required: true},
		unix.NFTA_PAYLOAD_OFFSET: {required: true},
		unix.NFTA_PAYLOAD_LEN:    {required: true},
	})
	if err != nil {
		return nil, err
	}
	register, err := kernelAttributeUint32(attrs[unix.NFTA_PAYLOAD_DREG], "payload destination register")
	if err != nil {
		return nil, err
	}
	base, err := kernelAttributeUint32(attrs[unix.NFTA_PAYLOAD_BASE], "payload base")
	if err != nil {
		return nil, err
	}
	offset, err := kernelAttributeUint32(attrs[unix.NFTA_PAYLOAD_OFFSET], "payload offset")
	if err != nil {
		return nil, err
	}
	length, err := kernelAttributeUint32(attrs[unix.NFTA_PAYLOAD_LEN], "payload length")
	if err != nil {
		return nil, err
	}
	return &expr.Payload{
		DestRegister: register,
		Base:         expr.PayloadBase(base),
		Offset:       offset,
		Len:          length,
	}, nil
}

func decodeKernelRange(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "range expression", map[uint16]kernelAttributeRule{
		unix.NFTA_RANGE_SREG:      {required: true},
		unix.NFTA_RANGE_OP:        {required: true},
		unix.NFTA_RANGE_FROM_DATA: {required: true},
		unix.NFTA_RANGE_TO_DATA:   {required: true},
	})
	if err != nil {
		return nil, err
	}
	register, err := kernelAttributeUint32(attrs[unix.NFTA_RANGE_SREG], "range source register")
	if err != nil {
		return nil, err
	}
	operation, err := kernelAttributeUint32(attrs[unix.NFTA_RANGE_OP], "range operation")
	if err != nil {
		return nil, err
	}
	from, err := decodeKernelDataValue(attrs[unix.NFTA_RANGE_FROM_DATA].Data, "range lower bound")
	if err != nil {
		return nil, err
	}
	to, err := decodeKernelDataValue(attrs[unix.NFTA_RANGE_TO_DATA].Data, "range upper bound")
	if err != nil {
		return nil, err
	}
	return &expr.Range{Op: expr.CmpOp(operation), Register: register, FromData: from, ToData: to}, nil
}

func decodeKernelVerdict(raw []byte) (expr.Any, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, "verdict immediate expression", map[uint16]kernelAttributeRule{
		unix.NFTA_IMMEDIATE_DREG: {required: true},
		unix.NFTA_IMMEDIATE_DATA: {required: true},
	})
	if err != nil {
		return nil, err
	}
	register, err := kernelAttributeUint32(attrs[unix.NFTA_IMMEDIATE_DREG], "verdict destination register")
	if err != nil {
		return nil, err
	}
	if register != unix.NFT_REG_VERDICT {
		return nil, invalidKernelObservation("immediate expression does not target the verdict register")
	}
	dataAttrs, err := decodeUniqueKernelAttributes(attrs[unix.NFTA_IMMEDIATE_DATA].Data, "verdict data",
		map[uint16]kernelAttributeRule{unix.NFTA_DATA_VERDICT: {required: true}})
	if err != nil {
		return nil, err
	}
	verdictAttrs, err := decodeUniqueKernelAttributes(dataAttrs[unix.NFTA_DATA_VERDICT].Data, "verdict",
		map[uint16]kernelAttributeRule{unix.NFTA_VERDICT_CODE: {required: true}})
	if err != nil {
		return nil, err
	}
	code, err := kernelAttributeUint32(verdictAttrs[unix.NFTA_VERDICT_CODE], "verdict code")
	if err != nil {
		return nil, err
	}
	kind := expr.VerdictKind(int32(code))
	if kind != expr.VerdictAccept && kind != expr.VerdictDrop {
		return nil, invalidKernelObservation("verdict kind is not emitted by the compiler")
	}
	return &expr.Verdict{Kind: kind}, nil
}

func decodeKernelDataValue(raw []byte, objectName string) ([]byte, error) {
	attrs, err := decodeUniqueKernelAttributes(raw, objectName,
		map[uint16]kernelAttributeRule{unix.NFTA_DATA_VALUE: {required: true}})
	if err != nil {
		return nil, err
	}
	value := attrs[unix.NFTA_DATA_VALUE].Data
	if len(value) == 0 || len(value) > 64 {
		return nil, invalidKernelObservation(objectName + " length is invalid")
	}
	return bytes.Clone(value), nil
}

func decodeUniqueKernelAttributes(
	raw []byte,
	objectName string,
	rules map[uint16]kernelAttributeRule,
) (map[uint16]netlink.Attribute, error) {
	attrs, err := decodeKernelAttributeList(raw, objectName)
	if err != nil {
		return nil, err
	}
	result := make(map[uint16]netlink.Attribute, len(attrs))
	for _, attr := range attrs {
		attrType := kernelAttributeType(attr)
		rule, allowed := rules[attrType]
		if !allowed || kernelAttributeFlags(attr) != rule.flags {
			return nil, invalidKernelObservation(fmt.Sprintf("%s contains unknown attribute %d", objectName, attrType))
		}
		if _, exists := result[attrType]; exists {
			return nil, invalidKernelObservation(
				fmt.Sprintf("%s contains duplicate attribute %d", objectName, attrType),
			)
		}
		if rule.zeroLength && len(attr.Data) != 0 {
			return nil, invalidKernelObservation(
				fmt.Sprintf("%s padding attribute %d is not empty", objectName, attrType),
			)
		}
		result[attrType] = attr
	}
	for attrType, rule := range rules {
		if _, exists := result[attrType]; rule.required && !exists {
			return nil, invalidKernelObservation(fmt.Sprintf("%s is missing attribute %d", objectName, attrType))
		}
	}
	return result, nil
}

func decodeKernelAttributeList(raw []byte, objectName string) ([]netlink.Attribute, error) {
	if err := validateKernelAttributeWire(raw, objectName); err != nil {
		return nil, err
	}
	attrs, err := netlink.UnmarshalAttributes(raw)
	if err != nil {
		return nil, invalidKernelObservation(fmt.Sprintf("cannot decode %s attributes: %v", objectName, err))
	}
	if len(attrs) > maxKernelObservationAttributes {
		return nil, invalidKernelObservation(objectName + " attribute count exceeds limit")
	}
	return attrs, nil
}

func validateKernelAttributeWire(raw []byte, objectName string) error {
	attributeCount := 0
	for offset := 0; offset < len(raw); {
		if len(raw)-offset < 4 {
			return invalidKernelObservation(objectName + " contains a truncated attribute header")
		}
		length := int(binary.NativeEndian.Uint16(raw[offset : offset+2]))
		if length < 4 || length > len(raw)-offset {
			return invalidKernelObservation(objectName + " contains an invalid attribute length")
		}
		attributeCount++
		if attributeCount > maxKernelObservationAttributes {
			return invalidKernelObservation(objectName + " attribute count exceeds limit")
		}
		alignedLength := (length + 3) &^ 3
		if alignedLength > len(raw)-offset {
			return invalidKernelObservation(objectName + " contains truncated attribute padding")
		}
		for _, padding := range raw[offset+length : offset+alignedLength] {
			if padding != 0 {
				return invalidKernelObservation(objectName + " contains nonzero attribute padding")
			}
		}
		offset += alignedLength
	}
	return nil
}

func kernelAttributeString(attr netlink.Attribute, objectName string) (string, error) {
	if len(attr.Data) < 2 || len(attr.Data) > maxKernelObservationStringBytes || attr.Data[len(attr.Data)-1] != 0 ||
		bytes.IndexByte(attr.Data[:len(attr.Data)-1], 0) >= 0 || !utf8.Valid(attr.Data[:len(attr.Data)-1]) {
		return "", invalidKernelObservation(objectName + " is not an exact NUL-terminated string")
	}
	return string(attr.Data[:len(attr.Data)-1]), nil
}

func kernelAttributeUint32(attr netlink.Attribute, objectName string) (uint32, error) {
	if len(attr.Data) != 4 {
		return 0, invalidKernelObservation(objectName + " is not a uint32")
	}
	return binary.BigEndian.Uint32(attr.Data), nil
}

func kernelAttributeUint64(attr netlink.Attribute, objectName string) (uint64, error) {
	if len(attr.Data) != 8 {
		return 0, invalidKernelObservation(objectName + " is not a uint64")
	}
	return binary.BigEndian.Uint64(attr.Data), nil
}

func kernelAttributeType(attr netlink.Attribute) uint16 {
	return attr.Type & ^uint16(netlink.Nested|netlink.NetByteOrder)
}

func kernelAttributeFlags(attr netlink.Attribute) uint16 {
	return attr.Type & uint16(netlink.Nested|netlink.NetByteOrder)
}

func kernelObservedTableIndex(
	tables []kernelObservedTable,
	family nftables.TableFamily,
	name string,
) (int, bool) {
	for index := range tables {
		if tables[index].family == family && tables[index].name == name {
			return index, true
		}
	}
	return 0, false
}

func kernelObservedChainIndex(chains []kernelObservedChain, name string) (int, bool) {
	for index := range chains {
		if chains[index].name == name {
			return index, true
		}
	}
	return 0, false
}
