package wadl

import (
	"net/http"
	"sort"
	"strconv"
)

// This file holds the method-mode policy the ratchet counts against, and the
// manager-only writes the sweep probes twice.
//
// Policy, per wx:mode of the method:
//
//	Mandatory, Optional  the method must be conformant.
//	Deprecated           conformant, or refused with 405.
//	Error                400 or 405, which classify reports as conformant.
//
// Every other verdict is a row outside the policy, and the verdict names why:
// unrouted, method_refused, wrong_status, e_mode_404 and so on.

// PolicyRow is one row outside the policy, or one method of a resource the
// crawl did not reach, which is not probed and so cannot be inside it.
type PolicyRow struct {
	ResourceID string
	Mode       Mode
	Verdict    Verdict
}

// insidePolicy reports whether a probed method row satisfies the policy for its
// mode. A row that is not a method observation is not judged here.
func insidePolicy(row Row) bool {
	if row.Phase != PhaseWADL || row.Method == "" {
		return true
	}
	switch row.Mode {
	case ModeDeprecated:
		return row.Verdict == VerdictConformant || row.Verdict == VerdictDeprecatedRefused
	default:
		return row.Verdict == VerdictConformant
	}
}

// OutsidePolicy returns every method outside the policy: the probed rows that
// fail it, and one not_reached entry for each method of a resource the crawl
// did not reach. A not-reached Deprecated or Error method is still probed at
// its sample path, so it is judged by its own row and never listed twice.
func (r *Results) OutsidePolicy(m *Model) []PolicyRow {
	var out []PolicyRow
	for _, row := range r.Rows {
		if !insidePolicy(row) {
			out = append(out, PolicyRow{ResourceID: row.ResourceID, Mode: row.Mode, Verdict: row.Verdict})
		}
	}
	for _, mode := range []Mode{ModeMandatory, ModeOptional} {
		for _, meth := range r.notReachedMethodList(m, mode) {
			out = append(out, PolicyRow{ResourceID: meth.ResourceID, Mode: mode, Verdict: VerdictNotReached})
		}
	}
	return out
}

// PolicyBreakdown renders OutsidePolicy as sorted "mode verdict count" lines,
// summed over resources.
func PolicyBreakdown(rows []PolicyRow) []string {
	type key struct {
		Mode    Mode
		Verdict Verdict
	}
	counts := map[key]int{}
	for _, row := range rows {
		counts[key{row.Mode, row.Verdict}]++
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Mode != keys[j].Mode {
			return keys[i].Mode < keys[j].Mode
		}
		return keys[i].Verdict < keys[j].Verdict
	})
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, string(k.Mode)+" "+string(k.Verdict)+" "+strconv.Itoa(counts[k]))
	}
	return out
}

// restrictedWrites names the Optional writes the server allows only to a
// manager certificate: the ones that change a credential or server-authored
// data (Registration PIN, DER programs, metering, prepayment credit). Keys are
// WADL resource ids, which are names the standard gives, not paths.
var restrictedWrites = map[string][]string{
	"Registration": {http.MethodPut, http.MethodDelete},

	"DERProgramList":           {http.MethodPost},
	"DERProgram":               {http.MethodDelete},
	"DERControlList":           {http.MethodPost},
	"DERControl":               {http.MethodDelete},
	"DefaultDERControl":        {http.MethodPut},
	"DERCurveList":             {http.MethodPost},
	"DERCurve":                 {http.MethodDelete},
	"AssociatedDERProgramList": {http.MethodPost},
	"CurrentDERProgram":        {http.MethodDelete},
	"AssociatedUsagePoint":     {http.MethodDelete},

	"UsagePointList":   {http.MethodPost},
	"UsagePoint":       {http.MethodDelete},
	"MeterReadingList": {http.MethodPost},
	"MeterReading":     {http.MethodDelete},
	"ReadingType":      {http.MethodPut},
	"ReadingSetList":   {http.MethodPost},
	"ReadingSet":       {http.MethodDelete},
	"ReadingList":      {http.MethodPost},
	"Reading":          {http.MethodPut, http.MethodDelete},

	"CreditRegisterList": {http.MethodPost},
	"CreditRegister":     {http.MethodDelete},
}

// restrictedWrite reports whether the method is one of the manager-only writes.
func restrictedWrite(m Method) bool {
	if m.Mode != ModeOptional {
		return false
	}
	return containsStr(restrictedWrites[m.ResourceID], m.Verb)
}

// RestrictedWrites counts the model's manager-only writes.
func RestrictedWrites(m *Model) int {
	n := 0
	for _, meth := range m.Methods {
		if restrictedWrite(meth) {
			n++
		}
	}
	return n
}

// probeDeviceOnRestricted sends the manager-only write as the ordinary device
// and records the answer. Only a 403 that an authority check wrote counts: the
// server also answers 403 to a path no route family names, which says nothing
// about authority, so that refusal is a failure of this assertion.
func (t *Target) probeDeviceOnRestricted(rows *[]Row, meth Method, path string, body []byte) {
	r := t.doWith(t.Client, meth.Verb, path, body)
	v := deviceVerdict(r)
	*rows = append(*rows, Row{
		Phase:          PhaseRestricted,
		ResourceID:     meth.ResourceID,
		WADLPath:       meth.Path,
		Path:           path,
		Method:         meth.Verb,
		Mode:           meth.Mode,
		Status:         r.status,
		BodyBytes:      len(r.body),
		BodyMinimal:    body != nil,
		Verdict:        v,
		TransportError: r.transportError,
	})
}

// deviceVerdict scores the ordinary device's answer to a manager-only write.
func deviceVerdict(r response) Verdict {
	if r.status == http.StatusForbidden && !isRouteRefusal(r) {
		return VerdictDeviceForbidden
	}
	return VerdictDeviceNotForbidden
}

// DeviceNotForbidden counts the manager-only writes for which no probe showed
// the ordinary device a 403: the ones it was answered otherwise, and the ones
// of resources the crawl did not reach, which were never probed.
func (r *Results) DeviceNotForbidden(m *Model) int {
	return RestrictedWrites(m) - r.Count(PhaseRestricted, VerdictDeviceForbidden)
}

// DeviceProbed counts the manager-only writes the sweep reached and probed as
// the ordinary device.
func (r *Results) DeviceProbed() int {
	return len(r.Filter(func(row Row) bool { return row.Phase == PhaseRestricted }))
}

// DeviceProbedNotForbidden counts the probed manager-only writes the ordinary
// device was not refused with an authority 403. Unlike DeviceNotForbidden it
// leaves out writes the crawl never reached, so a newly reached pair that is not
// refused raises it and a lost refusal raises it, while DeviceProbed alone moves
// when reach changes.
func (r *Results) DeviceProbedNotForbidden() int {
	return r.DeviceProbed() - r.Count(PhaseRestricted, VerdictDeviceForbidden)
}
