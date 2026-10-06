package sep2adminplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
)

type columnSource struct{}

func (columnSource) Columns() []sep2admin.DeviceColumn {
	return []sep2admin.DeviceColumn{{ID: "name", Label: "Name"}}
}

func (columnSource) Cells(context.Context, []string) (map[string]map[string]string, error) {
	return map[string]map[string]string{"AA": {"name": "alpha"}}, nil
}

func TestPlaneDeviceColumnsReachPayload(t *testing.T) {
	cfg := baseConfig()
	cfg.LoopbackBypass = true
	cfg.DeviceColumns = []sep2admin.DeviceColumnSource{columnSource{}}
	if err := cfg.Stores.EndDevices.Create(context.Background(), "1", sep2.EndDevice{SFDI: "111", LFDI: "AA"}); err != nil {
		t.Fatal(err)
	}
	w := send(newPlane(t, cfg), http.MethodGet, "/dashboard/data", "", false)
	var body struct {
		Columns []struct{ ID, Label string } `json:"columns"`
		Devices []struct {
			Cells map[string]string `json:"cells"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Devices) != 1 {
		t.Fatalf("GET /dashboard/data = %d %s (err %v)", w.Code, w.Body, err)
	}
	if len(body.Columns) != 1 || body.Columns[0].ID != "name" || body.Columns[0].Label != "Name" {
		t.Errorf("columns = %+v", body.Columns)
	}
	if got := body.Devices[0].Cells["name"]; got != "alpha" {
		t.Errorf("cell = %q, want alpha", got)
	}
}

func TestNewRefusesNilDeviceColumnSource(t *testing.T) {
	cfg := baseConfig()
	cfg.DeviceColumns = []sep2admin.DeviceColumnSource{columnSource{}, nil}
	if _, err := sep2adminplane.New(cfg); !errors.Is(err, sep2adminplane.ErrNilDeviceColumnSource) {
		t.Errorf("New = %v, want ErrNilDeviceColumnSource", err)
	}
}

func TestNewRefusesTypedNilDeviceColumnSource(t *testing.T) {
	var typed *typedNilSource
	cfg := baseConfig()
	cfg.DeviceColumns = []sep2admin.DeviceColumnSource{typed}
	if _, err := sep2adminplane.New(cfg); !errors.Is(err, sep2adminplane.ErrNilDeviceColumnSource) {
		t.Errorf("New = %v, want ErrNilDeviceColumnSource", err)
	}
}

type typedNilSource struct{}

func (*typedNilSource) Columns() []sep2admin.DeviceColumn { return nil }
func (*typedNilSource) Cells(context.Context, []string) (map[string]map[string]string, error) {
	return nil, nil
}
