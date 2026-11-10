package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/activelist"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// activeTextMessageSuffix is the one place the active list's path segment is
// written, so the link a program advertises and the route that serves it
// cannot disagree.
const activeTextMessageSuffix = "/acttm"

// ActiveTextMessageListHref is the href of the active text message list of
// the program at programHref ("/msg/{msgId}"). ok is false for any other
// shape.
func ActiveTextMessageListHref(programHref string) (string, bool) {
	id, ok := programID(programHref)
	if !ok {
		return "", false
	}
	return "/msg/" + id + activeTextMessageSuffix, true
}

// programID pulls {msgId} out of "/msg/{msgId}".
func programID(href string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(href), "/msg/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// NewActiveTextMessageStore serves only the text messages of messages that are
// Active at request time, with the paging and order the caller layers on top
// (see [activelist.ActiveStore]).
func NewActiveTextMessageStore(messages store.ScopedStore[sep2.TextMessage]) store.ScopedStore[sep2.TextMessage] {
	return activelist.NewActiveStore(messages, func(m *sep2.TextMessage) *sep2.Event { return &m.Event })
}

// TextMessageCounter is the read-only subset of a text message store the
// program decorator needs.
type TextMessageCounter = activelist.Counter

// ActiveLinkProgramStore decorates a MessagingProgram store so every served
// program carries an ActiveTextMessageListLink whose all is the live count of
// the program's active text messages. A stored link keeps its href; an absent
// one is derived from the program's own href. A failed count serves no link
// rather than a wrong all. Every other method delegates unchanged.
type ActiveLinkProgramStore struct {
	store.ResourceStore[sep2.MessagingProgram]
	active TextMessageCounter
}

// NewActiveLinkProgramStore decorates programs; active is the store from
// [NewActiveTextMessageStore].
func NewActiveLinkProgramStore(programs store.ResourceStore[sep2.MessagingProgram], active TextMessageCounter) *ActiveLinkProgramStore {
	return &ActiveLinkProgramStore{ResourceStore: programs, active: active}
}

var _ store.ResourceStore[sep2.MessagingProgram] = (*ActiveLinkProgramStore)(nil)

func (s *ActiveLinkProgramStore) Get(ctx context.Context, id string) (sep2.MessagingProgram, error) {
	p, err := s.ResourceStore.Get(ctx, id)
	if err != nil {
		return p, err
	}
	s.stamp(ctx, id, &p)
	return p, nil
}

func (s *ActiveLinkProgramStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.MessagingProgram], error) {
	result, err := s.ResourceStore.List(ctx, opts)
	if err != nil {
		return result, err
	}
	for i := range result.Items {
		id, _ := programID(result.Items[i].Href)
		s.stamp(ctx, id, &result.Items[i])
	}
	return result, nil
}

func (s *ActiveLinkProgramStore) stamp(ctx context.Context, id string, p *sep2.MessagingProgram) {
	if id == "" {
		return
	}
	derive := func() (string, bool) { return ActiveTextMessageListHref("/msg/" + id) }
	if err := activelist.StampLink(ctx, s.active, id, &p.ActiveTextMessageListLink, derive); err != nil {
		log.Printf("messaging: counting active text messages for %s: %s; serving no ActiveTextMessageListLink", logsafe.Quote(id), logsafe.Err(err))
	}
}
