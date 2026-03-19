package handler_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

func TestListHandlerPaging(t *testing.T) {
	s := memory.NewEndDeviceStore()

	for i := 0; i < 5; i++ {
		id := string(rune('1' + i))
		dev := sep2.EndDevice{SFDI: "00000000000" + id}
		dev.Href = "/edev/" + id
		s.Create(context.Background(), id, dev)
	}

	h := handler.ListHandler[sep2.EndDevice, sep2.EndDeviceList](
		s,
		func(href string, result store.ListResult[sep2.EndDevice], pollRate uint32) sep2.EndDeviceList {
			return sep2.EndDeviceList{
				ListResource: sep2.ListResource{
					SubscribableResource: sep2.SubscribableResource{
						Resource: sep2.Resource{Href: href},
					},
					All:      result.All,
					Results:  result.Results,
					PollRate: pollRate,
				},
				EndDevice: result.Items,
			}
		},
		900,
	)

	t.Run("default paging", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/edev", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("status = %d", w.Code)
		}

		var list sep2.EndDeviceList
		xml.Unmarshal(w.Body.Bytes(), &list)

		if list.All != 5 {
			t.Errorf("All = %d, want 5", list.All)
		}
		if list.Results != 5 {
			t.Errorf("Results = %d, want 5", list.Results)
		}
	})

	t.Run("limit 2", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/edev?l=2", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		var list sep2.EndDeviceList
		xml.Unmarshal(w.Body.Bytes(), &list)

		if list.All != 5 {
			t.Errorf("All = %d, want 5", list.All)
		}
		if list.Results != 2 {
			t.Errorf("Results = %d, want 2", list.Results)
		}
		if len(list.EndDevice) != 2 {
			t.Errorf("items = %d, want 2", len(list.EndDevice))
		}
	})

	t.Run("start 3 limit 10", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/edev?s=3&l=10", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		var list sep2.EndDeviceList
		xml.Unmarshal(w.Body.Bytes(), &list)

		if list.Results != 2 {
			t.Errorf("Results = %d, want 2 (items 4,5)", list.Results)
		}
	})

	t.Run("POST rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/edev", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", w.Code)
		}
	})

	t.Run("empty list", func(t *testing.T) {
		emptyStore := memory.NewEndDeviceStore()
		emptyHandler := handler.ListHandler[sep2.EndDevice, sep2.EndDeviceList](
			emptyStore,
			func(href string, result store.ListResult[sep2.EndDevice], pollRate uint32) sep2.EndDeviceList {
				return sep2.EndDeviceList{
					ListResource: sep2.ListResource{
						SubscribableResource: sep2.SubscribableResource{
							Resource: sep2.Resource{Href: href},
						},
						All:     result.All,
						Results: result.Results,
					},
					EndDevice: result.Items,
				}
			},
			900,
		)

		req := httptest.NewRequest(http.MethodGet, "/edev", nil)
		w := httptest.NewRecorder()
		emptyHandler.ServeHTTP(w, req)

		var list sep2.EndDeviceList
		xml.Unmarshal(w.Body.Bytes(), &list)

		if list.All != 0 || list.Results != 0 {
			t.Errorf("empty list: All=%d Results=%d", list.All, list.Results)
		}
	})

	t.Run("content type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/edev", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		ct := w.Header().Get("Content-Type")
		if ct != "application/sep+xml" {
			t.Errorf("Content-Type = %q", ct)
		}
	})
}
