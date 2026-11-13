package vector

import (
	"hash/fnv"
	"slices"

	"github.com/go-tenon/tenon/search"
)

// 混合索引的 point 有两个命名向量：模型计算的稠密向量，以及由分词结果得到的稀疏向量。
const (
	vectorDense  = "dense"
	vectorSparse = "sparse"
)

// BM25 的参数。IDF 由 Qdrant 按 collection 统计（稀疏向量的 modifier 为 idf），这里只计算词频部分。
// 建索引时不知道文档的平均长度，取一个与常见分段长度相当的常数，词数按切分出的词计。
const (
	bm25K1     = 1.2
	bm25B      = 0.75
	bm25AvgLen = 256
)

// sparse 是一个稀疏向量，indices 从小到大排列。
type sparse struct {
	indices []uint32
	values  []float32
}

// termID 返回词在稀疏向量中的维度：词的 32 位 FNV-1a 哈希。不同的词偶尔得到同一个维度，只会让它们互相多命中一点。
func termID(term string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(term))
	return h.Sum32()
}

// documentSparse 返回文本建索引用的稀疏向量，值为 BM25 的词频部分。
func documentSparse(tok search.Tokenizer, text string) sparse {
	tokens := tok.Index(text)
	tf := map[uint32]float64{}
	for _, t := range tokens {
		tf[termID(t.Text)]++
	}
	norm := bm25K1 * (1 - bm25B + bm25B*float64(len(tokens))/bm25AvgLen)
	return build(tf, func(n float64) float32 { return float32(n * (bm25K1 + 1) / (n + norm)) })
}

// querySparse 返回查询文本的稀疏向量，每个词的权重为 1，与文档向量的点积再乘上 IDF 就是 BM25 的分数。
func querySparse(tok search.Tokenizer, text string) sparse {
	tf := map[uint32]float64{}
	for _, t := range tok.Query(text) {
		tf[termID(t.Text)] = 1
	}
	return build(tf, func(float64) float32 { return 1 })
}

func build(tf map[uint32]float64, value func(float64) float32) sparse {
	s := sparse{indices: make([]uint32, 0, len(tf))}
	for id := range tf {
		s.indices = append(s.indices, id)
	}
	slices.Sort(s.indices)
	s.values = make([]float32, len(s.indices))
	for i, id := range s.indices {
		s.values[i] = value(tf[id])
	}
	return s
}
