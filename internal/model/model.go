// Package model holds the shared domain vocabulary used across the baggage
// leg closeout hub: scan types, the outbound/inbound domain split, security
// outcomes, discrepancy codes and leg-key construction.
//
// These types intentionally carry no behaviour beyond validation and key
// derivation so that every other package can depend on them without pulling in
// storage or protocol code.
package model

import "strings"

// ScanType names a kind of baggage scan fact. The ordering of the constants is
// not semantically meaningful; milestones are selected by the tracker using a
// stable (scan-time, device, control-number, fact-id) ordering instead.
type ScanType string

const (
	ScanCheckin  ScanType = "CHECKIN"
	ScanSort     ScanType = "SORT"
	ScanScreen   ScanType = "SCREEN"
	ScanLoad     ScanType = "LOAD"
	ScanOffload  ScanType = "OFFLOAD" // outbound: bag pulled from a leg before departure
	ScanUnload   ScanType = "UNLOAD"  // inbound: bag unloaded at the destination
	ScanTransfer ScanType = "TRANSFER"
	ScanClaim    ScanType = "CLAIM"
)

// allScanTypes lists every recognised scan type. The order is stable and used
// for canonical serialisation.
var allScanTypes = []ScanType{
	ScanCheckin, ScanSort, ScanScreen, ScanLoad, ScanOffload,
	ScanUnload, ScanTransfer, ScanClaim,
}

// MessageTypeCode is the three-character Type B protocol code that identifies a
// scan type on the wire.
type MessageTypeCode string

const (
	CodeCheckin  MessageTypeCode = "CKI"
	CodeSort     MessageTypeCode = "SRT"
	CodeScreen   MessageTypeCode = "SCR"
	CodeLoad     MessageTypeCode = "LOD"
	CodeOffload  MessageTypeCode = "OFL"
	CodeUnload   MessageTypeCode = "UNL"
	CodeTransfer MessageTypeCode = "TRF"
	CodeClaim    MessageTypeCode = "CLM"
)

// codeToScanType maps a wire code to its domain scan type.
var codeToScanType = map[MessageTypeCode]ScanType{
	CodeCheckin:  ScanCheckin,
	CodeSort:     ScanSort,
	CodeScreen:   ScanScreen,
	CodeLoad:     ScanLoad,
	CodeOffload:  ScanOffload,
	CodeUnload:   ScanUnload,
	CodeTransfer: ScanTransfer,
	CodeClaim:    ScanClaim,
}

// scanTypeToCode is the reverse mapping.
var scanTypeToCode = map[ScanType]MessageTypeCode{
	ScanCheckin:  CodeCheckin,
	ScanSort:     CodeSort,
	ScanScreen:   CodeScreen,
	ScanLoad:     CodeLoad,
	ScanOffload:  CodeOffload,
	ScanUnload:   CodeUnload,
	ScanTransfer: CodeTransfer,
	ScanClaim:    CodeClaim,
}

// ValidMessageType reports whether c is a recognised wire code.
func ValidMessageType(c MessageTypeCode) bool {
	_, ok := codeToScanType[c]
	return ok
}

// ScanTypeFromCode resolves a wire code to a scan type. The second result is
// false when the code is unknown.
func ScanTypeFromCode(c MessageTypeCode) (ScanType, bool) {
	st, ok := codeToScanType[c]
	return st, ok
}

// CodeFor returns the wire code for a scan type.
func CodeFor(st ScanType) MessageTypeCode {
	return scanTypeToCode[st]
}

// AllScanTypes returns a copy of the recognised scan types in canonical order.
func AllScanTypes() []ScanType {
	out := make([]ScanType, len(allScanTypes))
	copy(out, allScanTypes)
	return out
}

// Domain separates the two independent projections maintained per leg.
type Domain string

const (
	DomainOutbound Domain = "OUTBOUND"
	DomainInbound  Domain = "INBOUND"
)

// DomainOf returns the projection domain a scan type belongs to.
func (s ScanType) Domain() Domain {
	switch s {
	case ScanCheckin, ScanSort, ScanScreen, ScanLoad, ScanOffload:
		return DomainOutbound
	case ScanUnload, ScanTransfer, ScanClaim:
		return DomainInbound
	}
	return ""
}

// ScreenOutcome is the effective security conclusion for a bag on a leg.
type ScreenOutcome string

const (
	ScreenClear ScreenOutcome = "CLEAR"
	ScreenHold  ScreenOutcome = "HOLD"
	ScreenNone  ScreenOutcome = ""
)

// DiscrepancyCode identifies a reconciled mismatch in a frozen close report.
type DiscrepancyCode string

const (
	DiscExpectedNotLoaded      DiscrepancyCode = "EXPECTED_NOT_LOADED"
	DiscLoadedNotExpected      DiscrepancyCode = "LOADED_NOT_EXPECTED"
	DiscLoadedWithoutClearance DiscrepancyCode = "LOADED_WITHOUT_CLEARANCE"
	DiscLoadedWhileHeld        DiscrepancyCode = "LOADED_WHILE_HELD"
	DiscOffloadedBeforeClose   DiscrepancyCode = "OFFLOADED_BEFORE_CLOSE"
	DiscWrongZoneEvidence      DiscrepancyCode = "WRONG_ZONE_EVIDENCE"
	DiscUnknownBagEvidence     DiscrepancyCode = "UNKNOWN_BAG_EVIDENCE"
)

// LegKey returns the canonical, stable key identifying a flight leg across all
// stores. Fields are upper-cased and the leg sequence is zero-padded so that
// text comparisons match regardless of input formatting.
func LegKey(carrier, flightNo, departureDate, origin, destination, legSeq string) string {
	return strings.ToUpper(strings.TrimSpace(carrier)) + "/" +
		strings.ToUpper(strings.TrimSpace(flightNo)) + "/" +
		strings.TrimSpace(departureDate) + "/" +
		strings.ToUpper(strings.TrimSpace(origin)) + "/" +
		strings.ToUpper(strings.TrimSpace(destination)) + "/" +
		padLegSeq(strings.TrimSpace(legSeq))
}

// padLegSeq zero-pads a numeric leg sequence to two digits.
func padLegSeq(s string) string {
	if len(s) >= 2 {
		return s
	}
	return strings.Repeat("0", 2-len(s)) + s
}
