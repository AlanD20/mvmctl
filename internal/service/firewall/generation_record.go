package firewall

import (
	"regexp"

	"mvmctl/pkg/errs"
)

const generationRecordSchemaVersion = uint32(1)

var generationBootIDPattern = regexp.MustCompile(
	`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`,
)

type generationRecordLifecycle string

const (
	generationRecordCommitted generationRecordLifecycle = "committed"
	generationRecordApplying  generationRecordLifecycle = "applying"
)

type generationNamespaceIdentity struct {
	device uint64
	inode  uint64
}

type generationRecordBinding struct {
	bootID    string
	namespace generationNamespaceIdentity
}

type generationRecordIdentity struct {
	empty bool
	id    string
}

type generationRecord struct {
	schemaVersion uint32
	lifecycle     generationRecordLifecycle
	binding       generationRecordBinding
	committed     generationRecordIdentity
	before        *generationRecordIdentity
	target        *generationRecordIdentity
}

func newCommittedGenerationRecord(
	binding generationRecordBinding,
	identity generationRecordIdentity,
) (generationRecord, error) {
	record := generationRecord{
		schemaVersion: generationRecordSchemaVersion,
		lifecycle:     generationRecordCommitted,
		binding:       binding,
		committed:     identity,
	}
	if err := validateGenerationRecord(record); err != nil {
		return generationRecord{}, err
	}
	return record, nil
}

func beginGenerationRecordApplication(
	record generationRecord,
	before generationRecordIdentity,
	target generationRecordIdentity,
) (generationRecord, error) {
	if err := validateGenerationRecord(record); err != nil {
		return generationRecord{}, err
	}
	if record.lifecycle != generationRecordCommitted {
		return generationRecord{}, generationAuthorityError("firewall generation record is not committed", nil, nil)
	}
	if record.committed != before {
		return generationRecord{}, generationAuthorityError(
			"prior firewall generation does not match committed authority",
			nil,
			nil,
		)
	}
	if before == target {
		return generationRecord{}, generationAuthorityError(
			"firewall generation application must change generation",
			nil,
			nil,
		)
	}
	result := record
	result.lifecycle = generationRecordApplying
	result.before = &generationRecordIdentity{empty: before.empty, id: before.id}
	result.target = &generationRecordIdentity{empty: target.empty, id: target.id}
	if err := validateGenerationRecord(result); err != nil {
		return generationRecord{}, err
	}
	return result, nil
}

func completeGenerationRecordApplication(
	record generationRecord,
	target generationRecordIdentity,
) (generationRecord, error) {
	if err := validateGenerationRecord(record); err != nil {
		return generationRecord{}, err
	}
	if record.lifecycle != generationRecordApplying || record.target == nil || *record.target != target {
		return generationRecord{}, generationAuthorityError(
			"firewall generation target does not match applying authority",
			nil,
			nil,
		)
	}
	return newCommittedGenerationRecord(record.binding, target)
}

func admitGenerationRecord(record generationRecord, current generationRecordBinding) error {
	if err := validateGenerationRecord(record); err != nil {
		return err
	}
	if err := validateGenerationRecordBinding(current); err != nil {
		return err
	}
	if record.binding.bootID != current.bootID {
		return generationAuthorityError("firewall generation record belongs to another host boot", nil, nil)
	}
	if record.binding.namespace != current.namespace {
		return generationAuthorityError(
			"firewall generation record belongs to another host network namespace",
			nil,
			nil,
		)
	}
	return nil
}

func validateGenerationRecord(record generationRecord) error {
	if record.schemaVersion != generationRecordSchemaVersion {
		return generationAuthorityError("unsupported firewall generation record schema", nil, nil)
	}
	if err := validateGenerationRecordBinding(record.binding); err != nil {
		return err
	}
	if err := validateGenerationRecordIdentity(record.committed); err != nil {
		return err
	}
	switch record.lifecycle {
	case generationRecordCommitted:
		if record.before != nil || record.target != nil {
			return generationAuthorityError(
				"committed firewall generation record contains an applying transition",
				nil,
				nil,
			)
		}
	case generationRecordApplying:
		if record.before == nil || record.target == nil {
			return generationAuthorityError(
				"applying firewall generation record is missing transition authority",
				nil,
				nil,
			)
		}
		if err := validateGenerationRecordIdentity(*record.before); err != nil {
			return err
		}
		if err := validateGenerationRecordIdentity(*record.target); err != nil {
			return err
		}
		if record.committed != *record.before {
			return generationAuthorityError(
				"applying firewall generation record does not retain the committed generation",
				nil,
				nil,
			)
		}
		if *record.before == *record.target {
			return generationAuthorityError(
				"applying firewall generation record does not change generation",
				nil,
				nil,
			)
		}
	default:
		return generationAuthorityError("firewall generation record lifecycle is invalid", nil, nil)
	}
	return nil
}

func validateGenerationRecordBinding(binding generationRecordBinding) error {
	if !generationBootIDPattern.MatchString(binding.bootID) {
		return generationAuthorityError("firewall generation record boot ID is invalid", nil, nil)
	}
	if binding.namespace.device == 0 || binding.namespace.inode == 0 {
		return generationAuthorityError(
			"firewall generation record host namespace identity is invalid",
			nil,
			nil,
		)
	}
	return nil
}

func validateGenerationRecordIdentity(identity generationRecordIdentity) error {
	if err := validateGenerationID(identity.id); err != nil {
		return generationAuthorityError("firewall generation record identity is invalid", err, nil)
	}
	return nil
}

func generationRecordsEqual(left generationRecord, right generationRecord) bool {
	if left.schemaVersion != right.schemaVersion || left.lifecycle != right.lifecycle ||
		left.binding != right.binding || left.committed != right.committed {
		return false
	}
	return generationRecordIdentityPointersEqual(left.before, right.before) &&
		generationRecordIdentityPointersEqual(left.target, right.target)
}

func generationRecordIdentityPointersEqual(
	left *generationRecordIdentity,
	right *generationRecordIdentity,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func generationAuthorityError(message string, cause error, details map[string]any) *errs.DomainError {
	options := []errs.ErrorOption{errs.WithClass(errs.ClassInternal)}
	if details != nil {
		options = append(options, errs.WithDetails(details))
	}
	if cause == nil {
		return errs.New(errs.CodeNetworkFirewallFailed, message, options...)
	}
	return errs.WrapMsg(errs.CodeNetworkFirewallFailed, message, cause, options...)
}
