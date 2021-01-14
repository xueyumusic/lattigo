package ckks

import (
	"github.com/ldsec/lattigo/v2/ring"
	"math"
)

type MMPt struct {
	dimension uint64
	mPermuteA *PtDiagMatrix
	mPermuteB *PtDiagMatrix
	mRotRows  []*PtDiagMatrix
	mRotCols  []*PtDiagMatrix
}

func GenPlaintextMatrices(params *Parameters, level uint64, dimension uint64, encoder Encoder) (mmpt *MMPt) {

	mmpt = new(MMPt)

	mmpt.dimension = dimension

	var scale float64
	scale = float64(params.Qi()[level]) * math.Sqrt(float64(params.Qi()[level-2])/params.Scale())

	mmpt.mPermuteA, _ = GenPermuteAMatrix(level, scale, 16.0, dimension, params.LogSlots(), encoder)
	mmpt.mPermuteB, _ = GenPermuteBMatrix(level, scale, 16.0, dimension, params.LogSlots(), encoder)

	mmpt.mRotCols = make([]*PtDiagMatrix, dimension-1)
	mmpt.mRotRows = make([]*PtDiagMatrix, dimension-1)

	scale = float64(params.Qi()[level-1])

	for i := uint64(0); i < dimension-1; i++ {
		mmpt.mRotCols[i], _ = GenSubVectorRotationMatrix(level-1, scale, dimension, i+1, params.LogSlots(), encoder)
		mmpt.mRotRows[i], _ = GenSubVectorRotationMatrix(level-1, scale, dimension*dimension, (i+1)*dimension, params.LogSlots(), encoder)

	}
	return
}

func GenRotationKeys(mmpt *MMPt, kgen KeyGenerator, sk *SecretKey, rotKeys *RotationKeys) {

	kgen.GenRotKeysForDiagMatrix(mmpt.mPermuteA, sk, rotKeys)
	kgen.GenRotKeysForDiagMatrix(mmpt.mPermuteB, sk, rotKeys)

	for i := range mmpt.mRotCols {
		kgen.GenRotKeysForDiagMatrix(mmpt.mRotCols[i], sk, rotKeys)
		kgen.GenRotKeysForDiagMatrix(mmpt.mRotRows[i], sk, rotKeys)
	}
}

func (eval *evaluator) MulMatrixAB(A, B *Ciphertext, mmpt *MMPt, rlk *EvaluationKey, rotKeys *RotationKeys) (ciphertextAB *Ciphertext) {

	ciphertextA := eval.LinearTransform(A, mmpt.mPermuteA, rotKeys)[0]
	if err := eval.Rescale(ciphertextA, eval.params.Scale(), ciphertextA); err != nil {
		panic(err)
	}

	ciphertextB := eval.LinearTransform(B, mmpt.mPermuteB, rotKeys)[0]
	if err := eval.Rescale(ciphertextB, eval.params.Scale(), ciphertextB); err != nil {
		panic(err)
	}

	ciphertextAB = eval.MulRelinNew(ciphertextA, ciphertextB, nil)

	alpha := eval.params.Alpha()
	beta := uint64(math.Ceil(float64(ciphertextA.Level()+1) / float64(alpha)))

	c2QiQDecompB := make([]*ring.Poly, beta)
	c2QiPDecompB := make([]*ring.Poly, beta)

	for i := uint64(0); i < beta; i++ {
		c2QiQDecompB[i] = eval.ringQ.NewPolyLvl(ciphertextA.Level())
		c2QiPDecompB[i] = eval.ringP.NewPoly()
	}

	eval.DecompInternal(ciphertextA.Level(), ciphertextA.value[1], eval.c2QiQDecomp, eval.c2QiPDecomp)
	eval.DecompInternal(ciphertextB.Level(), ciphertextB.value[1], c2QiQDecompB, c2QiPDecompB)

	tmpC := NewCiphertext(eval.params, 2, ciphertextA.Level()-1, ciphertextA.Scale())

	tmpA := NewCiphertext(eval.params, 1, ciphertextA.Level(), ciphertextA.Scale())
	tmpB := NewCiphertext(eval.params, 1, ciphertextB.Level(), ciphertextB.Scale())

	tmpARescale := NewCiphertext(eval.params, 1, ciphertextA.Level()-1, ciphertextA.Scale())
	tmpBRescale := NewCiphertext(eval.params, 1, ciphertextB.Level()-1, ciphertextB.Scale())

	for i := uint64(0); i < mmpt.dimension-1; i++ {

		eval.multiplyByDiabMatrix(ciphertextA, tmpA, mmpt.mRotCols[i], rotKeys, eval.c2QiQDecomp, eval.c2QiPDecomp)
		eval.multiplyByDiabMatrix(ciphertextB, tmpB, mmpt.mRotRows[i], rotKeys, c2QiQDecompB, c2QiPDecompB)

		if err := eval.Rescale(tmpA, eval.params.Scale(), tmpARescale); err != nil {
			panic(err)
		}

		if err := eval.Rescale(tmpB, eval.params.Scale(), tmpBRescale); err != nil {
			panic(err)
		}

		eval.MulRelin(tmpARescale, tmpBRescale, nil, tmpC)

		eval.Add(ciphertextAB, tmpC, ciphertextAB)
	}

	eval.Relinearize(ciphertextAB, rlk, ciphertextAB)
	eval.Rescale(ciphertextAB, eval.params.Scale(), ciphertextAB)

	return
}

