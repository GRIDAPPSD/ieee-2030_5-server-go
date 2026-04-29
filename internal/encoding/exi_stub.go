package encoding

import "errors"

// ErrEXINotSupported is returned when a client requests EXI encoding.
// EXI support is planned for a future phase.
var ErrEXINotSupported = errors.New("EXI encoding not yet supported")

type exiStubEncoder struct{}

func (e *exiStubEncoder) ContentType() string {
	return ContentTypeSEPEXI
}

func (e *exiStubEncoder) Marshal(v any) ([]byte, error) {
	return nil, ErrEXINotSupported
}

func (e *exiStubEncoder) Unmarshal(data []byte, v any) error {
	return ErrEXINotSupported
}

// Compile-time assertion that exiStubEncoder satisfies the Encoder interface.
// Documents the intended shape for future EXI implementation (Phase 3).
var _ Encoder = (*exiStubEncoder)(nil)
