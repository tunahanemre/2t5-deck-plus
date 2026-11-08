package accel

import (
	"math"
	"testing"
	"time"
)

func TestCurveUntouched(t *testing.T) {
	c := New(0.002, 1.5)
	if got := c.Amount(4); math.Abs(got-0.002*8) > 1e-12 {
		t.Fatalf("Curve.Amount changed: %v", got)
	}
}

func TestLinearIsIdentityEverywhere(t *testing.T) {
	for _, name := range []string{"", ProfileLinear} {
		p, ok := ProfileByName(name)
		if !ok || !IsLinear(name) {
			t.Fatalf("%q must be linear", name)
		}
		for _, r := range []float64{0, 0.5, 7, 100, 1e9, -30, math.NaN(), math.Inf(1)} {
			if m := p.Multiplier(r); m != 1 {
				t.Fatalf("linear multiplier at %v = %v", r, m)
			}
		}
		if p.Scale(-3, 55) != -3 {
			t.Fatal("linear Scale not identity")
		}
	}
}

func TestUnknownProfileIsLinearNotOK(t *testing.T) {
	p, ok := ProfileByName("turbo")
	if ok || p.Multiplier(50) != 1 {
		t.Fatal("unknown profile must report !ok and be linear")
	}
}

func TestTablesWellFormedAndMonotone(t *testing.T) {
	for _, n := range []string{ProfilePrecise, ProfileStandard, ProfileFast} {
		p, _ := ProfileByName(n)
		k := p.Table()
		if len(k) < 2 {
			t.Fatalf("%s: too few knots", n)
		}
		for i := 1; i < len(k); i++ {
			if k[i].TicksPerSec <= k[i-1].TicksPerSec {
				t.Fatalf("%s: rates not strictly increasing", n)
			}
			if k[i].Multiplier < k[i-1].Multiplier {
				t.Fatalf("%s: multiplier decreases", n)
			}
		}
		// monotone along a dense sweep
		prev := p.Multiplier(0)
		for r := 0.0; r <= 400; r += 0.5 {
			m := p.Multiplier(r)
			if m+1e-12 < prev {
				t.Fatalf("%s not monotone at %v", n, r)
			}
			prev = m
		}
	}
}

func TestKnownValues(t *testing.T) {
	pre, _ := ProfileByName(ProfilePrecise)
	std, _ := ProfileByName(ProfileStandard)
	fast, _ := ProfileByName(ProfileFast)
	cases := []struct {
		p    Profile
		r    float64
		want float64
	}{
		{pre, 0, 0.5}, {pre, 10, 0.75}, {pre, 5, 0.625}, {pre, 30, 1}, {pre, 1000, 2.5}, {pre, -10, 0.75},
		{std, 0, 0.8}, {std, 8, 1}, {std, 150, 6}, {std, 1e6, 6}, {std, 4, 0.9},
		{fast, 0, 1}, {fast, 6, 1.5}, {fast, 120, 12}, {fast, 13, 2.25},
	}
	for _, c := range cases {
		if got := c.p.Multiplier(c.r); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s at %v = %v, want %v", c.p.Name, c.r, got, c.want)
		}
	}
	if pre.Multiplier(math.NaN()) != 0.5 {
		t.Error("NaN rate must count as 0")
	}
}

func TestCarryKeepsRemainder(t *testing.T) {
	var c Carry
	total := 0
	for i := 0; i < 10; i++ {
		total += c.Add(0.3)
	}
	if total != 3 {
		t.Fatalf("carry lost ticks: %d", total)
	}
	var d Carry
	got := d.Add(-1.5) + d.Add(-1.5)
	if got != -3 {
		t.Fatalf("negative carry: %d", got)
	}
	if d.Add(math.NaN()) != 0 || d.Add(math.Inf(1)) != 0 {
		t.Fatal("non-finite must be ignored")
	}
}

func TestMomentumIntegralIndependentOfStepping(t *testing.T) {
	t0 := time.Unix(1000, 0)
	tau := 200 * time.Millisecond
	one := Momentum{Tau: tau}
	one.Kick(t0, 100)
	a := one.Advance(t0.Add(500 * time.Millisecond))
	many := Momentum{Tau: tau}
	many.Kick(t0, 100)
	var b float64
	for i := 1; i <= 50; i++ {
		b += many.Advance(t0.Add(time.Duration(i) * 10 * time.Millisecond))
	}
	if math.Abs(a-b) > 1e-6 {
		t.Fatalf("stepping changes the integral: %v vs %v", a, b)
	}
	want := 100 * 0.2 * (1 - math.Exp(-2.5))
	if math.Abs(a-want) > 1e-6 {
		t.Fatalf("integral %v want %v", a, want)
	}
}

func TestMomentumSignEndsAndDisabled(t *testing.T) {
	t0 := time.Unix(0, 0)
	m := Momentum{Tau: 100 * time.Millisecond}
	m.Kick(t0, -50)
	if !m.Active() {
		t.Fatal("should be active")
	}
	if v := m.Advance(t0.Add(50 * time.Millisecond)); v >= 0 {
		t.Fatalf("sign lost: %v", v)
	}
	m.Advance(t0.Add(5 * time.Second))
	if m.Active() {
		t.Fatal("tail must end")
	}
	if m.Advance(t0.Add(6*time.Second)) != 0 {
		t.Fatal("ended tail must produce nothing")
	}
	off := Momentum{}
	off.Kick(t0, 100)
	if off.Active() || off.Advance(t0.Add(time.Second)) != 0 {
		t.Fatal("Tau 0 = off")
	}
	slow := Momentum{Tau: time.Second}
	slow.Kick(t0, 0.5)
	if slow.Active() {
		t.Fatal("below the floor must not start")
	}
	// time going backwards is harmless
	b := Momentum{Tau: time.Second}
	b.Kick(t0, 10)
	if b.Advance(t0.Add(-time.Second)) != 0 {
		t.Fatal("negative dt")
	}
	// total bounded by v*tau
	c := Momentum{Tau: 100 * time.Millisecond}
	c.Kick(t0, 80)
	if got := c.Advance(t0.Add(time.Hour)); got > 80*0.1+1e-9 {
		t.Fatalf("tail exceeds v*tau: %v", got)
	}
}