// GenPermuteAMatrix rotates each row of the matrix by k position, where k is the row index.
func GenPermuteAMatrix(level uint64, scale, maxM1N2Ratio float64, dimension, logSlots uint64, encoder Encoder) (*PtDiagMatrix, map[uint64][]complex128) {

	slots := uint64(1 << logSlots)

	diagMatrix := make(map[uint64][]complex128)

	d2 := int(dimension * dimension)

	for i := -int(dimension) + 1; i < int(dimension); i++ {

		m := make([]complex128, slots)

		for k := 0; k < d2; k++ {

			if i < 0 {
				for j := i; j < int(dimension); j++ {
					x := (d2 + k - (int(dimension)+i)*int(dimension)) % d2
					if x < int(dimension) && x >= -i {
						m[k] = 1
					}
				}
			} else {

				for j := i; j < int(dimension); j++ {
					if (d2+k-int(dimension)*i)%d2 < int(dimension)-i {
						m[k] = 1
					}
				}
			}
		}

		populateVector(m, d2, logSlots)

		diagMatrix[uint64((i+int(slots)))%slots] = m
	}

	return encoder.EncodeDiagMatrixAtLvl(level, diagMatrix, scale, maxM1N2Ratio, logSlots), diagMatrix

}

// GenPermuteAMatrix rotates each column of the matrix by k position, where k is the column index.
func GenPermuteBMatrix(level uint64, scale, maxM1N2Ratio float64, dimension, logSlots uint64, encoder Encoder) (*PtDiagMatrix, map[uint64][]complex128) {

	slots := uint64(1 << logSlots)

	diagMatrix := make(map[uint64][]complex128)

	d2 := int(dimension * dimension)

	if uint64(d2) < slots {

		for i := -int((dimension - 1) * dimension); i < d2; i = i + int(dimension) {

			m := make([]complex128, 1<<logSlots)

			if i >= 0 {
				for j := 0; j < d2-i; j = j + int(dimension) {
					m[i/int(dimension)+j] = 1
				}
			} else {
				for j := 0; j < d2+i; j = j + int(dimension) {
					m[-i+int(dimension)+(i/int(dimension))+j] = 1
				}
			}

			populateVector(m, d2, logSlots)

			diagMatrix[uint64((i+int(slots)))%slots] = m

		}
	} else {
		for i := 0; i < int(dimension); i++ {

			m := make([]complex128, 1<<logSlots)

			for j := 0; j < d2; j = j + int(dimension) {
				m[j+i] = 1
			}

			populateVector(m, d2, logSlots)

			diagMatrix[uint64(i)*dimension] = m
		}
	}

	return encoder.EncodeDiagMatrixAtLvl(level, diagMatrix, scale, maxM1N2Ratio, logSlots), diagMatrix
}

