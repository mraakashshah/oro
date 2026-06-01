package embeddings

import "crypto/sha256"

func deterministicVector(text string, dim int) []float32 {
	out := make([]float32, dim)
	seed := []byte(text)
	for i := range dim {
		sum := sha256.Sum256(append(seed, byte(i), byte(i>>8)))
		v := uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
		out[i] = (float32(v)/float32(^uint32(0)))*2 - 1
	}
	return out
}
