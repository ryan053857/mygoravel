package ai

import (
	"context"

	"github.com/cockroachdb/errors"
)

// QuestionKind 是决策问题的类型。
type QuestionKind string

// 决策问题的类型。
const (
	// YesNoQuestion 判断一个条件是否成立，回答是成立的概率。
	YesNoQuestion QuestionKind = "yes_no"
	// ChoiceQuestion 从无序的选项中选出一个，例如部门。
	ChoiceQuestion QuestionKind = "choice"
	// ScoreQuestion 按从低到高的级别打分，例如严重程度。
	ScoreQuestion QuestionKind = "score"
)

// ChoiceOption 是选择题的一个选项。
type ChoiceOption struct {
	// Value 是选项的值，同一个问题中不能重复。
	Value string
	// Description 说明什么情况下选它。
	Description string
}

// Level 是打分题的一个级别。
type Level struct {
	Label       string
	Description string
}

// Levels 用标签依次创建级别，从低到高。
func Levels(labels ...string) []Level {
	out := make([]Level, len(labels))
	for i, l := range labels {
		out[i] = Level{Label: l}
	}
	return out
}

// Question 是决策中的一个问题，用 YesNo、Choice 或 Score 创建。
type Question struct {
	// Name 标识问题，答案按它取用，同一次决策中不能重复。
	Name string
	Kind QuestionKind
	// Instructions 是问题本身。
	Instructions string
	// IfTrue 和 IfFalse 是是非题判断成立与不成立的标准，可以为空。
	IfTrue, IfFalse string
	// Options 是选择题的选项，至少 2 个。
	Options []ChoiceOption
	// Levels 是打分题的级别，从低到高，至少 2 个。
	Levels []Level
}

// YesNo 创建是非题。
func YesNo(name, instructions string) Question {
	return Question{Name: name, Kind: YesNoQuestion, Instructions: instructions}
}

// Criteria 设置是非题判断成立与不成立的标准。
func (q Question) Criteria(ifTrue, ifFalse string) Question {
	q.IfTrue, q.IfFalse = ifTrue, ifFalse
	return q
}

// Choice 创建选择题。
func Choice(name, instructions string, options ...ChoiceOption) Question {
	return Question{Name: name, Kind: ChoiceQuestion, Instructions: instructions, Options: options}
}

// Score 创建打分题，levels 从低到高。
func Score(name, instructions string, levels ...Level) Question {
	return Question{Name: name, Kind: ScoreQuestion, Instructions: instructions, Levels: levels}
}

// DecisionRequest 描述一次决策：一份材料和关于它的若干问题。
type DecisionRequest struct {
	// Input 是材料。字符串按文本发送，其他值按 JSON 发送，例如结构体或 map。
	Input any
	// Images 是一起判断的图片，可以用 Media.Name 命名。
	Images []Media
	// Questions 是要回答的问题。
	Questions []Question
}

// Validate 检查问题的名称、选项和级别。
func (r DecisionRequest) Validate() error {
	if r.Input == nil || r.Input == "" {
		return errors.New("ai: decision input is required")
	}
	if len(r.Questions) == 0 {
		return errors.New("ai: decision needs at least one question")
	}
	seen := map[string]bool{}
	for _, q := range r.Questions {
		if q.Name == "" {
			return errors.New("ai: decision question name is required")
		}
		if seen[q.Name] {
			return errors.Newf("ai: duplicate decision question %q", q.Name)
		}
		seen[q.Name] = true
		switch q.Kind {
		case YesNoQuestion:
		case ChoiceQuestion:
			if len(q.Options) < 2 {
				return errors.Newf("ai: choice question %q needs at least 2 options", q.Name)
			}
			values := map[string]bool{}
			for _, o := range q.Options {
				if o.Value == "" || values[o.Value] {
					return errors.Newf("ai: choice question %q has an empty or duplicate option", q.Name)
				}
				values[o.Value] = true
			}
		case ScoreQuestion:
			if len(q.Levels) < 2 {
				return errors.Newf("ai: score question %q needs at least 2 levels", q.Name)
			}
		default:
			return errors.Newf("ai: decision question %q has unknown kind %q", q.Name, q.Kind)
		}
	}
	return nil
}

