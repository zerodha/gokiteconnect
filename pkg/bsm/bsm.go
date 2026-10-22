// Package bsm prices European options with the Black-Scholes-Merton model,
// backs out implied volatility from a price, and gives the model's
// probability of the underlying finishing above or below a level.
//
// Time is in years and rates and volatility are annualised decimals, so 7%
// is 0.07. The underlying pays no dividends.
package bsm

import (
	"errors"
	"math"
)

// Kind is a call or a put.
type Kind int

const (
	Call Kind = iota
	Put
)

// ErrNoVolatility means no volatility prices the option at the given
// premium: the premium is below the option's no-arbitrage floor or above its
// ceiling, or an input is not positive.
var ErrNoVolatility = errors.New("bsm: no volatility matches the premium")

// Params are an option's inputs, without its volatility.
type Params struct {
	Kind   Kind
	Spot   float64
	Strike float64
	// Years is the time to expiry.
	Years float64
	// Rate is the continuously compounded risk-free rate.
	Rate float64
}

func (p Params) valid() bool {
	return p.Spot > 0 && p.Strike > 0 && p.Years > 0
}

func (p Params) d1d2(volatility float64) (float64, float64) {
	root := volatility * math.Sqrt(p.Years)
	d1 := (math.Log(p.Spot/p.Strike) + (p.Rate+volatility*volatility/2)*p.Years) / root
	return d1, d1 - root
}

// Price is the option's model value at the volatility.
func Price(p Params, volatility float64) float64 {
	discounted := p.Strike * math.Exp(-p.Rate*p.Years)
	if !p.valid() || volatility <= 0 {
		return intrinsic(p.Kind, p.Spot, discounted)
	}
	d1, d2 := p.d1d2(volatility)
	if p.Kind == Put {
		return discounted*cdf(-d2) - p.Spot*cdf(-d1)
	}
	return p.Spot*cdf(d1) - discounted*cdf(d2)
}

// Vega is the price's change per unit of volatility.
func Vega(p Params, volatility float64) float64 {
	if !p.valid() || volatility <= 0 {
		return 0
	}
	d1, _ := p.d1d2(volatility)
	return p.Spot * pdf(d1) * math.Sqrt(p.Years)
}

// ImpliedVolatility is the volatility at which the option's model value is
// premium, to within a thousandth of a rupee.
func ImpliedVolatility(p Params, premium float64) (float64, error) {
	if !p.valid() || premium <= 0 {
		return 0, ErrNoVolatility
	}
	discounted := p.Strike * math.Exp(-p.Rate*p.Years)
	floor, ceiling := intrinsic(p.Kind, p.Spot, discounted), p.Spot
	if p.Kind == Put {
		ceiling = discounted
	}
	if premium <= floor || premium >= ceiling {
		return 0, ErrNoVolatility
	}
	// Price rises with volatility, so bisect, starting Newton steps from the
	// midpoint and falling back to bisection when a step leaves the bracket.
	low, high := 1e-6, 10.0
	volatility := 0.3
	for range 100 {
		difference := Price(p, volatility) - premium
		if math.Abs(difference) < 1e-3 {
			return volatility, nil
		}
		if difference > 0 {
			high = volatility
		} else {
			low = volatility
		}
		next := (low + high) / 2
		if vega := Vega(p, volatility); vega > 0 {
			next = volatility - difference/vega
		}
		if math.IsNaN(next) || next <= low || next >= high {
			next = (low + high) / 2
		}
		volatility = next
	}
	return volatility, nil
}

// ProbabilityAbove is the model's risk-neutral probability that the
// underlying ends above level at expiry, N(d2) with level as the strike.
func ProbabilityAbove(spot, level, years, rate, volatility float64) float64 {
	if level <= 0 {
		return 1
	}
	p := Params{Spot: spot, Strike: level, Years: years, Rate: rate}
	if !p.valid() || volatility <= 0 {
		forward := spot * math.Exp(rate*max(years, 0))
		if forward > level {
			return 1
		}
		return 0
	}
	_, d2 := p.d1d2(volatility)
	return cdf(d2)
}

// ProbabilityOfProfit is the chance a bought option is worth more than its
// premium at expiry: the underlying ends above the breakeven for a call, or
// below it for a put. The volatility is implied from the premium.
func ProbabilityOfProfit(p Params, premium float64) (float64, error) {
	volatility, err := ImpliedVolatility(p, premium)
	if err != nil {
		return 0, err
	}
	if p.Kind == Put {
		return 1 - ProbabilityAbove(p.Spot, p.Strike-premium, p.Years, p.Rate, volatility), nil
	}
	return ProbabilityAbove(p.Spot, p.Strike+premium, p.Years, p.Rate, volatility), nil
}

func intrinsic(kind Kind, spot, strike float64) float64 {
	if kind == Put {
		return max(strike-spot, 0)
	}
	return max(spot-strike, 0)
}

func cdf(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

func pdf(x float64) float64 { return math.Exp(-x*x/2) / math.Sqrt(2*math.Pi) }
