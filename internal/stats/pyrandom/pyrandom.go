// SPDX-License-Identifier: AGPL-3.0-only

// Package pyrandom implements the subset of CPython's random module that
// AgentMeter needs for bit-exact parity with Python's bootstrap confidence
// intervals: MT19937 with CPython's init_by_array seeding, getrandbits and
// _randbelow/randrange.
//
// The implementation follows Modules/_randommodule.c (MT19937 by Takuji
// Nishimura and Makoto Matsumoto, as modified by CPython) and Lib/random.py's
// _randbelow_with_getrandbits. Only integer seeds in the int64 range are
// supported; CPython additionally accepts larger ints and hashable objects.
//
// A Random is not safe for concurrent use.
package pyrandom

import (
	"math/bits"
)

const (
	// MT19937 parameters.
	n         = 624
	m         = 397
	matrixA   = 0x9908b0df
	upperMask = 0x80000000
	lowerMask = 0x7fffffff
)

// Random is a CPython-compatible Mersenne Twister generator.
type Random struct {
	mt    [n]uint32
	index int
}

// New returns a generator seeded exactly like random.Random(seed).
//
// CPython uses the absolute value of an integer seed, splits it into 32-bit
// little-endian words, and runs init_by_array; seed 0 becomes the one-word
// key [0]. Negative seeds therefore match their positive counterparts.
func New(seed int64) *Random {
	r := new(Random)
	r.Seed(seed)
	return r
}

// Seed resets the generator state, matching random.Random(seed).
func (r *Random) Seed(seed int64) {
	// Absolute value as an unsigned 64-bit quantity. int64 min maps to 2**63.
	var u uint64
	if seed < 0 {
		u = uint64(-(seed + 1)) + 1
	} else {
		u = uint64(seed)
	}

	bitLen := bits.Len64(u)
	keyUsed := 1
	if bitLen > 0 {
		keyUsed = (bitLen-1)/32 + 1
	}
	key := make([]uint32, keyUsed)
	for i := 0; i < keyUsed; i++ {
		key[i] = uint32(u >> (32 * i))
	}
	r.initByArray(key)
}

// initGenrand mirrors init_genrand().
func (r *Random) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < n; i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i)
	}
	r.index = n
}

// initByArray mirrors init_by_array(), including its uint32 wraparound.
func (r *Random) initByArray(key []uint32) {
	r.initGenrand(19650218)

	i, j := 1, 0
	k := n
	if len(key) > k {
		k = len(key)
	}
	for ; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k = n - 1; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
	}
	// MSB is 1, assuring a non-zero initial array.
	r.mt[0] = 0x80000000
}

// Uint32 returns the next 32-bit value, mirroring genrand_uint32().
func (r *Random) Uint32() uint32 {
	if r.index >= n {
		var kk int
		for kk = 0; kk < n-m; kk++ {
			y := (r.mt[kk] & upperMask) | (r.mt[kk+1] & lowerMask)
			r.mt[kk] = r.mt[kk+m] ^ (y >> 1) ^ mag01(y)
		}
		for ; kk < n-1; kk++ {
			y := (r.mt[kk] & upperMask) | (r.mt[kk+1] & lowerMask)
			r.mt[kk] = r.mt[kk+(m-n)] ^ (y >> 1) ^ mag01(y)
		}
		y := (r.mt[n-1] & upperMask) | (r.mt[0] & lowerMask)
		r.mt[n-1] = r.mt[m-1] ^ (y >> 1) ^ mag01(y)
		r.index = 0
	}

	y := r.mt[r.index]
	r.index++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

func mag01(y uint32) uint32 {
	if y&1 == 0 {
		return 0
	}
	return matrixA
}

// GetRandBits mirrors random.Random.getrandbits(k) for 0 <= k <= 64.
//
// k == 0 returns 0 without consuming state (CPython does the same). For
// k <= 32 the single 32-bit draw is shifted right by 32-k. For larger k the
// value is assembled from little-endian 32-bit words, with only the most
// significant word shifted down to the requested bit count.
//
// k > 64 cannot fit in the uint64 return type; GetRandBits panics for it.
// CPython supports arbitrary k, but AgentMeter only ever draws n.bit_length()
// bits for randrange(n) with n <= 2**64.
func (r *Random) GetRandBits(k uint) uint64 {
	if k == 0 {
		return 0
	}
	if k <= 32 {
		return uint64(r.Uint32() >> (32 - k))
	}
	if k > 64 {
		panic("pyrandom: GetRandBits supports at most 64 bits")
	}
	words := (k-1)/32 + 1
	var result uint64
	for i := uint(0); i < words; i++ {
		word := uint64(r.Uint32())
		remaining := k - 32*i
		if remaining < 32 {
			word >>= 32 - remaining
		}
		result |= word << (32 * i)
	}
	return result
}

// RandBelow mirrors random.Random._randbelow_with_getrandbits: draw
// n.bit_length() bits and retry until the value is below n. It panics for
// n == 0, which would be randrange(0) in Python (a ValueError).
func (r *Random) RandBelow(n uint64) uint64 {
	if n == 0 {
		panic("pyrandom: RandBelow(0)")
	}
	k := uint(bits.Len64(n))
	v := r.GetRandBits(k)
	for v >= n {
		v = r.GetRandBits(k)
	}
	return v
}

// RandRange mirrors random.randrange(n) for n > 0.
func (r *Random) RandRange(n int) int {
	if n <= 0 {
		panic("pyrandom: RandRange requires n > 0")
	}
	return int(r.RandBelow(uint64(n)))
}

// Float64 mirrors random.Random.random(): a float in [0, 1) built from two
// 32-bit draws (27 and 26 bits of mantissa).
func (r *Random) Float64() float64 {
	a := uint64(r.Uint32() >> 5)
	b := uint64(r.Uint32() >> 6)
	return float64(a*67108864+b) * (1.0 / 9007199254740992.0)
}
