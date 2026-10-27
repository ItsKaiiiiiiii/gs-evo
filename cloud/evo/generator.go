// Package evo 是 Evolution Engine 的核心（§7.5）：
// ③生成（LLM 改写，rationale 强制留存）→ ④验证（BenchmarkSet 灰度评估）→ ⑤放行（人工/达标策略）。
package evo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// GeneratedCandidate LLM 生成产物。
type GeneratedCandidate struct {
	NewSkillMD string // 新 SKILL.md 全文
	Rationale  string // 生成理由（强制留存，§7.5 ③）
}

// Generator 生成器接口：生产为 LLM，测试为确定性实现。
type Generator interface {
	Generate(ctx context.Context, skillName, parentSkillMD, instruction string) (*GeneratedCandidate, error)
}

// ---- Anthropic 实现（设计 §4.5：模型端点可配；此处以最小 HTTP 客户端等价实现）----

type AnthropicGenerator struct {
	APIKey  string
	Model   string
	BaseURL string
}

func NewAnthropicFromEnv() *AnthropicGenerator {
	return &AnthropicGenerator{
		APIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		Model:   envOr("GSEVO_LLM_MODEL", "claude-sonnet-4-5"),
		BaseURL: envOr("GSEVO_LLM_BASE_URL", "https://api.anthropic.com"),
	}
}

func (a *AnthropicGenerator) Generate(ctx context.Context, skillName, parentSkillMD, instruction string) (*GeneratedCandidate, error) {
	if a.APIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
	}
	prompt := fmt.Sprintf(`你在优化一个 coding agent 的 skill。skill 名：%s。

当前 SKILL.md：
-----
%s
-----
改进要求：%s

请输出严格 JSON（不要 markdown 围栏）：
{"rationale":"一句话说明改了什么、为什么","new_skill_md":"完整的新 SKILL.md 全文（保留 YAML front-matter，name 不变，version 由系统统一改写）"}`,
		skillName, parentSkillMD, instruction)

	bodyReq, _ := json.Marshal(map[string]any{
		"model":      a.Model,
		"max_tokens": 8192,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(a.BaseURL, "/")+"/v1/messages",
		bytes.NewReader(bodyReq))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm HTTP %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	var text string
	for _, c := range out.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	return parseGeneratorJSON(text)
}

func parseGeneratorJSON(text string) (*GeneratedCandidate, error) {
	clean := strings.TrimSpace(text)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)
	var out GeneratedCandidate
	if err := json.Unmarshal([]byte(clean), &out); err != nil {
		return nil, fmt.Errorf("llm output not valid JSON: %w (head: %.200s)", err, clean)
	}
	if strings.TrimSpace(out.NewSkillMD) == "" {
		return nil, fmt.Errorf("llm returned empty new_skill_md")
	}
	return &out, nil
}

// ---- 确定性实现（测试/离线演示）：按 instruction 生成可预期的改写 ----

type FakeGenerator struct{}

var fakeRationale = "fake-generator: 按 instruction 追加改进说明小节（确定性实现，供 e2e）"

func (FakeGenerator) Generate(_ context.Context, skillName, parentSkillMD, instruction string) (*GeneratedCandidate, error) {
	// 在正文末尾追加改进小节；保证每次生成内容随 instruction 变化（e2e 断言版本内容不同）
	addition := fmt.Sprintf("\n## Improvement (%s)\n\n- instruction: %s\n- generated-by: fake-generator\n- at: %s\n",
		skillName, instruction, time.Now().UTC().Format(time.RFC3339Nano))
	return &GeneratedCandidate{NewSkillMD: parentSkillMD + addition, Rationale: fakeRationale + "; instruction=" + instruction}, nil
}

// ---- front-matter 工具（version 由系统统一改写，§7.5 ③）----

// SetFrontMatterVersion 改写 SKILL.md front-matter 中的 version 字段。
func SetFrontMatterVersion(skillMD, version string) string {
	re := regexp.MustCompile(`(?m)^(version:\s*).*$`)
	if re.MatchString(skillMD) {
		return re.ReplaceAllString(skillMD, "${1}"+version)
	}
	// 无 version 字段：插入到 front-matter 开头
	if strings.HasPrefix(strings.TrimSpace(skillMD), "---") {
		return strings.Replace(skillMD, "---\n", "---\nversion: "+version+"\n", 1)
	}
	return skillMD
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
