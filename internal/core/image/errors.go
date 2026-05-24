package image

import (
	"fmt"

	"mvmctl/internal/infra/errs"
)

// NewImageError creates a general image operation failure error.
func NewImageError(msg string) error {
	return &errs.DomainError{Code: errs.CodeImageError, Message: msg}
}

// NewImageCompressionError creates an image compression failure error.
func NewImageCompressionError(msg string) error {
	return &errs.DomainError{Code: errs.CodeImageCompressionError, Message: msg}
}

// NewImageDecompressionError creates an image decompression failure error.
func NewImageDecompressionError(msg string) error {
	return &errs.DomainError{Code: errs.CodeImageDecompressionError, Message: msg}
}

// NewImageCorruptError creates an image corruption error.
func NewImageCorruptError(msg string) error {
	return &errs.DomainError{Code: errs.CodeImageCorrupt, Message: msg}
}

// NewImageEmptyError creates an empty image error.
func NewImageEmptyError(msg string) error {
	return &errs.DomainError{Code: errs.CodeImageEmpty, Message: msg}
}

// NewImageValidationError creates an image format validation error.
func NewImageValidationError(msg string) error {
	return &errs.DomainError{Code: errs.CodeImageFormatInvalid, Message: msg}
}

// NewImageNotFoundError creates an image not found error.
func NewImageNotFoundError(msg string) error {
	return &errs.DomainError{Code: errs.CodeImageNotFound, Message: msg}
}

// NewImageChecksumMismatchError creates a checksum mismatch error.
func NewImageChecksumMismatchError(msg string) error {
	return &errs.DomainError{
		Code:    errs.CodeImageChecksumMismatch,
		Message: msg,
	}
}

// WrapError wraps an error as a DomainError with the given code.
func WrapError(code errs.Code, msg string) error {
	return &errs.DomainError{Code: code, Message: msg}
}

// WrapErrorf wraps an error as a DomainError with formatted message.
func WrapErrorf(code errs.Code, format string, args ...any) error {
	return &errs.DomainError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// strPtr returns a pointer to s if s is non-empty, or nil if s is empty.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
