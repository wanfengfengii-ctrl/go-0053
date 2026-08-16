package typeb

import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
)

// allErrCodes is the full set of public ErrCode sentinels. Every regression
// assertion below iterates it so that "non-corresponding error codes are
// rejected" is checked exhaustively rather than for one or two codes.
var allErrCodes = []ErrCode{
	ErrBadLength,
	ErrBadBoundary,
	ErrBadASCII,
	ErrBadCRC,
	ErrBadEnum,
	ErrEOFPartial,
}

// validFrame returns a fully populated, normalisable frame whose Encode output
// ParseFrame accepts without error. It is the single source of "known good"
// bytes from which every corruption-based fixture is derived.
func validFrame() Frame {
	return Frame{
		ProtocolVersion: "2",
		MessageType:     model.CodeCheckin,
		ControlNumber:   "1234567890",
		DeviceID:        "DEV00001",
		ScanTimeUTC:     "20260816120000",
		BagTag:          "0012345678901",
		Carrier:         "AA",
		FlightNumber:    "1234",
		DepartureDate:   "20260816",
		Origin:          "JFK",
		Destination:     "LHR",
		LegSequence:     "1",
		WorkZone:        "W1",
		Operator:        "OPR1",
		StatusReason:    "OK",
	}
}

// validRaw is the 160-byte encoding of validFrame(); it is the canonical good
// frame used as the base for corruption fixtures.
func validRaw() []byte {
	return Encode(validFrame())
}

// recomputeCRC rewrites the CRC field of b to match its current payload so that
// a fixture can alter payload bytes and still pass the CRC check (used to reach
// the enum-validation branch, which sits after CRC verification).
func recomputeCRC(b []byte) {
	sum := crc32.ChecksumIEEE(b[offProtocolVersion:offCRC])
	hexstr := strings.ToUpper(fmt.Sprintf("%08X", sum))
	copy(b[offCRC:offCRC+CRCLen], hexstr)
}

// assertCodeMatch checks the three machine-readable matching obligations for a
// DecodeError-bearing err:
//   - the corresponding code is matched (errors.Is true),
//   - the AsSentinel() backward-compat form is matched (errors.Is true),
//   - every non-corresponding code is rejected in both direct and sentinel
//     forms (errors.Is false).
func assertCodeMatch(t *testing.T, err error, want ErrCode) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("errors.Is(err, %s) = false, want true (err=%v)", want, err)
	}
	if !errors.Is(err, want.AsSentinel()) {
		t.Errorf("errors.Is(err, %s.AsSentinel()) = false, want true (err=%v)", want, err)
	}
	for _, other := range allErrCodes {
		if other == want {
			continue
		}
		if errors.Is(err, other) {
			t.Errorf("errors.Is(err, %s) = true, want false (err code=%s)", other, want)
		}
		if errors.Is(err, other.AsSentinel()) {
			t.Errorf("errors.Is(err, %s.AsSentinel()) = true, want false (err code=%s)", other, want)
		}
	}
}

// assertDecodeErrorDetails checks that err still carries the stable code, a
// human-readable reason and a verbatim copy of the offending raw bytes — i.e.
// that fixing the Is matcher did not strip error detail or original bytes.
func assertDecodeErrorDetails(t *testing.T, err error, want ErrCode, raw []byte) {
	t.Helper()
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("errors.As(err, &DecodeError) = false (err=%v)", err)
	}
	if de.Code != want {
		t.Errorf("DecodeError.Code = %s, want %s", de.Code, want)
	}
	if de.Reason == "" {
		t.Errorf("DecodeError.Reason is empty for %s", want)
	}
	if !bytes.Equal(de.Raw, raw) {
		t.Errorf("DecodeError.Raw = %x, want verbatim input %x", de.Raw, raw)
	}
}

// TestParseFrame_HappyPath guards that the Is fix did not perturb normal frame
// decoding: a valid frame round-trips with no error and its fields decode.
func TestParseFrame_HappyPath(t *testing.T) {
	want := validFrame()
	raw := Encode(want)
	if len(raw) != FrameLen {
		t.Fatalf("Encode produced %d bytes, want %d", len(raw), FrameLen)
	}
	if raw[offSTX] != STX || raw[offETX] != ETX {
		t.Fatalf("encoded frame has bad boundaries: STX=0x%02x ETX=0x%02x", raw[offSTX], raw[offETX])
	}

	got, err := ParseFrame(raw)
	if err != nil {
		t.Fatalf("ParseFrame returned error on a valid frame: %v", err)
	}
	if got.MessageType != want.MessageType {
		t.Errorf("MessageType = %q, want %q", got.MessageType, want.MessageType)
	}
	if got.ControlNumber != "1234567890" {
		t.Errorf("ControlNumber = %q, want %q", got.ControlNumber, "1234567890")
	}
	if got.BagTag != "0012345678901" {
		t.Errorf("BagTag = %q, want %q", got.BagTag, "0012345678901")
	}
	if got.Origin != "JFK" || got.Destination != "LHR" {
		t.Errorf("Origin/Destination = %q/%q, want JFK/LHR", got.Origin, got.Destination)
	}
	if got.Carrier != "AA" {
		t.Errorf("Carrier = %q, want %q", got.Carrier, "AA")
	}
}

