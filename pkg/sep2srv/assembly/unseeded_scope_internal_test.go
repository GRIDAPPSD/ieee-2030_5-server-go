package assembly

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// listFailsStore answers every List with a fixed error.
type listFailsStore struct {
	store.ScopedStore[sep2.DER]
	err error
}

func (s listFailsStore) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.DER], error) {
	return store.ListResult[sep2.DER]{}, s.err
}

// A failed existence check must be logged once with its error, not dropped.
// Not parallel: it captures the process-wide log output.
func TestLogUnseededScope_ListErrorIsLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	boom := errors.New("backend down")
	scoped := listFailsStore{ScopedStore: memory.NewScopedStore[sep2.DER](), err: boom}
	r := httptest.NewRequest("PUT", "/edev/d1/der/1", nil)
	logUnseededScope[sep2.DER](r, scoped, "d1", store.ErrNotFound)

	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "backend down") || strings.Contains(out, "never seeded") {
		t.Errorf("log = %q, want exactly one line carrying the List error and not claiming the scope was never seeded", out)
	}
}
