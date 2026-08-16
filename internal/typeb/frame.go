// Package typeb implements the strict 160-byte Type B ASCII frame used as the
// ingestion wire format for baggage scans.
//
// Frame layout (160 bytes, fixed):
//
//	 0   STX              (0x02)
//	 1   ProtocolVersion  2
//	 3   MessageType      3   wire code, e.g. "CKI"
//	 6   ControlNumber   10   zero-padded decimal
//	16   DeviceID         8
//	24   ScanTimeUTC     14   YYYYMMDDHHMMSS
//	38   BagTag          13
//	51   Carrier          3
//	54   FlightNumber     6
//	60   DepartureDate    8   YYYYMMDD
//	68   Origin           3
//	71   Destination      3
//	74   LegSequence      3
//	77   WorkZone         5
//	82   Operator         8
//	90   StatusReason     8
//	98   Reserved        53   space padding
//	151  CRC32            8   uppercase hex of CRC-32/IEEE over [1:151]
//	159  ETX              (0x03)
//
// The CRC covers the 150 payload bytes between STX and the CRC field. Fields
// are right space-padded and right-trimmed on decode; numeric fields carry no
// embedded spaces.
package typeb

import (
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"

	"github.com/baghub/baggage-leg-closeout-hub/internal/model"
)

// Frame length and control bytes.
const (
	FrameLen = 160
	STX      = 0x02
	ETX      = 0x03
	CRCLen   = 8
	Reserved = ' '
)

// Field offsets and widths.
const (
	offSTX             = 0
	offProtocolVersion = 1
	offMessageType     = 3
	offControlNumber   = 6
	offDeviceID        = 16
	offScanTimeUTC     = 24
	offBagTag          = 38
	offCarrier         = 51
	offFlightNumber    = 54
	offDepartureDate   = 60
	offOrigin          = 68
	offDestination     = 71
	offLegSequence     = 74
	offWorkZone        = 77
	offOperator        = 82
	offStatusReason    = 90
	offReserved        = 98
	offCRC             = 151
	offETX             = 159

	lenProtocolVersion = 2
	lenMessageType     = 3
	lenControlNumber   = 10
	lenDeviceID        = 8
	lenScanTimeUTC     = 14
	lenBagTag          = 13
	lenCarrier         = 3
	lenFlightNumber    = 6
	lenDepartureDate   = 8
	lenOrigin          = 3
	lenDestination     = 3
	lenLegSequence     = 3
	lenWorkZone        = 5
	lenOperator        = 8
	lenStatusReason    = 8
	lenReserved        = 53
)

// ErrCode is a stable, machine-readable rejection reason. It is the single
// discriminator returned to operators and persisted with rejected raw bytes.
type ErrCode string

const (
	ErrBadLength   ErrCode = "BAD_LENGTH"
	ErrBadBoundary ErrCode = "BAD_BOUNDARY"
	ErrBadASCII    ErrCode = "BAD_ASCII"
	ErrBadCRC      ErrCode = "BAD_CRC"
	ErrBadEnum     ErrCode = "BAD_ENUM"
	ErrEOFPartial  ErrCode = "EOF_PARTIAL"
)

// DecodeError carries a stable code together with the offending raw bytes so
// that ingest can persist them verbatim without losing the rejection reason.
type DecodeError struct {
	Code   ErrCode
	Reason string
	Raw    []byte
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("typeb: %s: %s", e.Code, e.Reason)
}

// Is enables errors.Is against a bare ErrCode sentinel: a *DecodeError matches
// ErrBadCRC etc. so callers can write errors.Is(err, typeb.ErrBadCRC). ErrCode
// already satisfies the error interface, so it is accepted directly; the
// AsSentinel() form is accepted as well for backward compatibility.
func (e *DecodeError) Is(target error) bool {
	switch t := target.(type) {
	case ErrCode:
		return e.Code == t
	case errCodeSentinel:
		return e.Code == ErrCode(t)
	}
	return false
}

// errCodeSentinel wraps an ErrCode so it satisfies the error interface and can
// be used as an errors.Is target.
type errCodeSentinel string

func (e errCodeSentinel) Error() string { return string(e) }

// ErrCode satisfies the error interface directly.
func (c ErrCode) Error() string { return string(c) }

