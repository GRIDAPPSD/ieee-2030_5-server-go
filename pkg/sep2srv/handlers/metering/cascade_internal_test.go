package metering

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCascade_ErrorsNameTheStepAndStore(t *testing.T) {
	t.Parallel()
	ran := 0
	ok := cascadeStep{
		name: "first", store: "*fake.A",
		probe: func(context.Context) error { return nil },
		run:   func(context.Context) (uint32, error) { ran++; return 2, nil },
	}
	boom := errors.New("backend gone")
	failing := cascadeStep{
		name: "second", store: "*fake.B",
		probe: func(context.Context) error { return nil },
		run:   func(context.Context) (uint32, error) { return 0, boom },
	}
	err := cascade(context.Background(), "upt/u1", ok, failing)
	if !errors.Is(err, boom) {
		t.Fatalf("cascade error = %v, want it to wrap the store's error", err)
	}
	for _, want := range []string{`"second"`, "*fake.B", "2 record(s) removed", "left in place"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("cascade error %q does not contain %q", err, want)
		}
	}

	refused := cascadeStep{
		name: "third", store: "*fake.C",
		probe: func(context.Context) error { return errors.New("cannot cascade") },
		run:   func(context.Context) (uint32, error) { t.Error("a refused step ran"); return 0, nil },
	}
	ran = 0
	if err := cascade(context.Background(), "upt/u1", ok, refused); err == nil || !strings.Contains(err.Error(), `"third"`) {
		t.Errorf("refused cascade error = %v, want it to name step third", err)
	}
	if ran != 0 {
		t.Errorf("an earlier step ran %d time(s) although a later one was refused", ran)
	}
}
