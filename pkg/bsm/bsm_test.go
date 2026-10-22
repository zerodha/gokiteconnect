package bsm

import (
	"errors"
	"math"
	"testing"
)

func near(got, want, tolerance float64) bool { return math.Abs(got-want) <= tolerance }

func TestPriceMatchesKnownValues(t *testing.T) {
	// Hull's example: S=42, K=40, r=10%, σ=20%, six months.
	call := Params{Kind: Call, Spot: 42, Strike: 40, Years: 0.5, Rate: 0.1}
	put := call
	put.Kind = Put
	if got := Price(call, 0.2); !near(got, 4.76, 0.005) {
		t.Errorf("call = %.4f, want 4.76", got)
	}
	if got := Price(put, 0.2); !near(got, 0.81, 0.005) {
		t.Errorf("put = %.4f, want 0.81", got)
	}
	// Put-call parity: C - P = S - K·e^(-rT).
	if parity := Price(call, 0.2) - Price(put, 0.2) - (42 - 40*math.Exp(-0.05)); !near(parity, 0, 1e-9) {
		t.Errorf("parity is off by %g", parity)
	}
	// With no time or volatility, an option is worth its intrinsic value.
	if got := Price(Params{Kind: Call, Spot: 42, Strike: 40}, 0.2); got != 2 {
		t.Errorf("expired call = %g, want 2", got)
	}
}

func TestImpliedVolatilityRoundTrips(t *testing.T) {
	for _, kind := range []Kind{Call, Put} {
		for _, strike := range []float64{22000, 24500, 25000, 25500, 28000} {
			for _, volatility := range []float64{0.08, 0.15, 0.4, 1.2} {
				p := Params{Kind: kind, Spot: 25000, Strike: strike, Years: 7.0 / 365, Rate: 0.065}
				premium := Price(p, volatility)
				// Without time value, every low volatility gives the same price.
				if premium-intrinsic(kind, p.Spot, strike*math.Exp(-p.Rate*p.Years)) < 0.05 {
					continue
				}
				got, err := ImpliedVolatility(p, premium)
				if err != nil {
					t.Errorf("%v %g at %g: %v", kind, strike, volatility, err)
					continue
				}
				if back := Price(p, got); !near(back, premium, 1e-3) {
					t.Errorf("%v %g at %g: σ %g prices %g, want %g", kind, strike, volatility, got, back, premium)
				}
			}
		}
	}
}

func TestImpliedVolatilityRejectsImpossiblePremiums(t *testing.T) {
	p := Params{Kind: Call, Spot: 25000, Strike: 24000, Years: 0.1, Rate: 0.065}
	for _, premium := range []float64{0, 500, 25000} {
		if _, err := ImpliedVolatility(p, premium); !errors.Is(err, ErrNoVolatility) {
			t.Errorf("premium %g: err = %v", premium, err)
		}
	}
	if _, err := ImpliedVolatility(Params{Kind: Put, Spot: 25000, Strike: 25000}, 100); !errors.Is(err, ErrNoVolatility) {
		t.Errorf("expired: err = %v", err)
	}
}

func TestProbabilityOfProfit(t *testing.T) {
	// A bought at-the-money option needs the underlying to move past the
	// premium, so it profits less than half the time; calls and puts on the
	// same strike sum to a bit under one.
	call := Params{Kind: Call, Spot: 25000, Strike: 25000, Years: 7.0 / 365, Rate: 0.065}
	put := call
	put.Kind = Put
	callPOP, err := ProbabilityOfProfit(call, Price(call, 0.12))
	if err != nil {
		t.Fatal(err)
	}
	putPOP, err := ProbabilityOfProfit(put, Price(put, 0.12))
	if err != nil {
		t.Fatal(err)
	}
	if callPOP <= 0.3 || callPOP >= 0.5 || putPOP <= 0.3 || putPOP >= 0.5 {
		t.Errorf("POP call %.3f put %.3f, want each in (0.3, 0.5)", callPOP, putPOP)
	}
	// Deep out-of-the-money options rarely pay.
	far := Params{Kind: Call, Spot: 25000, Strike: 27000, Years: 7.0 / 365, Rate: 0.065}
	if pop, err := ProbabilityOfProfit(far, Price(far, 0.12)); err != nil || pop > 0.01 {
		t.Errorf("far call POP = %g, %v", pop, err)
	}
	// ProbabilityAbove is N(d2) and halves at the forward.
	forward := 25000 * math.Exp(0.065*0.25)
	if got := ProbabilityAbove(25000, forward, 0.25, 0.065, 1e-9); got < 0.49 || got > 0.51 {
		t.Errorf("P(above forward) with no volatility = %g", got)
	}
}
