package dsp

// Autocorrelation writes the normalized autocorrelation of x into out, for
// lags 0 to len(out)-1. The mean of x is removed first. The result is
//
//	r[k] = sum (x[n]-u)(x[n+k]-u) / sum (x[n]-u)^2
//
// so r[0] = 1 and a strong repeat at lag k gives r[k] near 1. The sum for lag
// k covers only the n-k overlapping samples, so long lags read lower. If x
// has no variance, or a lag has no overlap, the value is 0.
func Autocorrelation(x, out []float64) {
	for k := range out {
		out[k] = 0
	}
	n := len(x)
	if n == 0 {
		return
	}

	var mean, sumSq float64
	for _, v := range x {
		mean += v
		sumSq += v * v
	}
	mean /= float64(n)

	var energy float64
	for _, v := range x {
		d := v - mean
		energy += d * d
	}
	// Treat rounding noise in a constant input as no variance.
	if energy <= 1e-12*sumSq || energy == 0 {
		return
	}

	for k := 0; k < len(out) && k < n; k++ {
		var s float64
		for i := 0; i+k < n; i++ {
			s += (x[i] - mean) * (x[i+k] - mean)
		}
		out[k] = s / energy
	}
}