// AsSentinel returns a comparable error value for use with errors.Is.
func (c ErrCode) AsSentinel() error { return errCodeSentinel(c) }

// Frame is the decoded view of a single 160-byte Type B message.
type Frame struct {
	ProtocolVersion string
	MessageType     model.MessageTypeCode
	ControlNumber   string
	DeviceID        string
	ScanTimeUTC     string
	BagTag          string
	Carrier         string
	FlightNumber    string
	DepartureDate   string
	Origin          string
	Destination     string
	LegSequence     string
	WorkZone        string
	Operator        string
	StatusReason    string
}

// fld describes a text field's position so encode/decode stay in sync.
type fld struct {
	off, length int
}

// Encode serialises a frame to exactly FrameLen bytes, computing the CRC.
func Encode(f Frame) []byte {
	out := make([]byte, FrameLen)
	// Reserved region is space-filled; everything else defaults to zero, so
	// pre-fill the whole buffer with spaces then overwrite control bytes.
	for i := range out {
		out[i] = Reserved
	}
	out[offSTX] = STX
	out[offETX] = ETX

	writeField(out, offProtocolVersion, lenProtocolVersion, f.ProtocolVersion)
	writeField(out, offMessageType, lenMessageType, string(f.MessageType))
	writeField(out, offControlNumber, lenControlNumber, zeroPad(f.ControlNumber, lenControlNumber))
	writeField(out, offDeviceID, lenDeviceID, f.DeviceID)
	writeField(out, offScanTimeUTC, lenScanTimeUTC, f.ScanTimeUTC)
	writeField(out, offBagTag, lenBagTag, f.BagTag)
	writeField(out, offCarrier, lenCarrier, strings.ToUpper(f.Carrier))
	writeField(out, offFlightNumber, lenFlightNumber, strings.ToUpper(f.FlightNumber))
	writeField(out, offDepartureDate, lenDepartureDate, f.DepartureDate)
	writeField(out, offOrigin, lenOrigin, strings.ToUpper(f.Origin))
	writeField(out, offDestination, lenDestination, strings.ToUpper(f.Destination))
	writeField(out, offLegSequence, lenLegSequence, zeroPad(f.LegSequence, lenLegSequence))
	writeField(out, offWorkZone, lenWorkZone, f.WorkZone)
	writeField(out, offOperator, lenOperator, f.Operator)
	writeField(out, offStatusReason, lenStatusReason, f.StatusReason)
	// Reserved already space-filled.

	sum := crc32.ChecksumIEEE(out[offProtocolVersion:offCRC])
	hexstr := strings.ToUpper(hex.EncodeToString(toBytes(sum)))
	writeField(out, offCRC, CRCLen, hexstr)
	return out
}

// writeField copies src into out[off:off+len] right space-padding. src longer
// than the field is truncated.
func writeField(out []byte, off, length int, src string) {
	for i := 0; i < length; i++ {
		if i < len(src) {
			out[off+i] = src[i]
		} else {
			out[off+i] = Reserved
		}
	}
}

func zeroPad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}

