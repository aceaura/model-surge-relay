package relayv1

import (
	"encoding/json"
	"testing"
)

// 上报里用量的线上键名。
//
// 跨进程契约：数据面与本服务永不互相 import，改 tag 在各自仓内都自洽，
// 线上效果是那一位恒为零。两侧各钉一份字面量（承 outcome 字面值那条判据）。
func TestUsageWireKeys(t *testing.T) {
	raw, err := json.Marshal(Usage{
		InputTokens:      1,
		OutputTokens:     2,
		CacheReadTokens:  3,
		CacheWriteTokens: 4,
		ReasoningTokens:  5,
	})
	if err != nil {
		t.Fatalf("编码：%v", err)
	}
	var got map[string]int64
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解码：%v", err)
	}
	want := map[string]int64{
		"input_tokens":       1,
		"output_tokens":      2,
		"cache_read_tokens":  3,
		"cache_write_tokens": 4,
		"reasoning_tokens":   5,
	}
	if len(got) != len(want) {
		t.Errorf("线上键 = %v，want 恰好 %d 个：%v", got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("键 %q = %d，want %d", k, got[k], v)
		}
	}
}

// 旧版数据面的上报（只有三位）必须仍能解。
//
// 两侧独立发版：新契约上线时在飞的数据面还是旧版，拒收会让那段时间的上报全丢，
// 而丢掉的上报意味着失败计数不清零、目标被错误地推向冷却。
func TestOlderReportsWithoutTheTwoNewKeysStillDecode(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(
		`{"input_tokens":7,"output_tokens":8,"cache_read_tokens":9}`), &u); err != nil {
		t.Fatalf("旧版上报被拒收了：%v", err)
	}
	if u.InputTokens != 7 || u.OutputTokens != 8 || u.CacheReadTokens != 9 {
		t.Errorf("前三位解错了：%+v", u)
	}
	if u.CacheWriteTokens != 0 || u.ReasoningTokens != 0 {
		t.Errorf("旧版没给的两位不该有值：%+v", u)
	}
}
