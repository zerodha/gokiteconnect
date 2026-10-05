package candles

import (
	"reflect"
	"testing"
)

func TestRollingReturnsCompleteZeroCopyWindows(t *testing.T) {
	values := Vector[int]{1, 2, 3, 4}
	windows := values.Rolling(3)
	want := WindowedVector[int]{{1, 2, 3}, {2, 3, 4}}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("Rolling(3) = %v, want %v", windows, want)
	}

	values[1] = 20
	if windows[0][1] != 20 || windows[1][0] != 20 {
		t.Fatalf("rolling windows do not share source storage: %v", windows)
	}

	for _, size := range []int{-1, 0, len(values) + 1} {
		if got := values.Rolling(size); got != nil {
			t.Errorf("Rolling(%d) = %v, want nil", size, got)
		}
	}
}

func TestWindowedVectorStatistics(t *testing.T) {
	windows := Vector[int]{1, 2, 3, 4}.Rolling(2)

	assertFloatVectorStatistic(t, "sum", windows.Sum, Vector[float64]{3, 5, 7})
	assertFloatVectorStatistic(t, "mean", windows.Mean, Vector[float64]{1.5, 2.5, 3.5})
	assertFloatVectorStatistic(t, "median", windows.Median, Vector[float64]{1.5, 2.5, 3.5})
	assertFloatVectorStatistic(t, "variance", windows.Variance, Vector[float64]{0.25, 0.25, 0.25})
	assertFloatVectorStatistic(t, "standard deviation", windows.StdDev, Vector[float64]{0.5, 0.5, 0.5})

	minimums, ok := windows.Min()
	if !ok || !reflect.DeepEqual(minimums, Vector[int]{1, 2, 3}) {
		t.Fatalf("Min() = (%v, %t), want ([1 2 3], true)", minimums, ok)
	}
	maximums, ok := windows.Max()
	if !ok || !reflect.DeepEqual(maximums, Vector[int]{2, 3, 4}) {
		t.Fatalf("Max() = (%v, %t), want ([2 3 4], true)", maximums, ok)
	}

	quantiles, ok := windows.Quantile(0.25)
	if !ok || !floatVectorsEqual(quantiles, Vector[float64]{1.25, 2.25, 3.25}) {
		t.Fatalf("Quantile(0.25) = (%v, %t), want ([1.25 2.25 3.25], true)", quantiles, ok)
	}
}

func TestWindowedVectorRejectsInvalidWindows(t *testing.T) {
	var nilWindows WindowedVector[int]
	if _, ok := nilWindows.Mean(); ok {
		t.Fatal("nil WindowedVector.Mean() ok = true, want false")
	}
	emptyWindows := WindowedVector[int]{}
	if _, ok := emptyWindows.Mean(); ok {
		t.Fatal("empty WindowedVector.Mean() ok = true, want false")
	}

	windowsWithEmpty := WindowedVector[int]{{1, 2}, {}}
	if _, ok := windowsWithEmpty.Sum(); ok {
		t.Fatal("WindowedVector with empty window Sum() ok = true, want false")
	}

	windows := Vector[int]{1, 2, 3}.Rolling(2)
	if _, ok := windows.Quantile(-1); ok {
		t.Fatal("WindowedVector.Quantile(-1) ok = true, want false")
	}
}

func TestWindowedQuantileDoesNotMutateSource(t *testing.T) {
	values := Vector[int]{3, 1, 2, 4}
	want := append(Vector[int](nil), values...)

	if _, ok := values.Rolling(3).Median(); !ok {
		t.Fatal("rolling Median() ok = false, want true")
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("rolling Median() mutated source: got %v, want %v", values, want)
	}
}

func assertFloatVectorStatistic(
	t *testing.T,
	name string,
	statistic func() (Vector[float64], bool),
	want Vector[float64],
) {
	t.Helper()
	got, ok := statistic()
	if !ok || !floatVectorsEqual(got, want) {
		t.Fatalf("%s = (%v, %t), want (%v, true)", name, got, ok, want)
	}
}

func floatVectorsEqual(left, right Vector[float64]) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !approximatelyEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}