// GenSubVectorRotationMatrix allows to generate a permutation matrix that roates subvectors independently.
// Given a vector of size N=2^"logSlots", partitionned into N/"vectorSize" subvectors each of size "vectorSize",
// rotates each subvector by "k" positions to the left.
//
// Example :
// Given v = [a_(0), a_(1), a_(2), ..., a_(N-3), a_(N-2), a_(N-1)],
// Then M x v = [rotate(a_(0), a_(1), ..., a_(vectorsize-1), k), ... , rotate(a_(N-vectorsize-1), a_(N-vectorsize), ..., a_(N-1), k)]
//
// If vectorSize does not divide N, then the last N%vectorSize slots are zero.
// If N = vectorSize, then no mask is generated and the evaluation is instead a single rotation.
//
// This is done by generating the two masks :
//       	 |     vectorsize     |, ..., |     vectorsize     |
// mask_0 = [{1, ..., 1, 0, ..., 0}, ..., {1, ..., 1, 0, ..., 0}]
// mask_1 = [{0, ..., 0, 1, ..., 1}, ..., {0, ..., 0, 1, ..., 1}]
//            0 ----- k                    0 ----- k
func GenSubVectorRotationMatrix(level uint64, scale float64, vectorSize, k, logSlots uint64, encoder Encoder) (*PtDiagMatrix, map[uint64][]complex128) {

	k %= vectorSize

	diagMatrix := make(map[uint64][]complex128)

	slots := uint64(1 << logSlots)

	matrix := new(PtDiagMatrix)
	matrix.Vec = make(map[uint64][2]*ring.Poly)

	if vectorSize < slots {
		m0 := make([]complex128, slots)
		m1 := make([]complex128, slots)

		for i := uint64(0); i < slots/vectorSize; i++ {

			index := i * vectorSize

			for j := uint64(0); j < k; j++ {
				m0[j+index] = 1
			}

			for j := k; j < vectorSize; j++ {
				m1[j+index] = 1
			}
		}

		/*
			fmt.Printf("%4d", (slots) - (vectorSize - k))
			for i := range m0[:vectorSize]{
				fmt.Printf("%2.f ", real(m0[i]))
			}
			fmt.Println()

			fmt.Printf("%4d", k)
			for i := range m1[:vectorSize]{
				fmt.Printf("%2.f ", real(m1[i]))
			}
			fmt.Println()
		*/

		diagMatrix[slots-vectorSize+k] = m0
		diagMatrix[k] = m1

		// Encoding
		matrix.LogSlots = logSlots
		matrix.Level = level
		matrix.Scale = scale
		matrix.naive = true

		// Encode m0
		matrix.Vec[slots-vectorSize+k] = encoder.(*encoderComplex128).encodeDiagonal(logSlots, level, scale, m0, slots-vectorSize+k)
		// Encode m1
		matrix.Vec[k] = encoder.(*encoderComplex128).encodeDiagonal(logSlots, level, scale, m1, k)

	} else {

		matrix.rotOnly = true

		// If N = vectorSize, the we a single rotation without masking is sufficient
		matrix.Vec[k] = [2]*ring.Poly{nil, nil}

	}

	return matrix, diagMatrix
}

func GenTransposeDiagMatrix(level uint64, scale, maxM1N2Ratio float64, dimension, logSlots uint64, encoder Encoder) (*PtDiagMatrix, map[uint64][]complex128) {

	slots := uint64(1 << logSlots)

	diagMatrix := make(map[uint64][]complex128)

	d2 := int(dimension * dimension)

	for i := -int(dimension) + 1; i < int(dimension); i++ {

		m := make([]complex128, slots)

		if i >= 0 {
			for j := 0; j < d2-i*int(dimension); j = j + int(dimension) + 1 {
				m[i+j] = 1
			}
		} else {
			for j := -i * int(dimension); j < d2; j = j + int(dimension) + 1 {
				m[j] = 1
			}
		}

		populateVector(m, d2, logSlots)

		diagMatrix[uint64(i*int(dimension-1)+int(slots))%slots] = m
	}

	return encoder.EncodeDiagMatrixAtLvl(level, diagMatrix, scale, maxM1N2Ratio, logSlots), diagMatrix
}

func populateVector(m []complex128, d2 int, logSlots uint64) {

	slots := uint64(1 << logSlots)

	for k := d2; k < int(slots); k = k + d2 {

		if k+d2 > int(slots) {
			break
		}

		for j := 0; j < d2; j++ {
			m[k+j] = m[j]
		}
	}
}
