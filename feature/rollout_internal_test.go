package feature

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestInRolloutDistribution 检查放量哈希的统计性质。它是纯函数，样本可以很大；
// 经过数据库的放量在 feature_test.go 中用少量主体检查。
func TestInRolloutDistribution(t *testing.T) {
	const n = 20000
	cohort := func(name string, percent int) map[string]bool {
		on := map[string]bool{}
		for i := range n {
			if id := fmt.Sprint("u", i); inRollout(name, id, percent) {
				on[id] = true
			}
		}
		return on
	}
	assert.Empty(t, cohort("search", 0))
	assert.Len(t, cohort("search", 100), n)
	ten, half := cohort("search", 10), cohort("search", 50)
	assert.InDelta(t, n/10, len(ten), n/100, "about 10%")
	assert.InDelta(t, n/2, len(half), n/100, "about 50%")
	for id := range ten {
		assert.True(t, half[id], "raising the percentage keeps %s enabled", id)
	}

	// 不同开关选中的人不同：两个 50% 的开关，约一半的人结果相同。
	other, same := cohort("other", 50), 0
	for i := range n {
		id := fmt.Sprint("u", i)
		if half[id] == other[id] {
			same++
		}
	}
	assert.InDelta(t, n/2, same, n/50, "flags pick independent cohorts")

	// 没有放量单位（未登录的访客、没有租户）时，100% 以下都不选中。
	assert.False(t, inRollout("search", "", 99))
	assert.True(t, inRollout("search", "", 100))
}
