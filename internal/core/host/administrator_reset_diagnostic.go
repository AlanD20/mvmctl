package host

import (
	"strconv"
	"strings"

	"mvmctl/pkg/errs"
)

const resetFailureCauseLimit = 512

// resetFailureCause walks masked domain causes and joined guard failures without
// exposing metadata. Process messages may contain argv/stderr and are omitted.
func resetFailureCause(cause error) string {
	const maxNodes = 32
	pending := []error{cause}
	var parts []string
	for visited := 0; visited < maxNodes && len(pending) != 0; visited++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		if domain, ok := current.(*errs.DomainError); ok {
			message := string(domain.Code)
			if domain.Code != errs.CodeProcessError {
				message += ": " + domain.Message
			}
			parts = append(parts, boundedResetFailureText(message))
			pending = append(pending, domain.Err)
			continue
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			children := wrapped.Unwrap()
			count := min(len(children), maxNodes-visited-1-len(pending))
			for index := count - 1; index >= 0; index-- {
				pending = append(pending, children[index])
			}
		case interface{ Unwrap() error }:
			// A wrapper's Error may copy a process message, including command output.
			pending = append(pending, wrapped.Unwrap())
		default:
			if exited, ok := current.(interface{ ExitCode() int }); ok {
				parts = append(parts, "process exit "+strconv.Itoa(exited.ExitCode()))
			} else {
				parts = append(parts, boundedResetFailureText(current.Error()))
			}
		}
	}
	return truncateResetFailureText(strings.Join(parts, "; "))
}

func boundedResetFailureText(text string) string {
	// Quote escapes terminal control bytes and invalid UTF-8. Bound before and
	// after escaping so a long cause cannot grow the public diagnostic unchecked.
	text = truncateResetFailureText(text)
	quoted := strconv.Quote(text)
	return truncateResetFailureText(quoted[1 : len(quoted)-1])
}

func truncateResetFailureText(text string) string {
	if len(text) <= resetFailureCauseLimit {
		return text
	}
	limit := resetFailureCauseLimit - len("...")
	for limit > 0 && text[limit]&0xc0 == 0x80 {
		limit--
	}
	return text[:limit] + "..."
}
