package zlgz

import "testing"

func parse(rows ...[]int8) ([]int8, int, int) {
	h := len(rows)
	w := len(rows[0])
	out := make([]int8, h*w)
	for r, row := range rows {
		copy(out[r*w:], row)
	}
	return out, h, w
}

func TestDirectPair(t *testing.T) {
	b, h, w := parse([]int8{0, 0})
	got := Solve(b, h, w, 8000, 200000, 24)
	if !got.Solved || len(got.Steps) != 1 || got.Steps[0].Shift != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestLineOfSight(t *testing.T) {
	b, h, w := parse([]int8{0, -1, 0})
	got := Solve(b, h, w, 8000, 200000, 24)
	if !got.Solved || len(got.Steps) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestSlideThenMatch(t *testing.T) {
	b, h, w := parse([]int8{0, -1}, []int8{-1, 0})
	got := Solve(b, h, w, 8000, 200000, 24)
	if !got.Solved {
		t.Fatalf("%+v", got)
	}
	ok := false
	for _, m := range got.Steps {
		if m.Shift > 0 {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("expected a slide: %+v", got.Steps)
	}
}

func TestOddRejected(t *testing.T) {
	b, h, w := parse([]int8{0, 0, 0})
	got := Solve(b, h, w, 8000, 200000, 24)
	if got.Solved || got.Reason != "odd" {
		t.Fatalf("%+v", got)
	}
}

func TestShiftCapacity(t *testing.T) {
	b, h, w := parse([]int8{0, 1, -1, -1})
	if shiftCapacity(b, h, w, 0, 0, 0, 1) != 2 {
		t.Fatal("capacity")
	}
}

func TestLookaheadPrefersZeroShift(t *testing.T) {
	b, h, w := parse([]int8{0, 0, 1, 1})
	m, ok := Lookahead(b, h, w, 4, 24)
	if !ok || m.Shift != 0 {
		t.Fatalf("%v %+v", ok, m)
	}
}
