package firewall

import (
	"bytes"
)

const (
	ruleUserDataMagic = "MVMNFT01"
	// The codec already requires a 64-byte generation ID and a rule ID of at most 255 bytes.
	maxRuleUserDataBytes = len(ruleUserDataMagic) + 2 + 64 + 255
)

type ruleIdentity struct {
	generation string
	rule       string
}

func encodeRuleIdentity(identity ruleIdentity) ([]byte, error) {
	return appendRuleIdentity(nil, identity)
}

func appendRuleIdentity(encoded []byte, identity ruleIdentity) ([]byte, error) {
	if err := validateGenerationID(identity.generation); err != nil {
		return nil, err
	}
	if err := validateRuleID(identity.rule); err != nil {
		return nil, err
	}

	additional := len(ruleUserDataMagic) + 2 + len(identity.generation) + len(identity.rule)
	if additional > cap(encoded)-len(encoded) {
		// A single allocation preserves the large-generation budget under race instrumentation too.
		grown := make([]byte, len(encoded), len(encoded)+additional)
		copy(grown, encoded)
		encoded = grown
	}
	encoded = append(encoded, ruleUserDataMagic...)
	encoded = append(encoded, byte(len(identity.generation)), byte(len(identity.rule)))
	encoded = append(encoded, identity.generation...)
	encoded = append(encoded, identity.rule...)
	return encoded, nil
}

func decodeRuleIdentity(encoded []byte) (ruleIdentity, error) {
	const headerLength = len(ruleUserDataMagic) + 2
	if len(encoded) < headerLength || !bytes.Equal(encoded[:len(ruleUserDataMagic)], []byte(ruleUserDataMagic)) {
		return ruleIdentity{}, invalidGeneration("invalid mvmctl rule userdata header")
	}

	generationLength := int(encoded[len(ruleUserDataMagic)])
	ruleLength := int(encoded[len(ruleUserDataMagic)+1])
	if len(encoded) != headerLength+generationLength+ruleLength {
		return ruleIdentity{}, invalidGeneration("invalid mvmctl rule userdata length")
	}

	identity := ruleIdentity{
		generation: string(encoded[headerLength : headerLength+generationLength]),
		rule:       string(encoded[headerLength+generationLength:]),
	}
	if err := validateGenerationID(identity.generation); err != nil {
		return ruleIdentity{}, err
	}
	if err := validateRuleID(identity.rule); err != nil {
		return ruleIdentity{}, err
	}
	return identity, nil
}
