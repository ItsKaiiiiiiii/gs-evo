package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTokenShape(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		tok    string
	}{
		{TokenPrefixDevice, NewDeviceToken()},
		{TokenPrefixEnroll, NewEnrollToken()},
		{TokenPrefixAdmin, NewAdminToken()},
	} {
		if !strings.HasPrefix(tc.tok, tc.prefix) {
			t.Fatalf("%s missing prefix %s", tc.tok, tc.prefix)
		}
		if len(tc.tok) != len(tc.prefix)+40 { // 20 字节 → 40 hex
			t.Fatalf("token length %d, want %d", len(tc.tok), len(tc.prefix)+40)
		}
	}
	h := HashToken("abc")
	if len(h) != 64 || h == "abc" {
		t.Fatalf("hash: %s", h)
	}
	if HashToken("abc") != h {
		t.Fatal("hash not deterministic")
	}
}

func TestIDUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID("c-")
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}

func TestDeviceIDFormat(t *testing.T) {
	id := NewDeviceID()
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("device id format: %s", id)
	}
}

func TestCmdJSONRoundTrip(t *testing.T) {
	c := Cmd{Type: TypeCmd, CmdID: "c-1", CmdSeq: 7, CmdType: CmdSkillsInstall,
		Payload: map[string]any{"name": "x"}}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Cmd
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.CmdType != CmdSkillsInstall || back.CmdSeq != 7 || back.Payload["name"] != "x" {
		t.Fatalf("roundtrip: %+v", back)
	}
}
