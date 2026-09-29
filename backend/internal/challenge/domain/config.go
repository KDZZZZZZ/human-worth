package domain

import (
	"errors"
	"math"
	"time"
)

type Config struct {
	Models, Harnesses, Skills     map[string]bool
	Lease, RunTimeout             time.Duration
	ModelCallLimit, ToolCallLimit int32
	InputPolicy                   string
}

// Engine 集中维护角色输入、排名校验和迭代规则。
// 应用层核实外部输入，并在持有推进权的事务内保存结果。
type Engine struct{ Config Config }

func NewEngine(cfg Config) (Engine, error) {
	if len(cfg.Models) == 0 || len(cfg.Harnesses) == 0 {
		return Engine{}, errors.New("challenge allowlists required")
	}
	if cfg.Lease == 0 {
		cfg.Lease = time.Minute
	}
	if cfg.RunTimeout == 0 {
		cfg.RunTimeout = 2 * time.Hour
	}
	if cfg.ModelCallLimit == 0 {
		cfg.ModelCallLimit = 8
	}
	if cfg.ToolCallLimit == 0 {
		cfg.ToolCallLimit = 16
	}
	if cfg.InputPolicy == "" {
		cfg.InputPolicy = InputPolicyVersion
	}
	if cfg.Lease <= 0 || cfg.RunTimeout <= cfg.Lease || cfg.ModelCallLimit < 1 || cfg.ToolCallLimit < 1 {
		return Engine{}, errors.New("invalid challenge limits")
	}
	return Engine{Config: cfg}, nil
}

// ValidateConfiguration 冻结初始包和三角色能力，客户端只能选择运维已启用的配置。
func (e Engine) ValidateConfiguration(c *RunConfiguration) error {
	if c == nil || c.InitialTaskPackage == nil || c.Packer == nil || c.Ranker == nil || c.Executor == nil || c.Budget == nil {
		return Invalid("configuration_required")
	}
	p := c.InitialTaskPackage
	if p.TaskRevision < 1 || !Bounded(p.Description, 16384) || !Bounded(p.WorkRequirements, 8192) || len(p.TaskAttachmentIds) > 32 {
		return Invalid("invalid_initial_package")
	}
	seen := map[string]bool{}
	for _, id := range p.TaskAttachmentIds {
		if !ValidID(id) || seen[id] {
			return Invalid("invalid_task_attachment")
		}
		seen[id] = true
	}
	if math.IsNaN(c.RankerFitThreshold) || c.RankerFitThreshold <= 0 || c.RankerFitThreshold >= 1 || c.RankerMinComparablePairs < 1 || c.RankerRoundLimit < 1 || c.RankerRoundLimit > 100 || c.RoundLimit < 1 || c.RoundLimit > 100 {
		return Invalid("invalid_iteration_limits")
	}
	if c.Budget.Unit != "model_calls" || c.Budget.Amount < 1 || c.Budget.Amount > 100000 {
		return Invalid("unsupported_budget")
	}
	for _, m := range []*ModelConfiguration{c.Packer, c.Ranker, {Model: c.Executor.Model, Prompt: c.Executor.Prompt, Parameters: c.Executor.Parameters}} {
		if !e.Config.Models[m.Model] || len(m.Prompt) > 16384 {
			return Invalid("model_not_allowed")
		}
		for k, v := range m.Parameters {
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
				return Invalid("invalid_model_parameters")
			}
			switch k {
			case "temperature":
				if n < 0 || n > 2 {
					return Invalid("invalid_model_parameters")
				}
			case "top_p":
				if n <= 0 || n > 1 {
					return Invalid("invalid_model_parameters")
				}
			default:
				return Invalid("invalid_model_parameters")
			}
		}
	}
	if !e.Config.Harnesses[c.Executor.Harness] {
		return Invalid("harness_not_allowed")
	}
	for _, v := range c.Executor.Skills {
		if !e.Config.Skills[v] {
			return Invalid("skill_not_allowed")
		}
	}
	return nil
}
