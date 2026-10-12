package candles

// WindowedVector is a series of numeric windows.
type WindowedVector[T Numeric] [][]T

// Rolling returns complete sliding windows of size elements. The returned
// windows share storage with v. Invalid sizes return nil.
func (v Vector[T]) Rolling(size int) WindowedVector[T] {
	if size <= 0 || size > len(v) {
		return nil
	}

	windows := make(WindowedVector[T], len(v)-size+1)
	for i := range windows {
		windows[i] = v[i : i+size]
	}
	return windows
}

// Sum returns the sum of every window.
func (v WindowedVector[T]) Sum() (Vector[float64], bool) {
	return aggregateWindows(v, func(window Vector[T]) (float64, bool) {
		return window.Sum()
	})
}

// Mean returns the arithmetic mean of every window.
func (v WindowedVector[T]) Mean() (Vector[float64], bool) {
	return aggregateWindows(v, func(window Vector[T]) (float64, bool) {
		return window.Mean()
	})
}

// Min returns the smallest value in every window.
func (v WindowedVector[T]) Min() (Vector[T], bool) {
	return aggregateWindows(v, func(window Vector[T]) (T, bool) {
		return window.Min()
	})
}

// Max returns the largest value in every window.
func (v WindowedVector[T]) Max() (Vector[T], bool) {
	return aggregateWindows(v, func(window Vector[T]) (T, bool) {
		return window.Max()
	})
}

// Median returns the median of every window.
func (v WindowedVector[T]) Median() (Vector[float64], bool) {
	return aggregateWindows(v, func(window Vector[T]) (float64, bool) {
		return window.Median()
	})
}

// Variance returns the population variance of every window.
func (v WindowedVector[T]) Variance() (Vector[float64], bool) {
	return aggregateWindows(v, func(window Vector[T]) (float64, bool) {
		return window.Variance()
	})
}

// StdDev returns the population standard deviation of every window.
func (v WindowedVector[T]) StdDev() (Vector[float64], bool) {
	return aggregateWindows(v, func(window Vector[T]) (float64, bool) {
		return window.StdDev()
	})
}

// Quantile returns the linearly interpolated q quantile of every window.
func (v WindowedVector[T]) Quantile(q float64) (Vector[float64], bool) {
	return aggregateWindows(v, func(window Vector[T]) (float64, bool) {
		return window.Quantile(q)
	})
}

func aggregateWindows[T Numeric, R Numeric](
	windows WindowedVector[T],
	statistic func(Vector[T]) (R, bool),
) (Vector[R], bool) {
	if len(windows) == 0 {
		return nil, false
	}

	results := make(Vector[R], len(windows))
	for i, window := range windows {
		value, ok := statistic(Vector[T](window))
		if !ok {
			return nil, false
		}
		results[i] = value
	}
	return results, true
}
