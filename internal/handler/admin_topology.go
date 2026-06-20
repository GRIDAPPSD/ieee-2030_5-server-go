package handler

import (
	"context"
	"net/http"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// IEEE-096: GET /api/topology returns the SY -> FD -> SP -> DEV tree for
// the dashboard. SY and FD/SP are stubs (single root, single feeder, single
// service-point) until the spec exposes them as first-class resources; the
// useful information is the DEV layer with FSAs and programs hanging off.

// EndDeviceLister is the read surface needed to walk all devices. Mirrors
// pkg/store.ResourceStore.List but kept narrow here per the Pike rule.
type EndDeviceLister interface {
	List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.EndDevice], error)
}

// TopologyNode represents one tier of the SY -> FD -> SP -> DEV tree.
type TopologyNode struct {
	Kind     string         `json:"kind"` // "SY" | "FD" | "SP" | "DEV"
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	SFDI     string         `json:"sfdi,omitempty"`
	LFDI     string         `json:"lfdi,omitempty"`
	Enabled  bool           `json:"enabled,omitempty"`
	FSAs     []TopologyFSA  `json:"fsas,omitempty"`
	Children []TopologyNode `json:"children,omitempty"`
}

// TopologyFSA describes an admin FSA hanging off a node.
type TopologyFSA struct {
	ID          string   `json:"id"`
	MRID        string   `json:"mRID"`
	Description string   `json:"description"`
	Programs    []string `json:"programs,omitempty"`
}

// topologyHandler is the dependency surface for GET /api/topology.
type topologyHandler struct {
	AdminFSAs  *memory.AdminFSAStore
	EndDevices EndDeviceLister
}

// HandleTopology returns a handler that emits the SY -> FD -> SP -> DEV
// JSON tree, with admin FSAs attached to the appropriate device and a
// "templates" bucket at the SY level for FSAs that exist but aren't yet
// assigned to any device.
func HandleTopology(adminFSAs *memory.AdminFSAStore, devices EndDeviceLister) http.HandlerFunc {
	h := &topologyHandler{AdminFSAs: adminFSAs, EndDevices: devices}
	return func(w http.ResponseWriter, r *http.Request) {
		root := h.buildTree(r.Context())
		writeJSON(w, http.StatusOK, root)
	}
}

func (h *topologyHandler) buildTree(ctx context.Context) TopologyNode {
	// DEV layer
	var devNodes []TopologyNode
	if h.EndDevices != nil {
		result, err := h.EndDevices.List(ctx, store.ListOptions{Limit: 10000})
		if err == nil {
			for _, dev := range result.Items {
				devNodes = append(devNodes, h.buildDevNode(ctx, dev))
			}
		}
	}

	// Unassigned admin-FSA templates live as a sibling collection under SY.
	var unassigned []TopologyFSA
	if h.AdminFSAs != nil {
		for _, fsa := range h.AdminFSAs.List(ctx) {
			if len(h.AdminFSAs.Devices(ctx, fsa.MRID)) == 0 {
				unassigned = append(unassigned, TopologyFSA{
					ID:          fsa.MRID,
					MRID:        fsa.MRID,
					Description: fsa.Description,
					Programs:    h.AdminFSAs.Programs(ctx, fsa.MRID),
				})
			}
		}
	}

	sp := TopologyNode{
		Kind:     "SP",
		ID:       "default-sp",
		Label:    "Default Service Point",
		Children: devNodes,
	}
	fd := TopologyNode{
		Kind:     "FD",
		ID:       "default-fd",
		Label:    "Default Feeder",
		Children: []TopologyNode{sp},
	}
	sy := TopologyNode{
		Kind:     "SY",
		ID:       "system",
		Label:    "IEEE 2030.5 System",
		FSAs:     unassigned, // templates not yet bound to a device
		Children: []TopologyNode{fd},
	}
	return sy
}

func (h *topologyHandler) buildDevNode(ctx context.Context, dev sep2.EndDevice) TopologyNode {
	enabled := dev.Enabled != nil && *dev.Enabled
	id := pathTail(dev.Href)

	var fsas []TopologyFSA
	if h.AdminFSAs != nil {
		for _, fsaID := range h.AdminFSAs.FSAsForDevice(ctx, id) {
			fsa, err := h.AdminFSAs.Get(ctx, fsaID)
			if err != nil {
				continue
			}
			fsas = append(fsas, TopologyFSA{
				ID:          fsaID,
				MRID:        fsa.MRID,
				Description: fsa.Description,
				Programs:    h.AdminFSAs.Programs(ctx, fsaID),
			})
		}
	}

	return TopologyNode{
		Kind:    "DEV",
		ID:      id,
		Label:   dev.SFDI,
		SFDI:    dev.SFDI,
		LFDI:    dev.LFDI,
		Enabled: enabled,
		FSAs:    fsas,
	}
}

// pathTail returns the segment after the last "/" in href.
func pathTail(href string) string {
	for i := len(href) - 1; i >= 0; i-- {
		if href[i] == '/' {
			return href[i+1:]
		}
	}
	return href
}
