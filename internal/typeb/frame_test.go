package typeb_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
	"github.com/baghub/baggage-leg-closeout-hub/internal/typeb"
)

func TestDecodeErrorMatchesPublicErrCode(t *testing.T) {
	raw := typeb.Encode(typeb.Frame{
		ProtocolVersion: "01",
		MessageType:     model.CodeCheckin,
	})
	raw[len(raw)-typeb.CRCLen-1] = 'Z'

	_, err := typeb.ParseFrame(raw)
	if err == nil {
		t.Fatal("ParseFrame() error = nil, want BAD_CRC")
	}
	if !errors.Is(err, typeb.ErrBadCRC) {
		t.Fatalf("errors.Is(%v, %v) = false, want true", err, typeb.ErrBadCRC)
	}
	if errors.Is(err, typeb.ErrBadASCII) {
		t.Fatalf("errors.Is(%v, %v) = true, want false", err, typeb.ErrBadASCII)
	}

	var decodeErr *typeb.DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("errors.As(%T, *DecodeError) = false", err)
	}
	if decodeErr.Reason == "" || !strings.Contains(err.Error(), decodeErr.Reason) {
		t.Fatalf("error detail was lost: error=%q reason=%q", err, decodeErr.Reason)
	}
	if !bytes.Equal(decodeErr.Raw, raw) {
		t.Fatal("DecodeError.Raw does not preserve the rejected frame")
	}
}