// toBytes returns the 4 big-endian bytes of a uint32.
func toBytes(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// ParseFrame decodes a complete 160-byte frame, validating boundaries, ASCII,
// reserved padding, CRC and message-type enum. It returns the decoded frame
// and its raw bytes on success.
func ParseFrame(raw []byte) (Frame, error) {
	if len(raw) != FrameLen {
		return Frame{}, &DecodeError{Code: ErrBadLength, Reason: fmt.Sprintf("len=%d want=%d", len(raw), FrameLen), Raw: append([]byte(nil), raw...)}
	}
	if raw[offSTX] != STX {
		return Frame{}, &DecodeError{Code: ErrBadBoundary, Reason: "missing STX", Raw: append([]byte(nil), raw...)}
	}
	if raw[offETX] != ETX {
		return Frame{}, &DecodeError{Code: ErrBadBoundary, Reason: "missing ETX", Raw: append([]byte(nil), raw...)}
	}
	// Every byte between STX and the CRC field must be printable ASCII.
	for i := offProtocolVersion; i < offCRC; i++ {
		if !isPrintable(raw[i]) {
			return Frame{}, &DecodeError{Code: ErrBadASCII, Reason: fmt.Sprintf("non-ascii byte 0x%02x at %d", raw[i], i), Raw: append([]byte(nil), raw...)}
		}
	}
	// Reserved must be entirely spaces.
	for i := offReserved; i < offCRC; i++ {
		if raw[i] != Reserved {
			return Frame{}, &DecodeError{Code: ErrBadASCII, Reason: fmt.Sprintf("reserved byte 0x%02x at %d", raw[i], i), Raw: append([]byte(nil), raw...)}
		}
	}
	// CRC field must be hex.
	crcField := string(raw[offCRC : offCRC+CRCLen])
	want, err := hexStringToUint(crcField)
	if err != nil {
		return Frame{}, &DecodeError{Code: ErrBadCRC, Reason: "crc field not hex", Raw: append([]byte(nil), raw...)}
	}
	got := crc32.ChecksumIEEE(raw[offProtocolVersion:offCRC])
	if got != want {
		return Frame{}, &DecodeError{Code: ErrBadCRC, Reason: fmt.Sprintf("crc got=%08X want=%s", got, crcField), Raw: append([]byte(nil), raw...)}
	}
	f := Frame{
		ProtocolVersion: strings.TrimSpace(string(raw[offProtocolVersion : offProtocolVersion+lenProtocolVersion])),
		MessageType:     model.MessageTypeCode(strings.TrimSpace(string(raw[offMessageType : offMessageType+lenMessageType]))),
		ControlNumber:   strings.TrimSpace(string(raw[offControlNumber : offControlNumber+lenControlNumber])),
		DeviceID:        strings.TrimSpace(string(raw[offDeviceID : offDeviceID+lenDeviceID])),
		ScanTimeUTC:     strings.TrimSpace(string(raw[offScanTimeUTC : offScanTimeUTC+lenScanTimeUTC])),
		BagTag:          strings.TrimSpace(string(raw[offBagTag : offBagTag+lenBagTag])),
		Carrier:         strings.ToUpper(strings.TrimSpace(string(raw[offCarrier : offCarrier+lenCarrier]))),
		FlightNumber:    strings.ToUpper(strings.TrimSpace(string(raw[offFlightNumber : offFlightNumber+lenFlightNumber]))),
		DepartureDate:   strings.TrimSpace(string(raw[offDepartureDate : offDepartureDate+lenDepartureDate])),
		Origin:          strings.ToUpper(strings.TrimSpace(string(raw[offOrigin : offOrigin+lenOrigin]))),
		Destination:     strings.ToUpper(strings.TrimSpace(string(raw[offDestination : offDestination+lenDestination]))),
		LegSequence:     strings.TrimSpace(string(raw[offLegSequence : offLegSequence+lenLegSequence])),
		WorkZone:        strings.TrimSpace(string(raw[offWorkZone : offWorkZone+lenWorkZone])),
		Operator:        strings.TrimSpace(string(raw[offOperator : offOperator+lenOperator])),
		StatusReason:    strings.TrimSpace(string(raw[offStatusReason : offStatusReason+lenStatusReason])),
	}
	if !model.ValidMessageType(f.MessageType) {
		return Frame{}, &DecodeError{Code: ErrBadEnum, Reason: fmt.Sprintf("unknown message type %q", f.MessageType), Raw: append([]byte(nil), raw...)}
	}
	return f, nil
}

// isPrintable accepts the 0x20..0x7E printable ASCII range.
func isPrintable(b byte) bool { return b >= 0x20 && b <= 0x7E }

func hexStringToUint(s string) (uint32, error) {
	if len(s) != 8 {
		return 0, errors.New("crc field wrong length")
	}
	var v uint32
	for i := 0; i < 8; i++ {
		c := s[i]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		default:
			return 0, errors.New("crc field not hex")
		}
		v = (v << 4) | uint32(d)
	}
	return v, nil
}

// StreamDecoder reads Type B frames from an io.Reader, keeping a private
// buffer per connection. It handles arbitrary fragmentation, multiple
// consecutive frames, EOF partial frames, non-ASCII bytes and bad boundaries.
//
// Each call to Next returns either a decoded frame with its raw bytes or a
// *DecodeError describing a rejected slice (which the caller persists and then
// continues). When the reader is exhausted and no bytes remain, Next returns
// io.EOF.
type StreamDecoder struct {
	r    io.Reader
	buf  []byte
	done bool
}

