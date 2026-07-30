package model

import (
	"fmt"
	"strings"
	"time"
)

const (
	ProvenanceSessionJSONL = "session_jsonl"
	ProvenanceOTel         = "otel"
	ProvenanceState        = "state_fallback"

	ConfidenceExact         = "exact"
	ConfidenceAggregateOnly = "aggregate_only"
	ConfidenceGapFallback   = "gap_fallback"
)

// TokenUsage keeps overlapping token categories separate. CachedInput is a
// subset of Input and ReasoningOutput is a subset of Output.
type TokenUsage struct {
	Input           int64 `json:"input"`
	CachedInput     int64 `json:"cached_input"`
	CacheWriteInput int64 `json:"cache_write_input"`
	Output          int64 `json:"output"`
	ReasoningOutput int64 `json:"reasoning_output"`
	Total           int64 `json:"total"`
}

func (u TokenUsage) Add(v TokenUsage) TokenUsage {
	return TokenUsage{
		Input:           u.Input + v.Input,
		CachedInput:     u.CachedInput + v.CachedInput,
		CacheWriteInput: u.CacheWriteInput + v.CacheWriteInput,
		Output:          u.Output + v.Output,
		ReasoningOutput: u.ReasoningOutput + v.ReasoningOutput,
		Total:           u.Total + v.Total,
	}
}

func (u TokenUsage) Sub(v TokenUsage) TokenUsage {
	return TokenUsage{
		Input:           u.Input - v.Input,
		CachedInput:     u.CachedInput - v.CachedInput,
		CacheWriteInput: u.CacheWriteInput - v.CacheWriteInput,
		Output:          u.Output - v.Output,
		ReasoningOutput: u.ReasoningOutput - v.ReasoningOutput,
		Total:           u.Total - v.Total,
	}
}

func (u TokenUsage) Equal(v TokenUsage) bool {
	return u == v
}

func (u TokenUsage) MonotonicFrom(v TokenUsage) bool {
	return u.Input >= v.Input &&
		u.CachedInput >= v.CachedInput &&
		u.CacheWriteInput >= v.CacheWriteInput &&
		u.Output >= v.Output &&
		u.ReasoningOutput >= v.ReasoningOutput &&
		u.Total >= v.Total
}

func (u TokenUsage) NonNegative() bool {
	return u.Input >= 0 &&
		u.CachedInput >= 0 &&
		u.CacheWriteInput >= 0 &&
		u.Output >= 0 &&
		u.ReasoningOutput >= 0 &&
		u.Total >= 0
}

func (u TokenUsage) IsZero() bool {
	return u == TokenUsage{}
}

// Compatible fills totals missing from older records. It never adds subset
// categories on top of their parent categories.
func (u TokenUsage) Compatible() TokenUsage {
	if u.Total == 0 && (u.Input != 0 || u.Output != 0) {
		u.Total = u.Input + u.Output
	}
	return u
}

func (u TokenUsage) String() string {
	return fmt.Sprintf("total=%d input=%d cached=%d cache_write=%d output=%d reasoning=%d",
		u.Total, u.Input, u.CachedInput, u.CacheWriteInput, u.Output, u.ReasoningOutput)
}

type UsageEvent struct {
	ID          string     `json:"id"`
	Timestamp   time.Time  `json:"timestamp,omitempty"`
	ObservedAt  time.Time  `json:"observed_at"`
	MachineID   string     `json:"machine_id"`
	SessionID   string     `json:"session_id,omitempty"`
	TurnID      string     `json:"turn_id,omitempty"`
	Model       string     `json:"model,omitempty"`
	Source      string     `json:"source,omitempty"`
	AgentType   string     `json:"agent_type,omitempty"`
	ProjectPath string     `json:"project_path,omitempty"`
	ThreadTitle string     `json:"thread_title,omitempty"`
	Usage       TokenUsage `json:"usage"`
	Provenance  string     `json:"provenance"`
	Confidence  string     `json:"confidence"`
	CodexHome   string     `json:"codex_home,omitempty"`
}

type SessionInfo struct {
	SessionID    string    `json:"session_id"`
	RolloutPath  string    `json:"rollout_path,omitempty"`
	CodexHome    string    `json:"codex_home,omitempty"`
	Title        string    `json:"title,omitempty"`
	ProjectPath  string    `json:"project_path,omitempty"`
	Model        string    `json:"model,omitempty"`
	Source       string    `json:"source,omitempty"`
	ThreadSource string    `json:"thread_source,omitempty"`
	AgentType    string    `json:"agent_type,omitempty"`
	CLIValue     string    `json:"cli_version,omitempty"`
	TokensUsed   int64     `json:"tokens_used,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
	Archived     bool      `json:"archived"`
}

type Machine struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

type Warning struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Kind      string    `json:"kind"`
	Path      string    `json:"path,omitempty"`
	Detail    string    `json:"detail"`
}

type Filter struct {
	Since      time.Time
	Until      time.Time
	Model      string
	Source     string
	AgentType  string
	Project    string
	SessionID  string
	Confidence string
}

type Summary struct {
	Usage              TokenUsage `json:"usage"`
	Unattributed       TokenUsage `json:"unattributed"`
	GrandTotal         int64      `json:"grand_total"`
	EventCount         int64      `json:"event_count"`
	SessionCount       int64      `json:"session_count"`
	FirstEvent         time.Time  `json:"first_event,omitempty"`
	LastEvent          time.Time  `json:"last_event,omitempty"`
	CoverageIncomplete bool       `json:"coverage_incomplete"`
}

type Point struct {
	Time  time.Time  `json:"time"`
	Usage TokenUsage `json:"usage"`
}

type BreakdownItem struct {
	Key      string     `json:"key"`
	Usage    TokenUsage `json:"usage"`
	Events   int64      `json:"events"`
	Sessions int64      `json:"sessions"`
}

func ClassifyAgent(parts ...string) string {
	s := strings.ToLower(strings.Join(parts, " "))
	switch {
	case strings.Contains(s, "guardian"):
		return "guardian"
	case strings.Contains(s, "memory"):
		return "memory"
	case strings.Contains(s, "subagent"), strings.Contains(s, "sub_agent"), strings.Contains(s, "agent"):
		return "subagent"
	default:
		return "main"
	}
}
