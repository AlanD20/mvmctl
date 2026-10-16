package common

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"mvmctl/pkg/errs"
)

// OutputError preserves the domain error contract at the CLI boundary.
type OutputError struct {
	Code    errs.Code      `json:"code"`
	Class   errs.Class     `json:"class"`
	Message string         `json:"message"`
	Op      string         `json:"op,omitempty"`
	Entity  string         `json:"entity,omitempty"`
	Details map[string]any `json:"details"`
}

// OutputEnvelope is the versioned public CLI response for reads and mutations.
type OutputEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	// Data is an intentional sum type containing the command's typed API result.
	Data  any          `json:"data"`
	Error *OutputError `json:"error"`
}

// A CLI process executes one command at a time. Concurrent commands in the same
// process must not share this display session or the existing Cli singleton.
var outputSession struct {
	enabled bool
	data    any
	partial bool
}

// BeginOutput starts a display session before initialization, so startup and
// argument errors use the same envelope. Arguments after -- belong to the guest.
func BeginOutput(args []string) {
	outputSession.enabled = false
	outputSession.data = nil
	outputSession.partial = false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		switch arg {
		case "--json", "--json=true", "--json=1", "--json=t", "--json=T", "--json=TRUE", "--json=True":
			outputSession.enabled = true
		case "--json=false", "--json=0", "--json=f", "--json=F", "--json=FALSE", "--json=False":
			outputSession.enabled = false
		}
	}
}

// StructuredOutput reports whether this invocation requested a JSON envelope.
func StructuredOutput() bool { return outputSession.enabled }

// RecordResult supplies the actual API result without interpreting its fields.
// The return value lets a handler skip its human-readable renderer.
func RecordResult(data any) bool {
	if !StructuredOutput() {
		return false
	}
	outputSession.data = data
	return true
}

// RecordListResult keeps empty list responses as arrays in the public JSON envelope.
func RecordListResult[T any](items []T) bool {
	if items == nil {
		items = []T{}
	}
	return RecordResult(items)
}

// RecordPartialResult records completed work when the caller also returns an
// error. The caller determines partial completion from its typed result.
func RecordPartialResult(data any) {
	if RecordResult(data) {
		outputSession.partial = true
	}
}

// RecordBatch preserves every item and its structured failure, including the
// underlying DomainError details that OperationResult's JSON marshaler omits.
func RecordBatch(batch *errs.BatchResult) error {
	type item struct {
		Result *errs.OperationResult `json:"result"`
		Error  *OutputError          `json:"error"`
	}
	data := struct {
		Items    []item         `json:"items"`
		Warnings []string       `json:"warnings"`
		Metadata map[string]any `json:"metadata"`
	}{Items: make([]item, 0, len(batch.Items)), Warnings: batch.Warnings, Metadata: batch.Metadata}
	failed, succeeded := 0, 0
	for i := range batch.Items {
		r := &batch.Items[i]
		entry := item{Result: r}
		if r.IsError() {
			failed++
			err := error(r.ToError())
			var domainError *errs.DomainError
			if errors.As(r.Exception, &domainError) {
				err = r.Exception
			}
			entry.Error = outputError(err)
		} else {
			succeeded++
		}
		data.Items = append(data.Items, entry)
	}
	RecordResult(data)
	outputSession.partial = failed > 0 && succeeded > 0
	if failed > 0 {
		return errs.New("cli.batch.failed", "one or more operations failed", errs.WithClass(errs.ClassInternal))
	}
	return nil
}

// UnsupportedOutput rejects commands whose terminal or raw stream protocol
// cannot be represented by one finite structured response, before their effects.
func UnsupportedOutput(command string) error {
	return errs.New("cli.output.unsupported", "structured output is not supported for "+command,
		errs.WithClass(errs.ClassValidation))
}

// FinishOutput emits exactly one envelope and returns the original operation
// error. Encoding completes before writing so serialization errors emit no fragment.
func FinishOutput(w io.Writer, err error) error {
	if !StructuredOutput() {
		return err
	}
	status := "success"
	if err != nil {
		status = "error"
		if outputSession.partial {
			status = "partial"
		}
	}
	envelope := OutputEnvelope{SchemaVersion: 1, Status: status, Data: outputSession.data, Error: outputError(err)}
	b, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		err = errs.WrapMsg("cli.output.encode_failed", "cannot encode command result", marshalErr,
			errs.WithClass(errs.ClassInternal))
		b, marshalErr = json.Marshal(OutputEnvelope{SchemaVersion: 1, Status: "error", Error: outputError(err)})
		if marshalErr != nil {
			return marshalErr
		}
	}
	if _, writeErr := w.Write(append(b, '\n')); writeErr != nil {
		return writeErr
	}
	return err
}

func outputError(err error) *OutputError {
	if err == nil {
		return nil
	}
	var de *errs.DomainError
	if errors.As(err, &de) {
		return &OutputError{Code: de.Code, Class: de.Class, Message: de.Message,
			Op: de.Op, Entity: de.Entity, Details: de.Details}
	}
	var interaction *errs.NeedsInteraction
	if errors.As(err, &interaction) {
		return &OutputError{Code: errs.Code(interaction.Code), Class: errs.ClassNeedsInteraction,
			Message: interaction.Message, Details: interaction.Context}
	}
	return &OutputError{Code: "cli.failed", Class: errs.ClassUnknown, Message: strings.TrimSpace(err.Error())}
}
