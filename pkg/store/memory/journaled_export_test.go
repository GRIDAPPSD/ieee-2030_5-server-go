package memory

import "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"

// SetJournalFault makes every journal write of s fail while fault returns an
// error. Set it before the writes it should affect.
func SetJournalFault[T store.Copier[T]](s *JournaledScopedStore[T], fault func() error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.fault = fault
}

// JournalCarrying reports whether s is collecting entries for a compaction.
func JournalCarrying[T store.Copier[T]](s *JournaledScopedStore[T]) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.since != nil
}
