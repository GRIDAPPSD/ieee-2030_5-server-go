package der_test

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	coredel "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// specCorrectDERStatusPUT is the body a spec-correct IEEE 2030.5 client sends.
//
// stateOfChargeStatus and storageModeStatus are complexTypes in sep.xsd
// (sep.xsd:4189, sep.xsd:4195), each a sequence of a required dateTime
// (TimeType) and a required value. This is verbatim the shape that the EPRI
// client emitted and that the server rejected with 400 during end-to-end run
// 9: Go modelled stateOfChargeStatus as a bare *uint16, so encoding/xml tried
// to parse the element's (empty) chardata as an unsigned integer and failed
// with `strconv.ParseUint: parsing "": invalid syntax`. The client then tore
// down its connection and never POSTed a MirrorMeterReading.
const specCorrectDERStatusPUT = `<DERStatus xmlns="urn:ieee:std:2030.5:ns">
  <genConnectStatus>
    <dateTime>1604963587</dateTime>
    <value>01</value>
  </genConnectStatus>
  <inverterStatus>
    <dateTime>1604963587</dateTime>
    <value>2</value>
  </inverterStatus>
  <operationalModeStatus>
    <dateTime>1604963587</dateTime>
    <value>2</value>
  </operationalModeStatus>
  <readingTime>1604963587</readingTime>
  <stateOfChargeStatus>
    <dateTime>1604963587</dateTime>
    <value>7500</value>
  </stateOfChargeStatus>
  <storageModeStatus>
    <dateTime>1604963587</dateTime>
    <value>1</value>
  </storageModeStatus>
</DERStatus>`

// TestDERStatusPUTAcceptsSpecCorrectStateOfChargeStatus is a regression
// test. It asserts the server accepts the spec-correct body AND
// that the parsed values survive the round trip, per the data-invariants rule:
// a 204 that stored a zeroed or dropped state of charge would be a silent
// corruption, not a fix.
func TestDERStatusPUTAcceptsSpecCorrectStateOfChargeStatus(t *testing.T) {
	t.Parallel()

	statuses := memory.NewScopedStore[sep2.DERStatus]()
	_, _, ders, _ := coredel.DERSingletonHandlers(
		memory.NewScopedStore[sep2.DERCapability](),
		memory.NewScopedStore[sep2.DERSettings](),
		statuses,
		memory.NewScopedStore[sep2.DERAvailability](),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/ders", ders)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/ders", ders)

	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, httptest.NewRequest(
		http.MethodPut,
		"/edev/e1/der/d1/ders",
		bytes.NewReader([]byte(specCorrectDERStatusPUT)),
	))
	if putW.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204. Body: %s", putW.Code, putW.Body.String())
	}

	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, httptest.NewRequest(http.MethodGet, "/edev/e1/der/d1/ders", nil))
	if getW.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getW.Code)
	}

	var got sep2.DERStatus
	if err := xml.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal GET body: %v\nbody: %s", err, getW.Body.String())
	}

	if got.StateOfChargeStatus == nil {
		t.Fatalf("stateOfChargeStatus round-tripped as nil; body: %s", getW.Body.String())
	}
	if want := int64(1604963587); got.StateOfChargeStatus.DateTime != want {
		t.Errorf("stateOfChargeStatus.dateTime = %d, want %d", got.StateOfChargeStatus.DateTime, want)
	}
	// 7500 hundredths of a percent is 75%. PerCent is UInt16 scaled by 100
	// (sep.xsd:5945-5952), so a uint8 field here would silently truncate.
	if want := uint16(7500); got.StateOfChargeStatus.Value != want {
		t.Errorf("stateOfChargeStatus.value = %d, want %d", got.StateOfChargeStatus.Value, want)
	}

	if got.StorageModeStatus == nil {
		t.Fatalf("storageModeStatus round-tripped as nil; body: %s", getW.Body.String())
	}
	if want := int64(1604963587); got.StorageModeStatus.DateTime != want {
		t.Errorf("storageModeStatus.dateTime = %d, want %d", got.StorageModeStatus.DateTime, want)
	}
	if want := uint8(1); got.StorageModeStatus.Value != want {
		t.Errorf("storageModeStatus.value = %d, want %d", got.StorageModeStatus.Value, want)
	}

	// The other fields must keep their current wire form: this is a targeted
	// type fix, not a remodel.
	if got.ReadingTime != 1604963587 {
		t.Errorf("readingTime = %d, want 1604963587", got.ReadingTime)
	}
	if got.GenConnectStatus == nil || got.GenConnectStatus.Value != sep2.HexBinary8(1) {
		t.Errorf("genConnectStatus = %+v, want value 1", got.GenConnectStatus)
	}
	if got.InverterStatus == nil || got.InverterStatus.Value != 2 {
		t.Errorf("inverterStatus = %+v, want value 2", got.InverterStatus)
	}
	if got.OperationalModeStatus == nil || got.OperationalModeStatus.Value != 2 {
		t.Errorf("operationalModeStatus = %+v, want value 2", got.OperationalModeStatus)
	}
}
