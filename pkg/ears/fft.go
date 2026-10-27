package ears

import (
	"math"
	"math/cmplx"
)

// fft is an in-place radix-2 FFT; len(x) must be a power of two.
func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < size/2; k++ {
				a, b := x[start+k], x[start+k+size/2]*wk
				x[start+k], x[start+k+size/2] = a+b, a-b
				wk *= w
			}
		}
	}
}

func hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n)) // periodic, as scipy
	}
	return w
}

// power returns |FFT|² of one windowed frame, bins 0..n/2.
func power(frame, win []float64, buf []complex128) []float64 {
	for i, v := range frame {
		buf[i] = complex(v*win[i], 0)
	}
	fft(buf)
	p := make([]float64, len(buf)/2+1)
	for i := range p {
		p[i] = real(buf[i])*real(buf[i]) + imag(buf[i])*imag(buf[i])
	}
	return p
}
