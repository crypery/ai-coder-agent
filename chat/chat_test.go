package chat

import (
	"bufio"
	"strings"
	"testing"
)

func TestNextInputSingleEnter(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("hello\nworld\n"))
	in, ok := nextInput(sc)
	if !ok || in != "hello" {
		t.Fatalf("first input = %q ok=%v", in, ok)
	}
	in, ok = nextInput(sc)
	if !ok || in != "world" {
		t.Fatalf("second input = %q ok=%v", in, ok)
	}
	if _, ok := nextInput(sc); ok {
		t.Fatal("expected EOF after two lines")
	}
}

func TestNextInputTrimsWhitespace(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("  padded  \n"))
	in, ok := nextInput(sc)
	if !ok || in != "padded" {
		t.Fatalf("input = %q ok=%v", in, ok)
	}
}

func TestNextInputNormalizesNewlines(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("line1" + "\r\n"))
	in, ok := nextInput(sc)
	if !ok || in != "line1" {
		t.Fatalf("input = %q ok=%v", in, ok)
	}
}

func TestNextInputSkipsEmptyTrailing(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("msg\n"))
	in, ok := nextInput(sc)
	if !ok || in != "msg" {
		t.Fatalf("input = %q ok=%v", in, ok)
	}
}