// TestParseFrame_ErrorCodes covers real parse failures: for each public ErrCode
// producible by ParseFrame it builds an offending 160-byte slice, confirms the
// corresponding sentinel matches, every non-corresponding sentinel is rejected,
// and error detail plus raw bytes are preserved.
func TestParseFrame_ErrorCodes(t *testing.T) {
	tests := []struct {
		name string
		code ErrCode
		raw  func() []byte
	}{
		{
			name: "wrong length",
			code: ErrBadLength,
			raw:  func() []byte { return make([]byte, FrameLen-1) },
		},
		{
			name: "missing STX",
			code: ErrBadBoundary,
			raw: func() []byte {
				b := validRaw()
				b[offSTX] = 'X' // first byte must be STX
				return b
			},
		},
		{
			name: "missing ETX",
			code: ErrBadBoundary,
			raw: func() []byte {
				b := validRaw()
				b[offETX] = 'X' // last byte must be ETX
				return b
			},
		},
		{
			name: "non-ascii payload byte",
			code: ErrBadASCII,
			raw: func() []byte {
				b := validRaw()
				b[offMessageType+1] = 0x01 // control byte inside payload
				return b
			},
		},
		{
			name: "reserved region not spaces",
			code: ErrBadASCII,
			raw: func() []byte {
				b := validRaw()
				b[offReserved+2] = 'X' // printable, but reserved must be space
				return b
			},
		},
		{
			name: "crc mismatch",
			code: ErrBadCRC,
			raw: func() []byte {
				b := validRaw()
				// Flip a payload digit: the stored CRC field no longer matches.
				if b[offControlNumber] == '1' {
					b[offControlNumber] = '9'
				} else {
					b[offControlNumber] = '1'
				}
				return b
			},
		},
		{
			name: "crc field not hex",
			code: ErrBadCRC,
			raw: func() []byte {
				b := validRaw()
				b[offCRC] = 'Z' // CRC field must be hex
				return b
			},
		},
		{
			name: "unknown message type",
			code: ErrBadEnum,
			raw: func() []byte {
				// Encode writes a correct CRC for an invalid message type, so
				// the frame passes every check up to enum validation.
				f := validFrame()
				f.MessageType = "XXX"
				return Encode(f)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.raw()
			_, err := ParseFrame(raw)
			assertCodeMatch(t, err, tt.code)
			assertDecodeErrorDetails(t, err, tt.code, raw)
		})
	}
}

// TestErrorsIs_ThroughWrapping confirms the realistic ingest pattern where the
// DecodeError is wrapped before classification still resolves to its code. This
// is the scenario the接入层 relies on: errors.Is(fmt.Errorf("...: %w", err),
// typeb.ErrBadCRC).
func TestErrorsIs_ThroughWrapping(t *testing.T) {
	raw := validRaw()
	raw[offControlNumber] = '9'
	_, err := ParseFrame(raw)
	if err == nil {
		t.Fatal("expected error")
	}

	wrapped := fmt.Errorf("ingest rejected frame: %w", err)
	if !errors.Is(wrapped, ErrBadCRC) {
		t.Errorf("errors.Is(wrapped, ErrBadCRC) = false, want true")
	}
	if errors.Is(wrapped, ErrBadLength) {
		t.Errorf("errors.Is(wrapped, ErrBadLength) = true, want false")
	}

	// Detail and raw bytes remain reachable through the wrapper.
	var de *DecodeError
	if !errors.As(wrapped, &de) {
		t.Fatal("errors.As(wrapped, &DecodeError) = false")
	}
	if de.Code != ErrBadCRC {
		t.Errorf("DecodeError.Code = %s, want %s", de.Code, ErrBadCRC)
	}
	if !bytes.Equal(de.Raw, raw) {
		t.Errorf("DecodeError.Raw does not match the offending bytes")
	}
}

// TestErrorsIs_BareErrCodeAsError checks that a bare ErrCode used directly as an
// error value still classifies correctly, and that distinct codes do not alias.
func TestErrorsIs_BareErrCodeAsError(t *testing.T) {
	var err error = ErrBadCRC
	if !errors.Is(err, ErrBadCRC) {
		t.Errorf("errors.Is(ErrBadCRC, ErrBadCRC) = false, want true")
	}
	if errors.Is(err, ErrBadLength) {
		t.Errorf("errors.Is(ErrBadCRC, ErrBadLength) = true, want false")
	}
}

