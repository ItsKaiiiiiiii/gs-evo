package worker

import (
	"strings"
	"testing"
)

func TestPumpGSAgentLines(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"hello","agent":"gsagent","version":"1"}`,
		`{"type":"message","text":"chunk 1"}`,
		`{"type":"tool_call","name":"demo.echo","input":{"path":"/tmp"}}`,
		`{"type":"result","text":"done","exit":0}`,
	}, "\n")
	var kinds []string
	pumpAgentLines("gsagent", strings.NewReader(input), func(ev agentEvent) {
		kinds = append(kinds, ev.Kind)
	})
	want := []string{"init", "message", "tool_call", "result"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds=%v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds[%d]=%s, want %s", i, kinds[i], want[i])
		}
	}
}
