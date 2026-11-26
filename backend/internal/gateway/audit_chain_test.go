package gateway

import (
	"testing"
)

func TestChainHashDeterministic(t *testing.T) {
	h1 := chainHash(1, "usage", []byte(`{"a":1}`), genesisHash)
	h2 := chainHash(1, "usage", []byte(`{"a":1}`), genesisHash)
	if h1 != h2 {
		t.Fatal("相同输入应产生相同 hash")
	}
	if len(h1) != 64 {
		t.Fatalf("SHA-256 应为64字符: %s", h1)
	}
}

func TestChainHashChangesOnTamper(t *testing.T) {
	base := chainHash(1, "usage", []byte(`{"input":100}`), genesisHash)
	tampered := chainHash(1, "usage", []byte(`{"input":999}`), genesisHash)
	if base == tampered {
		t.Fatal("payload 改变必须改变 hash（篡改可检出）")
	}
	changedPrev := chainHash(1, "usage", []byte(`{"input":100}`), "abc")
	if base == changedPrev {
		t.Fatal("prev_hash 改变必须改变 hash")
	}
}

func TestChainLinkage(t *testing.T) {
	h1 := chainHash(1, "usage", []byte(`{}`), genesisHash)
	h2 := chainHash(2, "usage", []byte(`{}`), h1)
	h2Tampered := chainHash(2, "usage", []byte(`{}`), genesisHash)
	if h2 == h2Tampered {
		t.Fatal("第二条记录必须依赖第一条 hash（断链可检出）")
	}
}