// NewStreamDecoder wraps r with a fresh per-connection buffer.
func NewStreamDecoder(r io.Reader) *StreamDecoder {
	return &StreamDecoder{r: r}
}

// Next returns the next frame or rejection from the stream. It returns io.EOF
// when the reader is fully consumed.
func (d *StreamDecoder) Next() (Frame, []byte, error) {
	for {
		// Try to produce an event from the current buffer.
		if frame, raw, err, produced := d.tryProduce(); produced {
			return frame, raw, err
		}
		if d.done {
			// No more data: flush any leftover bytes as an EOF partial.
			if len(d.buf) > 0 {
				raw := d.buf
				d.buf = nil
				return Frame{}, raw, &DecodeError{Code: ErrEOFPartial, Reason: fmt.Sprintf("%d trailing bytes", len(raw)), Raw: append([]byte(nil), raw...)}
			}
			return Frame{}, nil, io.EOF
		}
		if err := d.fill(); err != nil {
			if errors.Is(err, io.EOF) {
				d.done = true
				continue
			}
			return Frame{}, nil, err
		}
	}
}

// tryProduce attempts to emit one event from d.buf. produced is true if an
// event (frame or error) was emitted.
func (d *StreamDecoder) tryProduce() (Frame, []byte, error, bool) {
	// Locate the first STX.
	idx := indexByte(d.buf, STX)
	if idx < 0 {
		// No STX yet. If we have a lot of bytes, they are likely garbage, but
		// the STX might still arrive; keep at most FrameLen-1 bytes to bound
		// memory and emit the rest as bad-boundary evidence.
		if len(d.buf) > FrameLen {
			cut := len(d.buf) - (FrameLen - 1)
			raw := d.buf[:cut]
			d.buf = d.buf[cut:]
			return Frame{}, raw, &DecodeError{Code: ErrBadBoundary, Reason: "no STX within window", Raw: append([]byte(nil), raw...)}, true
		}
		return Frame{}, nil, nil, false
	}
	// Discard any leading garbage as bad-boundary evidence.
	if idx > 0 {
		raw := d.buf[:idx]
		d.buf = d.buf[idx:]
		return Frame{}, raw, &DecodeError{Code: ErrBadBoundary, Reason: "bytes before STX", Raw: append([]byte(nil), raw...)}, true
	}
	// STX is at index 0. Need a full frame.
	if len(d.buf) < FrameLen {
		return Frame{}, nil, nil, false
	}
	// ETX must land exactly at the end of the fixed frame.
	if d.buf[offETX] != ETX {
		// Misaligned STX: drop it and resync.
		raw := d.buf[:1]
		d.buf = d.buf[1:]
		return Frame{}, raw, &DecodeError{Code: ErrBadBoundary, Reason: "ETX not at frame end", Raw: append([]byte(nil), raw...)}, true
	}
	raw := append([]byte(nil), d.buf[:FrameLen]...)
	d.buf = d.buf[FrameLen:]
	f, err := ParseFrame(raw)
	if err != nil {
		return Frame{}, raw, err, true
	}
	return f, raw, nil, true
}

// fill appends more bytes from the reader to the buffer.
func (d *StreamDecoder) fill() error {
	tmp := make([]byte, 4096)
	n, err := d.r.Read(tmp)
	if n > 0 {
		d.buf = append(d.buf, tmp[:n]...)
	}
	if err != nil {
		return err
	}
	return nil
}

// indexByte returns the index of the first occurrence of b in s, or -1.
func indexByte(s []byte, b byte) int {
	for i, c := range s {
		if c == b {
			return i
		}
	}
	return -1
}

// ShardReader wraps a byte slice and returns at most chunk bytes per Read,
// simulating arbitrary TCP fragmentation. A chunk of 1 delivers one byte at a
// time, exercising every byte boundary.
type ShardReader struct {
	data  []byte
	chunk int
	pos   int
}

// NewShardReader returns a reader over data that yields at most chunk bytes
// per Read call. chunk <= 0 yields one byte at a time.
func NewShardReader(data []byte, chunk int) *ShardReader {
	if chunk <= 0 {
		chunk = 1
	}
	return &ShardReader{data: data, chunk: chunk}
}

// Read implements io.Reader.
func (r *ShardReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := r.chunk
	if n > len(r.data)-r.pos {
		n = len(r.data) - r.pos
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}
