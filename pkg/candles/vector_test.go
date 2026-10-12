package candles

import (
	"math"
	"reflect"
	"testing"
)

type customNumber int32

func TestVectorDescriptiveStatistics(t *testing.T) {
	values := Vector[customNumber]{1, 2, 3, 4}

	assertFloatStatistic(t, "sum", values.Sum, 10)
	assertFloatStatistic(t, "mean", values.Mean, 2.5)
	assertFloatStatistic(t, "median", values.Median, 2.5)
	assertFloatStatistic(t, "variance", values.Variance, 1.25)
	assertFloatStatistic(t, "standard deviation", values.StdDev, math.Sqrt(1.25))

	minimum, ok := values.Min()
	if !ok || minimum != 1 {
		t.Fatalf("Min() = (%v, %t), want (1, true)", minimum, ok)
	}
	maximum, ok := values.Max()
	if !ok || maximum != 4 {
		t.Fatalf("Max() = (%v, %t), want (4, true)", maximum, ok)
	}

	quantile, ok := values.Quantile(0.25)
	if !ok || !approximatelyEqual(quantile, 1.75) {
		t.Fatalf("Quantile(0.25) = (%v, %t), want (1.75, true)", quantile, ok)
	}
	minimumQuantile, ok := values.Quantile(0)
	if !ok || minimumQuantile != 1 {
		t.Fatalf("Quantile(0) = (%v, %t), want (1, true)", minimumQuantile, ok)
	}
	maximumQuantile, ok := values.Quantile(1)
	if !ok || maximumQuantile != 4 {
		t.Fatalf("Quantile(1) = (%v, %t), want (4, true)", maximumQuantile, ok)
	}

	oddMedian, ok := (Vector[int]{9, 1, 5}).Median()
	if !ok || oddMedian != 5 {
		t.Fatalf("odd Median() = (%v, %t), want (5, true)", oddMedian, ok)
	}
}

func TestVectorKnownPopulationVariance(t *testing.T) {
	values := Vector[float64]{2, 4, 4, 4, 5, 5, 7, 9}
	assertFloatStatistic(t, "variance", values.Variance, 4)
	assertFloatStatistic(t, "standard deviation", values.StdDev, 2)
}

func TestVectorSumUsesCompensation(t *testing.T) {
	values := Vector[float64]{1e16, 1, -1e16}
	assertFloatStatistic(t, "sum", values.Sum, 1)
	assertFloatStatistic(t, "mean", values.Mean, 1.0/3.0)

	largeValues := Vector[float64]{math.MaxFloat64, math.MaxFloat64}
	assertFloatStatistic(t, "large finite mean", largeValues.Mean, math.MaxFloat64)
	assertFloatStatisticInvalid(t, "overflowing sum", largeValues.Sum)
}

func TestQuantileDoesNotMutateVector(t *testing.T) {
	values := Vector[int]{4, 1, 3, 2}
	want := append(Vector[int](nil), values...)

	median, ok := values.Median()
	if !ok || median != 2.5 {
		t.Fatalf("Median() = (%v, %t), want (2.5, true)", median, ok)
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("Median() mutated vector: got %v, want %v", values, want)
	}
}

func TestVectorRejectsUndefinedStatistics(t *testing.T) {
	empty := Vector[float64]{}
	assertFloatStatisticInvalid(t, "empty sum", empty.Sum)
	assertFloatStatisticInvalid(t, "empty mean", empty.Mean)
	assertFloatStatisticInvalid(t, "empty median", empty.Median)
	assertFloatStatisticInvalid(t, "empty variance", empty.Variance)
	assertFloatStatisticInvalid(t, "empty standard deviation", empty.StdDev)
	if _, ok := empty.Min(); ok {
		t.Fatal("empty Min() ok = true, want false")
	}
	if _, ok := empty.Max(); ok {
		t.Fatal("empty Max() ok = true, want false")
	}

	values := Vector[float64]{1, 2, 3}
	for _, q := range []float64{-0.01, 1.01, math.NaN(), math.Inf(1)} {
		if _, ok := values.Quantile(q); ok {
			t.Errorf("Quantile(%v) ok = true, want false", q)
		}
	}

	for name, nonFinite := range map[string]float64{
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			invalid := Vector[float64]{1, nonFinite, 3}
			assertFloatStatisticInvalid(t, "sum", invalid.Sum)
			assertFloatStatisticInvalid(t, "mean", invalid.Mean)
			assertFloatStatisticInvalid(t, "median", invalid.Median)
			assertFloatStatisticInvalid(t, "variance", invalid.Variance)
			assertFloatStatisticInvalid(t, "standard deviation", invalid.StdDev)
			if _, ok := invalid.Min(); ok {
				t.Error("Min() ok = true, want false")
			}
			if _, ok := invalid.Max(); ok {
				t.Error("Max() ok = true, want false")
			}
		})
	}
}

func assertFloatStatistic(t *testing.T, name string, statistic func() (float64, bool), want float64) {
	t.Helper()
	got, ok := statistic()
	if !ok || !approximatelyEqual(got, want) {
		t.Fatalf("%s = (%v, %t), want (%v, true)", name, got, ok, want)
	}
}

func assertFloatStatisticInvalid(t *testing.T, name string, statistic func() (float64, bool)) {
	t.Helper()
	if got, ok := statistic(); ok {
		t.Fatalf("%s = (%v, true), want (_, false)", name, got)
	}
}

func approximatelyEqual(left, right float64) bool {
	const tolerance = 1e-12
	difference := math.Abs(left - right)
	scale := math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
	return difference <= tolerance*scale
}
