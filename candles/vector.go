package candles

import (
	"math"
	"sort"
)

// Numeric is the set of values supported by Vector.
type Numeric interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// Vector is a numeric series.
type Vector[T Numeric] []T

// Sum returns the compensated sum of v.
func (v Vector[T]) Sum() (float64, bool) {
	return v.compensatedSum(1)
}

// Mean returns the arithmetic mean of v.
func (v Vector[T]) Mean() (float64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	if sum, ok := v.Sum(); ok {
		return sum / float64(len(v)), true
	}
	return v.compensatedSum(1 / float64(len(v)))
}

// Min returns the smallest value in v without converting its type.
func (v Vector[T]) Min() (T, bool) {
	var zero T
	if len(v) == 0 {
		return zero, false
	}

	minimum := v[0]
	if _, ok := finiteFloat64(minimum); !ok {
		return zero, false
	}
	for _, value := range v[1:] {
		if _, ok := finiteFloat64(value); !ok {
			return zero, false
		}
		if value < minimum {
			minimum = value
		}
	}
	return minimum, true
}

// Max returns the largest value in v without converting its type.
func (v Vector[T]) Max() (T, bool) {
	var zero T
	if len(v) == 0 {
		return zero, false
	}

	maximum := v[0]
	if _, ok := finiteFloat64(maximum); !ok {
		return zero, false
	}
	for _, value := range v[1:] {
		if _, ok := finiteFloat64(value); !ok {
			return zero, false
		}
		if value > maximum {
			maximum = value
		}
	}
	return maximum, true
}

// Median returns the 50th percentile of v.
func (v Vector[T]) Median() (float64, bool) {
	return v.Quantile(0.5)
}

// Variance returns the population variance of v using Welford's algorithm.
func (v Vector[T]) Variance() (float64, bool) {
	if len(v) == 0 {
		return 0, false
	}

	var mean, sumSquaredDifferences float64
	for i, raw := range v {
		value, ok := finiteFloat64(raw)
		if !ok {
			return 0, false
		}

		count := float64(i + 1)
		delta := value - mean
		mean += delta / count
		deltaFromUpdatedMean := value - mean
		sumSquaredDifferences += delta * deltaFromUpdatedMean
		if !isFinite(mean) || !isFinite(sumSquaredDifferences) {
			return 0, false
		}
	}

	variance := sumSquaredDifferences / float64(len(v))
	if !isFinite(variance) || variance < 0 {
		return 0, false
	}
	return variance, true
}

// StdDev returns the population standard deviation of v.
func (v Vector[T]) StdDev() (float64, bool) {
	variance, ok := v.Variance()
	if !ok {
		return 0, false
	}
	return math.Sqrt(variance), true
}

// Quantile returns the linearly interpolated q quantile of v, where q is in
// the inclusive range [0, 1]. It sorts a copy and never mutates v.
func (v Vector[T]) Quantile(q float64) (float64, bool) {
	if !isFinite(q) || q < 0 || q > 1 {
		return 0, false
	}

	values, ok := v.float64Values()
	if !ok {
		return 0, false
	}
	sort.Float64s(values)

	position := q * float64(len(values)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return values[lower], true
	}

	fraction := position - float64(lower)
	result := values[lower]*(1-fraction) + values[upper]*fraction
	if !isFinite(result) {
		return 0, false
	}
	return result, true
}

func (v Vector[T]) float64Values() ([]float64, bool) {
	if len(v) == 0 {
		return nil, false
	}

	values := make([]float64, len(v))
	for i, raw := range v {
		value, ok := finiteFloat64(raw)
		if !ok {
			return nil, false
		}
		values[i] = value
	}
	return values, true
}

func (v Vector[T]) compensatedSum(scale float64) (float64, bool) {
	if len(v) == 0 {
		return 0, false
	}

	var sum, compensation float64
	for _, raw := range v {
		value, ok := finiteFloat64(raw)
		if !ok {
			return 0, false
		}
		value *= scale

		next := sum + value
		if math.Abs(sum) >= math.Abs(value) {
			compensation += (sum - next) + value
		} else {
			compensation += (value - next) + sum
		}
		sum = next
	}

	result := sum + compensation
	if !isFinite(result) {
		return 0, false
	}
	return result, true
}

func finiteFloat64[T Numeric](value T) (float64, bool) {
	converted := float64(value)
	return converted, isFinite(converted)
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
