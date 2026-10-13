package sep2admin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func openNothing(context.Context, StreamRequest, StreamSendFunc) error { return nil }

func TestRegisterValidatesStream(t *testing.T) {
	cases := []struct {
		name   string
		stream *Stream
		ok     bool
	}{
		{"no stream", nil, true},
		{"minimal", &Stream{Param: StreamParam{MaxLen: 1, Charset: "a"}, Open: openNothing}, true},
		{"MaxLen at cap, charset at the printable bounds", &Stream{Param: StreamParam{MaxLen: MaxStreamParamLen, Charset: " ~"}, Open: openNothing}, true},
		{"nil Open", &Stream{Param: StreamParam{MaxLen: 1, Charset: "a"}}, false},
		{"MaxLen 0", &Stream{Param: StreamParam{MaxLen: 0, Charset: "a"}, Open: openNothing}, false},
		{"MaxLen over cap", &Stream{Param: StreamParam{MaxLen: MaxStreamParamLen + 1, Charset: "a"}, Open: openNothing}, false},
		{"empty charset", &Stream{Param: StreamParam{MaxLen: 1}, Open: openNothing}, false},
		{"control byte in charset", &Stream{Param: StreamParam{MaxLen: 1, Charset: "a\n"}, Open: openNothing}, false},
		{"DEL in charset", &Stream{Param: StreamParam{MaxLen: 1, Charset: "a\x7f"}, Open: openNothing}, false},
		{"non-ASCII in charset", &Stream{Param: StreamParam{MaxLen: 1, Charset: "a\xc3\xa9"}, Open: openNothing}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := graftPanel("streamer", 1)
			p.Stream = tc.stream
			err := NewRegistry().Register(p)
			if tc.ok && err != nil {
				t.Fatalf("Register = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidStream) {
				t.Fatalf("Register = %v, want ErrInvalidStream", err)
			}
		})
	}
}

func TestStreamParamValidate(t *testing.T) {
	p := StreamParam{MaxLen: 8, Charset: "abc/._-"}
	cases := []struct {
		name string
		v    string
		ok   bool
	}{
		{"one byte", "a", true},
		{"at MaxLen", "/a.b_c-a", true},
		{"empty", "", false},
		{"over MaxLen", "aaaaaaaaa", false},
		{"byte outside charset", "abd", false},
		{"space outside charset", "a b", false},
		{"multibyte outside charset", "a\xc3\xa9", false},
		{"NUL", "a\x00", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Validate(tc.v)
			if tc.ok && err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tc.v, err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidStreamParam) {
				t.Fatalf("Validate(%q) = %v, want ErrInvalidStreamParam", tc.v, err)
			}
			if err != nil && strings.Contains(err.Error(), tc.v) && tc.v != "" {
				t.Fatalf("error %q carries the refused value", err)
			}
		})
	}
}