// TestStreamDecoder_ValidFrame confirms fragmented delivery (one byte per Read)
// still reassembles and decodes a good frame, and that io.EOF terminates.
func TestStreamDecoder_ValidFrame(t *testing.T) {
	raw := validRaw()
	sd := NewStreamDecoder(NewShardReader(raw, 1)) // worst-case fragmentation

	f, gotRaw, err := sd.Next()
	if err != nil {
		t.Fatalf("Next returned error on a valid fragmented frame: %v", err)
	}
	if !bytes.Equal(gotRaw, raw) {
		t.Errorf("returned raw does not match input (%d vs %d bytes)", len(gotRaw), len(raw))
	}
	if f.MessageType != model.CodeCheckin {
		t.Errorf("MessageType = %q, want %q", f.MessageType, model.CodeCheckin)
	}

	if _, _, err := sd.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("second Next = %v, want io.EOF", err)
	}
}

// TestStreamDecoder_TwoFrames confirms the decoder emits multiple frames from a
// single stream and stays in lockstep with the framing.
func TestStreamDecoder_TwoFrames(t *testing.T) {
	raw := append(append([]byte{}, validRaw()...), validRaw()...)
	sd := NewStreamDecoder(bytes.NewReader(raw))

	for i := 0; i < 2; i++ {
		f, _, err := sd.Next()
		if err != nil {
			t.Fatalf("frame %d: unexpected error %v", i, err)
		}
		if f.MessageType != model.CodeCheckin {
			t.Errorf("frame %d: MessageType = %q", i, f.MessageType)
		}
	}
	if _, _, err := sd.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("final Next = %v, want io.EOF", err)
	}
}

// TestStreamDecoder_BadCRC confirms a rejectable frame surfaced through the
// stream decoder still matches its public ErrCode.
func TestStreamDecoder_BadCRC(t *testing.T) {
	raw := validRaw()
	raw[offControlNumber] = '9'
	sd := NewStreamDecoder(bytes.NewReader(raw))

	_, gotRaw, err := sd.Next()
	assertCodeMatch(t, err, ErrBadCRC)
	if !bytes.Equal(gotRaw, raw) {
		t.Errorf("returned raw does not match input")
	}
	assertDecodeErrorDetails(t, err, ErrBadCRC, raw)

	if _, _, err := sd.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("final Next = %v, want io.EOF", err)
	}
}

// TestStreamDecoder_BadEnum covers a frame that parses past CRC but fails enum
// validation, surfaced through streaming.
func TestStreamDecoder_BadEnum(t *testing.T) {
	f := validFrame()
	f.MessageType = "XXX"
	raw := Encode(f)
	sd := NewStreamDecoder(bytes.NewReader(raw))

	_, gotRaw, err := sd.Next()
	assertCodeMatch(t, err, ErrBadEnum)
	if !bytes.Equal(gotRaw, raw) {
		t.Errorf("returned raw does not match input")
	}
	assertDecodeErrorDetails(t, err, ErrBadEnum, raw)
}

// TestStreamDecoder_EOFPartial covers the only code ParseFrame cannot produce:
// trailing bytes that never reach a full frame. The code must still be
// matchable, every other code rejected, and the trailing bytes preserved.
func TestStreamDecoder_EOFPartial(t *testing.T) {
	partial := validRaw()[:FrameLen-60] // STX plus a short tail, < FrameLen
	sd := NewStreamDecoder(bytes.NewReader(partial))

	_, gotRaw, err := sd.Next()
	assertCodeMatch(t, err, ErrEOFPartial)
	if !bytes.Equal(gotRaw, partial) {
		t.Errorf("returned raw = %d bytes, want %d trailing bytes", len(gotRaw), len(partial))
	}
	assertDecodeErrorDetails(t, err, ErrEOFPartial, partial)

	if _, _, err := sd.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("final Next = %v, want io.EOF", err)
	}
}

// TestStreamDecoder_GarbageBeforeSTX covers the bad-boundary path where leading
// junk is shed before a valid frame; the junk is reported, then the frame is
// decoded normally.
func TestStreamDecoder_GarbageBeforeSTX(t *testing.T) {
	raw := append([]byte("GARBAGE"), validRaw()...)
	sd := NewStreamDecoder(bytes.NewReader(raw))

	_, junk, err := sd.Next()
	assertCodeMatch(t, err, ErrBadBoundary)
	if string(junk) != "GARBAGE" {
		t.Errorf("junk = %q, want %q", junk, "GARBAGE")
	}

	f, _, err := sd.Next()
	if err != nil {
		t.Fatalf("valid frame after garbage: unexpected error %v", err)
	}
	if f.MessageType != model.CodeCheckin {
		t.Errorf("MessageType = %q, want %q", f.MessageType, model.CodeCheckin)
	}

	if _, _, err := sd.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("final Next = %v, want io.EOF", err)
	}
}