// Answer 是一个问题的回答。
type Answer struct {
	Kind QuestionKind
	// Refused 表示模型拒绝回答这个问题，其余字段都是零值。
	Refused bool
	// Probability 是是非题中条件成立的概率。
	Probability float64
	// Choice 是选择题选中的选项值。
	Choice string
	// Score 是打分题各级别序号（从 0 开始）按概率加权的平均值，可能落在两个级别之间。
	Score float64
	// Probabilities 是选择题每个选项值的概率，或打分题每个级别的概率（按级别顺序）。
	Probabilities []float64
	// Confidence 是模型给出的置信度，没有给出时为 0。
	Confidence float64

	question Question
}

// NewAnswer 创建 q 的回答，供 Decider 的实现和测试替身使用；probabilities 按 q 的选项或级别顺序排列。
func NewAnswer(q Question, a Answer) Answer {
	a.Kind, a.question = q.Kind, q
	return a
}

// Yes 判断是非题的概率是否达到 threshold。拒答时返回 false。
func (a Answer) Yes(threshold float64) bool { return !a.Refused && a.Probability >= threshold }

// ProbabilityOf 返回选择题中选项 value 的概率。
func (a Answer) ProbabilityOf(value string) float64 {
	for i, o := range a.question.Options {
		if o.Value == value && i < len(a.Probabilities) {
			return a.Probabilities[i]
		}
	}
	return 0
}

// Level 返回打分题中概率最高的级别序号，从 0 开始；拒答时返回 -1。
func (a Answer) Level() int {
	if a.Refused || len(a.Probabilities) == 0 {
		return -1
	}
	best := 0
	for i, p := range a.Probabilities {
		if p > a.Probabilities[best] {
			best = i
		}
	}
	return best
}

// Label 返回打分题中概率最高的级别标签；拒答时返回空字符串。
func (a Answer) Label() string {
	if i := a.Level(); i >= 0 && i < len(a.question.Levels) {
		return a.question.Levels[i].Label
	}
	return ""
}

// Decision 是一次决策的结果。
type Decision struct {
	// Answers 按问题名称给出回答，每个问题都有。
	Answers map[string]Answer
	// Model 是实际使用的模型。
	Model string
	// InputTokens 和 OutputTokens 是用量，模型服务没有给出时为 0。
	InputTokens, OutputTokens int
}

// Decider 回答关于一份材料的是非、选择和打分问题，给出概率而不是生成文本。
type Decider interface {
	Decide(ctx context.Context, req DecisionRequest) (Decision, error)
}

// DecideBy 让 d 从 items 中选出最符合 instructions 的一项，option 给出每一项的值和说明，值不能重复。
// 选中项的概率低于 threshold 或模型拒答时返回 false。
func DecideBy[T any](ctx context.Context, d Decider, instructions string, input any, items []T, option func(T) ChoiceOption, threshold float64) (T, bool, error) {
	var zero T
	q := Choice("choice", instructions)
	for _, it := range items {
		q.Options = append(q.Options, option(it))
	}
	res, err := d.Decide(ctx, DecisionRequest{Input: input, Questions: []Question{q}})
	if err != nil {
		return zero, false, err
	}
	a := res.Answers[q.Name]
	if a.Refused {
		return zero, false, nil
	}
	for i, o := range q.Options {
		if o.Value == a.Choice {
			if a.ProbabilityOf(o.Value) < threshold {
				return zero, false, nil
			}
			return items[i], true, nil
		}
	}
	return zero, false, errors.Newf("ai: decision chose unknown option %q", a.Choice)
}
